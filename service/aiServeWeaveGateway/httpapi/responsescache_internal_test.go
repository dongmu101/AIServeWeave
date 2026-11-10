package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// countingResponsesClient is an in-memory control plane that counts reads, so
// a test can tell a cache hit from a round trip.
//
// countingResponsesClient 是一个会统计读取次数的内存控制面，测试据此区分缓存命中与
// 一次真实往返。
type countingResponsesClient struct {
	mu    sync.Mutex
	turns map[string]cachedTurnFixture
	gets  int
}

type cachedTurnFixture struct {
	previous string
	messages json.RawMessage
}

func newCountingResponsesClient() *countingResponsesClient {
	return &countingResponsesClient{turns: map[string]cachedTurnFixture{}}
}

func (c *countingResponsesClient) seed(tenantID, id, previous, messages string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns[tenantID+"/"+id] = cachedTurnFixture{previous, json.RawMessage(messages)}
}

func (c *countingResponsesClient) CreateResponseTurn(_ context.Context, id, tenantID, previous, _ string, messages json.RawMessage) error {
	c.seed(tenantID, id, previous, string(messages))
	return nil
}

func (c *countingResponsesClient) GetResponseTurn(_ context.Context, tenantID, id string) (string, json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	turn, ok := c.turns[tenantID+"/"+id]
	if !ok {
		return "", nil, ErrResponseTurnNotFound
	}
	return turn.previous, turn.messages, nil
}

func (c *countingResponsesClient) getCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets
}

func seedChain(c *countingResponsesClient, tenantID string, depth int) string {
	for i := 0; i < depth; i++ {
		previous := ""
		if i > 0 {
			previous = fmt.Sprintf("resp_%d", i-1)
		}
		c.seed(tenantID, fmt.Sprintf("resp_%d", i), previous, fmt.Sprintf(`[{"Role":"user","Content":"turn %d"}]`, i))
	}
	return fmt.Sprintf("resp_%d", depth-1)
}

// TestLoadResponsePrefixSecondContinuationSkipsTheControlPlane proves the
// chain is fetched once: the first continuation pays one round trip per hop,
// the second one is served entirely from the cache and returns the same
// messages.
//
// TestLoadResponsePrefixSecondContinuationSkipsTheControlPlane 证明链只取一次：第一次
// 续接每一跳付出一次往返，第二次完全由缓存提供，并返回相同的消息。
func TestLoadResponsePrefixSecondContinuationSkipsTheControlPlane(t *testing.T) {
	client := newCountingResponsesClient()
	head := seedChain(client, "tnt_1", 20)
	h := &handlers{responsePersist: newResponsePersister(client, 1, time.Second, discardLogger(), nil)}

	first, err := h.loadResponsePrefix(context.Background(), "tnt_1", head)
	if err != nil {
		t.Fatalf("first loadResponsePrefix: %v", err)
	}
	if got := client.getCount(); got != 20 {
		t.Fatalf("control plane reads after the first continuation = %d, want 20", got)
	}
	second, err := h.loadResponsePrefix(context.Background(), "tnt_1", head)
	if err != nil {
		t.Fatalf("second loadResponsePrefix: %v", err)
	}
	if got := client.getCount(); got != 20 {
		t.Fatalf("control plane reads after the second continuation = %d, want still 20 (served from cache)", got)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if len(first) != 20 || string(a) != string(b) {
		t.Fatalf("second prefix differs from the first: %d vs %d messages", len(first), len(second))
	}
}

// TestResponseTurnCacheIsTenantScoped proves a cached turn is invisible to
// every other tenant: the other tenant still asks the control plane, which
// answers not-found for an id it does not own.
//
// TestResponseTurnCacheIsTenantScoped 证明被缓存的一轮对其他租户不可见：其他租户仍然
// 会去问控制面，而控制面对不属于它的 id 答复未找到。
func TestResponseTurnCacheIsTenantScoped(t *testing.T) {
	client := newCountingResponsesClient()
	client.seed("tnt_a", "resp_1", "", `[{"Role":"user","Content":"secret of a"}]`)
	h := &handlers{responsePersist: newResponsePersister(client, 1, time.Second, discardLogger(), nil)}

	if _, err := h.loadResponsePrefix(context.Background(), "tnt_a", "resp_1"); err != nil {
		t.Fatalf("tenant a: %v", err)
	}
	before := client.getCount()
	_, err := h.loadResponsePrefix(context.Background(), "tnt_b", "resp_1")
	if !errors.Is(err, ErrResponseTurnNotFound) {
		t.Fatalf("tenant b err = %v, want ErrResponseTurnNotFound (a's cached turn must not be reachable)", err)
	}
	if client.getCount() != before+1 {
		t.Fatalf("tenant b reads = %d, want one control plane read (cache miss)", client.getCount()-before)
	}
	// And a's own entry is still served from the cache afterwards.
	if _, err := h.loadResponsePrefix(context.Background(), "tnt_a", "resp_1"); err != nil || client.getCount() != before+1 {
		t.Fatalf("tenant a again: err=%v reads=%d, want a cache hit", err, client.getCount()-before)
	}
}

// TestResponseTurnCacheDoesNotRememberAbsence proves a not-found answer is
// never cached: a turn that is persisted a moment later must be found.
//
// TestResponseTurnCacheDoesNotRememberAbsence 证明「未找到」的答复从不被缓存：稍后才
// 持久化的一轮必须能被找到。
func TestResponseTurnCacheDoesNotRememberAbsence(t *testing.T) {
	client := newCountingResponsesClient()
	h := &handlers{responsePersist: newResponsePersister(client, 1, time.Second, discardLogger(), nil)}

	if _, err := h.loadResponsePrefix(context.Background(), "tnt_1", "resp_late"); !errors.Is(err, ErrResponseTurnNotFound) {
		t.Fatalf("before persisting: err = %v, want not found", err)
	}
	client.seed("tnt_1", "resp_late", "", `[{"Role":"user","Content":"hi"}]`)
	if _, err := h.loadResponsePrefix(context.Background(), "tnt_1", "resp_late"); err != nil {
		t.Fatalf("after persisting: %v, want the turn to be found", err)
	}
}

// TestResponsePersistPopulatesTheCacheOnlyOnSuccess proves the write half
// seeds the cache with an acknowledged turn, and leaves a failed write out.
//
// TestResponsePersistPopulatesTheCacheOnlyOnSuccess 证明写入那一半只把已确认的一轮放进
// 缓存，写入失败的则不放。
func TestResponsePersistPopulatesTheCacheOnlyOnSuccess(t *testing.T) {
	client := newCountingResponsesClient()
	p := newResponsePersister(client, 4, time.Second, discardLogger(), nil)
	p.persist("resp_ok", "tnt_1", "", "m", json.RawMessage(`[{"Role":"user","Content":"hi"}]`))
	gatewaytest.WaitFor(t, "the persisted turn to be cached", func() bool {
		_, _, ok := p.cache.get("tnt_1", "resp_ok")
		return ok
	})
	if _, _, err := p.get(context.Background(), "tnt_1", "resp_ok"); err != nil || client.getCount() != 0 {
		t.Fatalf("get after persist: err=%v reads=%d, want a cache hit with no control plane read", err, client.getCount())
	}

	failing := &failingCreateClient{countingResponsesClient: newCountingResponsesClient()}
	pf := newResponsePersister(failing, 4, time.Second, discardLogger(), nil)
	pf.persist("resp_bad", "tnt_1", "", "m", json.RawMessage(`[]`))
	gatewaytest.WaitFor(t, "the failing create to be attempted", func() bool { return failing.attempts() == 1 })
	// Give the goroutine's tail a moment to (incorrectly) cache.
	time.Sleep(20 * time.Millisecond)
	if _, _, ok := pf.cache.get("tnt_1", "resp_bad"); ok {
		t.Fatal("a turn whose write failed must not be cached")
	}
}

type failingCreateClient struct {
	*countingResponsesClient
	n int
}

func (c *failingCreateClient) CreateResponseTurn(context.Context, string, string, string, string, json.RawMessage) error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return errors.New("control plane down")
}

func (c *failingCreateClient) attempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// TestResponseTurnCacheIsBoundedInBytes proves the byte budget holds: filling
// the cache with far more turns than fit keeps the accounted size under the
// limit, evicts the least recently used turn first, and refuses a turn that
// alone would take a large share of the budget.
//
// TestResponseTurnCacheIsBoundedInBytes 证明字节预算成立：塞入远超容量的轮次后，记账
// 的大小仍低于上限，最久未使用的一轮先被淘汰，单个就占预算很大一部分的轮次则被拒绝。
func TestResponseTurnCacheIsBoundedInBytes(t *testing.T) {
	const limit = 64 << 10
	c := newResponseTurnCache(limit, time.Hour)
	body := json.RawMessage(`"` + strings.Repeat("x", 4<<10) + `"`)
	for i := 0; i < 100; i++ {
		c.put("tnt", fmt.Sprintf("resp_%d", i), "", body)
		if c.used > limit {
			t.Fatalf("after %d puts the cache holds %d bytes, over the %d limit", i+1, c.used, limit)
		}
	}
	if _, _, ok := c.get("tnt", "resp_0"); ok {
		t.Fatal("the oldest turn should have been evicted")
	}
	if _, _, ok := c.get("tnt", "resp_99"); !ok {
		t.Fatal("the newest turn should still be cached")
	}
	// Touching an entry protects it from the next eviction.
	_, _, _ = c.get("tnt", "resp_99")
	huge := json.RawMessage(`"` + strings.Repeat("y", limit/2) + `"`)
	c.put("tnt", "resp_huge", "", huge)
	if _, _, ok := c.get("tnt", "resp_huge"); ok {
		t.Fatal("a turn taking more than a quarter of the budget must not be cached")
	}
	if c.used > limit {
		t.Fatalf("cache holds %d bytes, over the %d limit", c.used, limit)
	}
}

// TestResponseTurnCacheExpires proves the TTL bounds how long a cached turn
// is served, so a turn removed from the control plane is eventually dropped.
//
// TestResponseTurnCacheExpires 证明 TTL 限定了被缓存的一轮可被提供的时长，因此从控制面
// 移除的一轮最终会被丢弃。
func TestResponseTurnCacheExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newResponseTurnCache(1<<20, time.Minute)
	c.now = func() time.Time { return now }
	c.put("tnt", "resp_1", "", json.RawMessage(`[]`))
	now = now.Add(59 * time.Second)
	if _, _, ok := c.get("tnt", "resp_1"); !ok {
		t.Fatal("entry should still be valid before the TTL")
	}
	now = now.Add(2 * time.Second)
	if _, _, ok := c.get("tnt", "resp_1"); ok {
		t.Fatal("entry should have expired")
	}
	if c.used != 0 || len(c.entries) != 0 {
		t.Fatalf("expired entry still accounted: used=%d entries=%d", c.used, len(c.entries))
	}
}
