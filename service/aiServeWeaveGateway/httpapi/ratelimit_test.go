package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/quota"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/ratelimit"
)

// limitedVerifier authenticates one key and attaches limits to it, which is
// how a tenant's quota reaches the Gateway in production too: with the
// identity, from the same verification.
//
// limitedVerifier 认证一个 key 并给它附上限制，这也是生产环境中租户配额抵达 Gateway
// 的方式：与身份一起，来自同一次校验。
func limitedVerifier(limits quota.Limits) *fakeVerifier {
	return &fakeVerifier{answer: func(key string) (httpapi.Identity, error) {
		if key == goodKey {
			return httpapi.Identity{TenantID: "tnt_1", KeyID: "key_1", Limits: limits}, nil
		}
		return httpapi.Identity{}, httpapi.ErrKeyRejected
	}}
}

// chat posts one chat request with the given key.
//
// chat 用给定的 key 发一次聊天请求。
func chat(t *testing.T, url, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen3:8b","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

func TestRateLimitRejectsOverTheRequestAllowance(t *testing.T) {
	limits := quota.Limits{RequestsPerMinute: 2}
	clock := gatewaytest.NewClock()
	srv, h := newServer(t, httpapi.Config{
		Verifier: limitedVerifier(limits),
		Limiter:  ratelimit.NewMemory(ratelimit.MemoryConfig{Clock: clock}),
	})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	for i := 0; i < 2; i++ {
		resp := chat(t, srv.URL, goodKey)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 within the allowance", i, resp.StatusCode)
		}
	}

	resp := chat(t, srv.URL, goodKey)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 past the allowance", resp.StatusCode)
	}
	// A caller that honours Retry-After retries once, at the right moment,
	// instead of polling. Without the header it can only guess, and guessing
	// wrong is what turns one rejected request into a retry storm.
	//
	// 遵守 Retry-After 的调用方只会在正确的时刻重试一次，而不是轮询。没有这个头它
	// 只能猜，而猜错正是把一次被拒的请求变成重试风暴的原因。
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After is absent on a 429")
	}
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds <= 0 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", retryAfter)
	}

	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding error body: %v", err)
	}
	if body.Error.Type != "rate_limit_error" {
		t.Errorf("error.type = %q, want rate_limit_error", body.Error.Type)
	}
	if body.Error.Code != string(ratelimit.ReasonRequests) {
		t.Errorf("error.code = %q, want %q", body.Error.Code, ratelimit.ReasonRequests)
	}
}

func TestRateLimitLetsAnUnconfiguredTenantThrough(t *testing.T) {
	clock := gatewaytest.NewClock()
	srv, h := newServer(t, httpapi.Config{
		Verifier: limitedVerifier(quota.Limits{}),
		Limiter:  ratelimit.NewMemory(ratelimit.MemoryConfig{Clock: clock}),
	})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	for i := 0; i < 20; i++ {
		resp := chat(t, srv.URL, goodKey)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 for a tenant with no limits", i, resp.StatusCode)
		}
	}
}

// TestRateLimitChargesReportedTokens is the dimension that measures work
// rather than frequency: the fake backend reports 8 tokens per response, so a
// 10-token allowance is gone after the second request even though the request
// rate was never touched.
//
// TestRateLimitChargesReportedTokens 是那个衡量工作量而非频率的维度：假后端每次响应
// 上报 8 个 token，因此 10 个 token 的额度在第二个请求后就用尽了，尽管请求频率从未
// 被触及。
func TestRateLimitChargesReportedTokens(t *testing.T) {
	clock := gatewaytest.NewClock()
	srv, h := newServer(t, httpapi.Config{
		Verifier: limitedVerifier(quota.Limits{TokensPerMinute: 10}),
		Limiter:  ratelimit.NewMemory(ratelimit.MemoryConfig{Clock: clock}),
	})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := chat(t, srv.URL, goodKey)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", resp.StatusCode)
	}

	resp = chat(t, srv.URL, goodKey)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second request: status = %d, want 200 — the overspend is paid by the requests behind it", resp.StatusCode)
	}

	resp = chat(t, srv.URL, goodKey)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("third request: status = %d, want 429 once the token allowance was spent", resp.StatusCode)
	}
}

// TestRateLimitIsSkippedWithoutAuthentication covers the local-development
// mode: with no verifier every request is anonymous, so there is no tenant to
// bill and nothing to enforce against.
//
// TestRateLimitIsSkippedWithoutAuthentication 覆盖本地开发模式：没有校验器时每个请求
// 都是匿名的，因此没有可记账的租户，也就无从执行。
func TestRateLimitIsSkippedWithoutAuthentication(t *testing.T) {
	clock := gatewaytest.NewClock()
	srv, h := newServer(t, httpapi.Config{
		Limiter: ratelimit.NewMemory(ratelimit.MemoryConfig{Clock: clock}),
	})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	for i := 0; i < 10; i++ {
		resp := chat(t, srv.URL, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, resp.StatusCode)
		}
	}
}

// TestRateLimitMetricsCarryNoTenantLabel is this feature's cardinality test.
// A tenant id is exactly the kind of value that looks harmless as a label and
// grows without bound as the deployment succeeds.
//
// TestRateLimitMetricsCarryNoTenantLabel 是本功能的基数测试。租户 id 恰恰是那种「作为
// 标签看着人畜无害、却随着部署成功而无界增长」的取值。
func TestRateLimitMetricsCarryNoTenantLabel(t *testing.T) {
	mx := metricstest.New()
	clock := gatewaytest.NewClock()
	srv, h := newServer(t, httpapi.Config{
		Verifier: limitedVerifier(quota.Limits{RequestsPerMinute: 1}),
		Limiter:  ratelimit.NewMemory(ratelimit.MemoryConfig{Clock: clock}),
		Metrics:  mx,
	})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	chat(t, srv.URL, goodKey).Body.Close()
	chat(t, srv.URL, goodKey).Body.Close() // rejected

	found := false
	for _, s := range mx.All() {
		for key, value := range s.Labels {
			if value == "tnt_1" || key == "tenant" || key == "tenant_id" {
				t.Errorf("metric %s carries %s=%q: a tenant id reached a label", s.Name, key, value)
			}
		}
		if s.Name == httpapi.MetricRateLimitedTotal {
			found = true
			if got := s.Labels[httpapi.LabelReason]; got != string(ratelimit.ReasonRequests) {
				t.Errorf("%s reason = %q, want %q", s.Name, got, ratelimit.ReasonRequests)
			}
		}
	}
	if !found {
		t.Errorf("no %s series was recorded", httpapi.MetricRateLimitedTotal)
	}
}
