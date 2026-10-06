package messagesession

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"github.com/redis/go-redis/v9"
)

// TestKeysRejectIncompleteSets rejects duplicate and unbounded result sets.
// TestKeysRejectIncompleteSets 拒绝重复和无界的结果集合。
func TestKeysRejectIncompleteSets(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"empty", nil}, {"duplicate", []string{"a", "a"}}, {"empty ID", []string{""}}, {"too many", make([]string, 9)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := keysFor(tc.ids); err == nil {
				t.Fatal("error=nil, want invalid set")
			}
		})
	}
}

// TestRedisClaims verifies cross-client atomic ownership, rollback and one-shot consumption.
// TestRedisClaims 验证跨客户端原子归属、回滚及一次性消费。
func TestRedisClaims(t *testing.T) {
	addr := os.Getenv("AISW_MESSAGES_REDIS_ADDR")
	if addr == "" {
		t.Skip("set AISW_MESSAGES_REDIS_ADDR for isolated random-key Redis checks")
	}
	first := redis.NewClient(&redis.Options{Addr: addr})
	second := redis.NewClient(&redis.Options{Addr: addr})
	defer first.Close()
	defer second.Close()
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	ids := []string{"test-" + hex.EncodeToString(nonce[:]) + "-a", "test-" + hex.EncodeToString(nonce[:]) + "-b"}
	keys, _ := keysFor(ids)
	defer first.Del(t.Context(), keys...)
	a, b := NewRedis(first), NewRedis(second)
	record := Record{TenantID: "tenant", KeyID: "key", Model: "alias", IDs: ids, Candidate: scheduler.Candidate{NodeID: "node", RuntimeID: "local", Model: "sonnet"}}
	if err := a.Publish(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(t.Context(), record); err == nil {
		t.Fatal("duplicate publish accepted")
	}
	for _, tc := range []struct {
		name               string
		ids                []string
		tenant, key, model string
	}{
		{"other tenant", ids, "other", "key", "alias"}, {"other key", ids, "tenant", "other", "alias"}, {"other model", ids, "tenant", "key", "other"}, {"partial round", ids[:1], "tenant", "key", "alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := b.Claim(t.Context(), tc.ids, tc.tenant, tc.key, tc.model); err == nil {
				t.Fatal("invalid claim accepted")
			}
		})
	}
	_, finish, err := b.Claim(t.Context(), ids, "tenant", "key", "alias")
	if err != nil {
		t.Fatal(err)
	}
	if err := finish(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for _, store := range []*Redis{a, b} {
		wg.Go(func() {
			_, finish, err := store.Claim(t.Context(), ids, "tenant", "key", "alias")
			if err == nil {
				winners.Add(1)
				if err := finish(t.Context(), true); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("claim winners=%d,want 1", winners.Load())
	}
	if _, _, err := a.Claim(t.Context(), ids, "tenant", "key", "alias"); err == nil {
		t.Fatal("consumed result accepted")
	}
	if ttl := first.PTTL(t.Context(), keys[0]).Val(); ttl <= 0 || ttl > 2*time.Minute {
		t.Fatalf("TTL=%s,want (0,2m]", ttl)
	}
}
