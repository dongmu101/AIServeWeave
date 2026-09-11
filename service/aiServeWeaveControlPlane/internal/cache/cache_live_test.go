package cache

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
)

func liveRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("AISW_REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("AISW_REDIS_TEST_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Ping error = %v, want nil", err)
	}
	return client
}

func waitForSubscribers(t *testing.T, ctx context.Context, client *redis.Client, channel string, want int64) {
	t.Helper()
	for {
		counts, err := client.PubSubNumSub(ctx, channel).Result()
		if err != nil {
			t.Fatalf("PubSubNumSub error = %v, want nil", err)
		}
		if counts[channel] >= want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("subscribers = %d, want at least %d", counts[channel], want)
		default:
			runtime.Gosched()
		}
	}
}

func TestLiveGenerationPreventsStaleVerificationRepopulation(t *testing.T) {
	client := liveRedisClient(t)
	ctx := context.Background()

	verifications := NewWithClient(client, time.Minute)
	hash := "cache-live-hash-" + time.Now().UTC().Format("20060102150405.000000000")
	want := logic.Verification{TenantID: "tnt_1", KeyID: "key_1"}
	_, found, generation := verifications.Get(ctx, hash)
	if found {
		t.Fatal("Get found a verification before Put")
	}
	verifications.Put(ctx, hash, want, generation)
	if got, found, _ := verifications.Get(ctx, hash); !found || got != want {
		t.Errorf("Get after Put = (%+v, %v), want (%+v, true)", got, found, want)
	}

	verifications.InvalidateAll(ctx)
	verifications.Put(ctx, hash, want, generation)
	if got, found, newGeneration := verifications.Get(ctx, hash); found || newGeneration == generation {
		t.Errorf("Get after stale Put = (%+v, %v, %d), want miss at a generation other than than %d", got, found, newGeneration, generation)
	}
}

// TestLiveInvalidationPublishesToEveryWatcher proves Pub/Sub wakes every
// Gateway waiter rather than load-balancing one invalidation between them.
//
// TestLiveInvalidationPublishesToEveryWatcher 证明 Pub/Sub 会唤醒每个 Gateway
// 等待者，而不是把一次失效通知在它们之间做负载均衡。
func TestLiveInvalidationPublishesToEveryWatcher(t *testing.T) {
	client := liveRedisClient(t)
	verifications := NewWithClient(client, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	before, err := verifications.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation error = %v, want nil", err)
	}
	type result struct {
		generation int64
		err        error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			generation, err := verifications.WatchGeneration(ctx, before, time.Minute)
			results <- result{generation: generation, err: err}
		}()
	}
	waitForSubscribers(t, ctx, client, notificationChannel, 2)
	verifications.InvalidateAll(ctx)

	for i := range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("watcher %d error = %v, want nil", i, got.err)
		}
		if got.generation != before+1 {
			t.Errorf("watcher %d generation = %d, want %d", i, got.generation, before+1)
		}
	}
}

// TestLiveWatcherCatchesUpWithoutPubSub proves the stored cursor repairs a
// notification missed before a Gateway subscribes.
//
// TestLiveWatcherCatchesUpWithoutPubSub 证明持久游标能补偿 Gateway 订阅前漏掉的通知。
func TestLiveWatcherCatchesUpWithoutPubSub(t *testing.T) {
	client := liveRedisClient(t)
	verifications := NewWithClient(client, time.Minute)
	ctx := context.Background()

	before, err := verifications.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation error = %v, want nil", err)
	}
	verifications.InvalidateAll(ctx)
	got, err := verifications.WatchGeneration(ctx, before, time.Minute)
	if err != nil || got != before+1 {
		t.Fatalf("WatchGeneration = (%d, %v), want (%d, nil)", got, err, before+1)
	}
}

// TestLiveWatchHeartbeatReturnsCurrentGeneration proves an unchanged watch
// returns the authoritative cursor when its bounded wait elapses.
//
// TestLiveWatchHeartbeatReturnsCurrentGeneration 证明没有变化的监听会在有界等待结束时
// 返回权威游标。
func TestLiveWatchHeartbeatReturnsCurrentGeneration(t *testing.T) {
	client := liveRedisClient(t)
	verifications := NewWithClient(client, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	want, err := verifications.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation error = %v, want nil", err)
	}
	got, err := verifications.WatchGeneration(ctx, want, 10*time.Millisecond)
	if err != nil || got != want {
		t.Fatalf("WatchGeneration heartbeat = (%d, %v), want (%d, nil)", got, err, want)
	}
}

// TestLiveWatchReturnsWhenClientCloses proves a broken Redis connection
// releases an outstanding watch so the HTTP layer can report 503.
//
// TestLiveWatchReturnsWhenClientCloses 证明 Redis 连接断开会释放尚未完成的监听，
// 从而让 HTTP 层可以返回 503。
func TestLiveWatchReturnsWhenClientCloses(t *testing.T) {
	client := liveRedisClient(t)
	verifications := NewWithClient(client, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	after, err := verifications.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation error = %v, want nil", err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := verifications.WatchGeneration(ctx, after, time.Minute)
		result <- err
	}()
	waitForSubscribers(t, ctx, client, notificationChannel, 1)
	if err := client.Close(); err != nil {
		t.Fatalf("Close error = %v, want nil", err)
	}
	if err := <-result; err == nil {
		t.Fatal("WatchGeneration error = nil, want client-closed failure")
	}
}
