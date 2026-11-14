package waitqueue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// liveStore returns a Redis store when AISW_REDIS_ADDR names a reachable
// server. It is opt-in so `go test ./...` stays hermetic. Each test uses its
// own queue name instead of flushing, so a shared developer Redis keeps its
// data.
//
// liveStore 在 AISW_REDIS_ADDR 指向可达服务器时返回 Redis store。它是选择性开启的，
// 使 `go test ./...` 保持自足。每个测试使用自己的队列名而不是 flush，因此共用的开发
// Redis 不会丢数据。
func liveStore(t *testing.T) *redisStore {
	t.Helper()
	addr := os.Getenv("AISW_REDIS_ADDR")
	if addr == "" {
		t.Skip("AISW_REDIS_ADDR not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("AISW_REDIS_ADDR=%q unreachable: %v", addr, err)
	}
	store := newRedisStore(client, "test-"+newTicketID())
	t.Cleanup(func() {
		iter := client.Scan(ctx, 0, store.base+"*", 100).Iterator()
		for iter.Next(ctx) {
			client.Del(ctx, iter.Val())
		}
		_ = client.Close()
	})
	return store
}

func tk(replica, lane, id string) string { return replica + "|" + lane + "|" + id }

func TestRedisStoreJoinCapAcrossLanesAndFIFO(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	if ok, err := s.join(ctx, "a", tk("r1", "a", "1"), 2, time.Minute); err != nil || !ok {
		t.Fatalf("join = %v %v", ok, err)
	}
	if ok, err := s.join(ctx, "b", tk("r2", "b", "1"), 2, time.Minute); err != nil || !ok {
		t.Fatalf("join lane b = %v %v", ok, err)
	}
	if ok, err := s.join(ctx, "a", tk("r1", "a", "2"), 2, time.Minute); err != nil || ok {
		t.Fatalf("join beyond the cluster cap = %v %v, want refused", ok, err)
	}
	// Each lane's head is eligible on its own.
	got, seq, err := s.poll(ctx, "b", tk("r2", "b", "1"), "r2|", time.Minute, 1, time.Hour)
	if err != nil || got != pollEligible || seq <= 0 {
		t.Fatalf("lane b head = %v %d %v, want claimed", got, seq, err)
	}
	// Claimed: the ticket left the lane and freed a cap slot.
	if got, _, _ := s.poll(ctx, "b", tk("r2", "b", "1"), "r2|", time.Minute, 1, time.Hour); got != pollLost {
		t.Fatalf("claimed ticket poll = %v, want lost until rejoined", got)
	}
	if err := s.rejoin(ctx, "b", tk("r2", "b", "1"), seq, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, again, _ := s.poll(ctx, "b", tk("r2", "b", "1"), "r2|", time.Minute, 1, time.Hour); got != pollEligible || again != seq {
		t.Fatalf("rejoined poll = %v %d, want claimed at the same seq %d", got, again, seq)
	}
}

func TestRedisStoreRejoinKeepsThePlace(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	first, second := tk("r1", "a", "1"), tk("r1", "a", "2")
	_, _ = s.join(ctx, "a", first, 8, time.Minute)
	_, _ = s.join(ctx, "a", second, 8, time.Minute)
	_, seq, _ := s.poll(ctx, "a", first, "r1|", time.Minute, 1, time.Hour)
	// While first is out, second is the head.
	if got, _, _ := s.poll(ctx, "a", second, "r1|", time.Minute, 1, time.Hour); got != pollEligible {
		t.Fatalf("second while first is out = %v, want eligible", got)
	}
	_ = s.rejoin(ctx, "a", second, 2, time.Minute) // second's own seq is 2
	_ = s.rejoin(ctx, "a", first, seq, time.Minute)
	if got, _, _ := s.poll(ctx, "a", second, "r1|", time.Minute, 1, time.Hour); got != pollWait {
		t.Fatalf("second after first rejoined = %v, want wait behind it", got)
	}
}

func TestRedisStoreStallEscape(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	for _, ticket := range []string{tk("r1", "a", "head"), tk("r2", "a", "x"), tk("r2", "a", "y")} {
		if ok, err := s.join(ctx, "a", ticket, 8, time.Minute); err != nil || !ok {
			t.Fatalf("join %s = %v %v", ticket, ok, err)
		}
	}
	stall := 50 * time.Millisecond
	if got, _, _ := s.poll(ctx, "a", tk("r2", "a", "x"), "r2|", time.Minute, 1, stall); got != pollWait {
		t.Fatalf("before the stall = %v, want wait", got)
	}
	time.Sleep(2 * stall)
	if got, _, _ := s.poll(ctx, "a", tk("r2", "a", "y"), "r2|", time.Minute, 1, stall); got != pollWait {
		t.Fatalf("r2's second ticket after the stall = %v, want wait", got)
	}
	if got, _, _ := s.poll(ctx, "a", tk("r2", "a", "x"), "r2|", time.Minute, 1, stall); got != pollEligible {
		t.Fatalf("r2's oldest after the stall = %v, want eligible", got)
	}
}

func TestRedisStoreExpiredLeaseIsPurged(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	if ok, err := s.join(ctx, "a", tk("dead", "a", "1"), 1, 50*time.Millisecond); err != nil || !ok {
		t.Fatalf("join = %v %v", ok, err)
	}
	time.Sleep(100 * time.Millisecond)
	// The crashed replica's ticket no longer holds the only slot, even from
	// another lane.
	if ok, err := s.join(ctx, "b", tk("live", "b", "1"), 1, time.Minute); err != nil || !ok {
		t.Fatalf("join after expiry = %v %v, want the stale ticket purged", ok, err)
	}
	if got, _, _ := s.poll(ctx, "a", tk("dead", "a", "1"), "dead|", time.Minute, 1, time.Hour); got != pollLost {
		t.Fatalf("expired ticket = %v, want lost", got)
	}
}
