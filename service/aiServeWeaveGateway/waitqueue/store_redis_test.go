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
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("AISW_REDIS_ADDR=%q unreachable: %v", addr, err)
	}
	store := newRedisStore(client, "test-"+newTicketID())
	t.Cleanup(func() {
		_ = client.Del(context.Background(), store.keys...).Err()
		_ = client.Close()
	})
	return store
}

func TestRedisStoreJoinCapAndFIFO(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	for _, ticket := range []string{"r1|a", "r2|b"} {
		if ok, err := s.join(ctx, ticket, 2, time.Minute); err != nil || !ok {
			t.Fatalf("join %s = %v %v", ticket, ok, err)
		}
	}
	if ok, err := s.join(ctx, "r1|c", 2, time.Minute); err != nil || ok {
		t.Fatalf("join beyond the cap = %v %v, want refused", ok, err)
	}
	if got, _ := s.poll(ctx, "r1|a", "r1|", time.Minute, 1, time.Hour); got != pollEligible {
		t.Fatalf("head poll = %v, want eligible", got)
	}
	if got, _ := s.poll(ctx, "r2|b", "r2|", time.Minute, 1, time.Hour); got != pollWait {
		t.Fatalf("second poll = %v, want wait", got)
	}
	if err := s.leave(ctx, "r1|a", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.poll(ctx, "r2|b", "r2|", time.Minute, 1, time.Hour); got != pollEligible {
		t.Fatalf("poll after the head left = %v, want eligible", got)
	}
	if got, _ := s.poll(ctx, "r1|a", "r1|", time.Minute, 1, time.Hour); got != pollLost {
		t.Fatalf("poll of a removed ticket = %v, want lost", got)
	}
}

func TestRedisStoreStallEscape(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	for _, ticket := range []string{"r1|head", "r2|x", "r2|y"} {
		if ok, err := s.join(ctx, ticket, 8, time.Minute); err != nil || !ok {
			t.Fatalf("join %s = %v %v", ticket, ok, err)
		}
	}
	stall := 50 * time.Millisecond
	if got, _ := s.poll(ctx, "r2|x", "r2|", time.Minute, 1, stall); got != pollWait {
		t.Fatalf("before the stall = %v, want wait", got)
	}
	time.Sleep(2 * stall)
	if got, _ := s.poll(ctx, "r2|x", "r2|", time.Minute, 1, stall); got != pollEligible {
		t.Fatalf("r2's oldest after the stall = %v, want eligible", got)
	}
	if got, _ := s.poll(ctx, "r2|y", "r2|", time.Minute, 1, stall); got != pollWait {
		t.Fatalf("r2's second ticket after the stall = %v, want wait", got)
	}
}

func TestRedisStoreExpiredLeaseIsPurged(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	if ok, err := s.join(ctx, "dead|a", 1, 50*time.Millisecond); err != nil || !ok {
		t.Fatalf("join = %v %v", ok, err)
	}
	time.Sleep(100 * time.Millisecond)
	// The crashed replica's ticket no longer holds the only slot.
	if ok, err := s.join(ctx, "live|b", 1, time.Minute); err != nil || !ok {
		t.Fatalf("join after expiry = %v %v, want the stale ticket purged", ok, err)
	}
	if got, _ := s.poll(ctx, "dead|a", "dead|", time.Minute, 1, time.Hour); got != pollLost {
		t.Fatalf("expired ticket = %v, want lost", got)
	}
}
