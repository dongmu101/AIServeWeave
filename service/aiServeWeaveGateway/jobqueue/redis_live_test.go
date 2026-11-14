package jobqueue_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/service/aiServeWeaveGateway/jobqueue"
)

// liveRedis returns a client and a unique queue name when AISW_REDIS_ADDR
// names a reachable server. It is opt-in so `go test ./...` stays hermetic,
// and it never flushes: every key it creates carries the unique name and is
// deleted afterwards, so a shared developer Redis keeps its data.
//
// liveRedis 在 AISW_REDIS_ADDR 指向可达服务器时返回客户端与唯一队列名。它是选择性开启的，
// 使 `go test ./...` 保持自足；它从不 flush：创建的每个键都带唯一名称并在结束后删除，
// 因此共用的开发 Redis 不会丢数据。
func liveRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	addr := os.Getenv("AISW_REDIS_ADDR")
	if addr == "" {
		t.Skip("AISW_REDIS_ADDR not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 32})
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("AISW_REDIS_ADDR=%q unreachable: %v", addr, err)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "test-" + hex.EncodeToString(b[:])
	t.Cleanup(func() {
		for _, pattern := range []string{"aisw:jq:{" + name + "}:*", "asynq:{" + name + "}:*"} {
			iter := client.Scan(ctx, 0, pattern, 100).Iterator()
			for iter.Next(ctx) {
				client.Del(ctx, iter.Val())
			}
		}
		client.SRem(ctx, "asynq:queues", name)
		_ = client.Close()
	})
	return client, name
}

func TestRedisStoreTransitions(t *testing.T) {
	client, name := liveRedis(t)
	store := jobqueue.NewRedisStore(client, name)
	ctx := context.Background()
	now := time.Now()

	for i, id := range []string{"job_a", "job_b"} {
		position, err := store.Admit(ctx, jobqueue.Record{JobID: id, TenantID: "t1", WorkflowID: "wf", Kind: jobqueue.KindRun, CreatedAt: now}, 2, time.Hour, time.Hour)
		if err != nil || position != i+1 {
			t.Fatalf("Admit %s = %d %v", id, position, err)
		}
	}
	if _, err := store.Admit(ctx, jobqueue.Record{JobID: "job_c", CreatedAt: now}, 2, time.Hour, time.Hour); !errors.Is(err, jobqueue.ErrQueueFull) {
		t.Fatalf("Admit beyond the length = %v", err)
	}

	if got, err := store.BeginDispatch(ctx, "job_a", "r1:x", now); err != nil || got != jobqueue.Begun {
		t.Fatalf("BeginDispatch = %v %v", got, err)
	}
	if got, _ := store.BeginDispatch(ctx, "job_a", "r1:x", now); got != jobqueue.Begun {
		t.Fatalf("retried BeginDispatch with the same intent = %v, want begun", got)
	}
	if got, _ := store.BeginDispatch(ctx, "job_a", "r2:y", now); got != jobqueue.BeginNotQueued {
		t.Fatalf("a second worker = %v, want not queued", got)
	}
	if result, _ := store.Cancel(ctx, "job_a", now); result != jobqueue.CancelRequested {
		t.Fatalf("Cancel while dispatching = %v", result)
	}
	dispatched, err := store.Dispatched(ctx, "job_a", "r1:x", jobqueue.Binding{NodeID: "n1", RuntimeID: "c1", RunID: "p1"}, now)
	if err != nil || !dispatched.Recorded || !dispatched.CancelRequested {
		t.Fatalf("Dispatched = %+v %v, want recorded with the cancel request reported", dispatched, err)
	}
	if again, _ := store.Dispatched(ctx, "job_a", "r1:x", jobqueue.Binding{}, now); again.Recorded {
		t.Fatal("a second Dispatched on a decided record reported recorded")
	}
	rec, ok, err := store.Get(ctx, "job_a")
	if err != nil || !ok || rec.State != jobqueue.StateDispatched || rec.Binding.RunID != "p1" || rec.TenantID != "t1" {
		t.Fatalf("Get = %+v %v %v", rec, ok, err)
	}
	if position, _ := store.Position(ctx, "job_a"); position != 0 {
		t.Fatalf("a dispatched job holds position %d", position)
	}
	if position, _ := store.Position(ctx, "job_b"); position != 1 {
		t.Fatalf("job_b position = %d, want 1 once job_a left", position)
	}
	if result, _ := store.Cancel(ctx, "job_b", now); result != jobqueue.CancelDone {
		t.Fatalf("Cancel while queued = %v", result)
	}
	if ok, _ := store.Fail(ctx, "job_b", jobqueue.StateQueued, jobqueue.CodeQueueTimeout, now); ok {
		t.Fatal("a cancelled job was failed")
	}
	if depth, _ := store.Depth(ctx); depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}

	// A cancel that arrives while a worker holds the job survives the
	// worker handing it back: the requeue cancels it.
	if _, err := store.Admit(ctx, jobqueue.Record{JobID: "job_d", TenantID: "t1", CreatedAt: now}, 4, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	_, _ = store.BeginDispatch(ctx, "job_d", "r1:z", now)
	_, _ = store.Cancel(ctx, "job_d", now)
	if cancelled, err := store.Requeue(ctx, "job_d", "r1:z", now); err != nil || !cancelled {
		t.Fatalf("Requeue after a cancel = %v %v, want cancelled", cancelled, err)
	}
	rec, position, ok, err := store.Snapshot(ctx, "job_d")
	if err != nil || !ok || rec.State != jobqueue.StateCancelled || position != 0 {
		t.Fatalf("Snapshot after the cancelled requeue = %+v %d %v %v", rec, position, ok, err)
	}
	if _, _, ok, _ := store.Snapshot(ctx, "job_missing"); ok {
		t.Fatal("Snapshot of a missing job reported a record")
	}
}

// TestAsynqDeliveryEndToEnd runs a real asynq server against Redis: a task
// enqueued by one Queue is delivered to the worker, dispatched once, and its
// binding is visible to another Queue sharing the store (another replica).
func TestAsynqDeliveryEndToEnd(t *testing.T) {
	client, name := liveRedis(t)
	logger := slog.New(slog.DiscardHandler)
	newQueue := func(replica string, disp jobqueue.Dispatcher) *jobqueue.Queue {
		q, err := jobqueue.New(jobqueue.Config{
			Store: jobqueue.NewRedisStore(client, name),
			Transport: jobqueue.NewAsynqTransport(jobqueue.AsynqConfig{Client: client, Queue: name, Concurrency: 2,
				RetryInterval: time.Second, ShutdownTimeout: 2 * time.Second, Logger: logger}),
			Classify: classify, ReplicaID: replica, MaxWait: time.Minute, MaxLength: 8, RetryInterval: 100 * time.Millisecond, Logger: logger,
		})
		if err != nil {
			t.Fatal(err)
		}
		if disp != nil {
			if err := q.Start(disp); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(q.Shutdown)
		}
		return q
	}
	// The first two submits find the node busy, so the worker holds and re-polls.
	disp := &fakeDispatcher{answers: []error{errBusy, errBusy}}
	worker := newQueue("r1", disp)
	front := newQueue("r2", nil)

	now := time.Now()
	if _, err := front.Enqueue(context.Background(), jobqueue.Task{Kind: jobqueue.KindRun, JobID: "job_live", TenantID: "t1",
		WorkflowID: "wf", Graph: []byte(`{"1":{}}`), EnqueuedAt: now, Deadline: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rec, err := front.AwaitDispatch(ctx, "job_live", nil)
	if err != nil || rec.State != jobqueue.StateDispatched || rec.Binding.NodeID != "node-a" {
		t.Fatalf("AwaitDispatch = %+v %v", rec, err)
	}
	disp.mu.Lock()
	submits, dispatched := disp.submits, len(disp.dispatched)
	disp.mu.Unlock()
	if submits != 3 || dispatched != 1 {
		t.Fatalf("submits=%d dispatched=%d, want 3 attempts and one dispatch", submits, dispatched)
	}
	_ = worker
}
