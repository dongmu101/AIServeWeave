package modelpullrouter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/modelpullrouter"
)

// TestMain asserts no test in this package leaks a goroutine — the router
// fans out to every replica the same way fleet.Aggregator does, so a fan-out
// that forgot to wait would show up here.
//
// TestMain 断言本包没有测试泄漏协程——本包的 fan-out 与 fleet.Aggregator 同一
// 写法，因此一次忘了等待的扇出会在这里暴露。
func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !settles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func settles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const token = "model-pull-token-long-enough-for-a-test"

// pull is one replica's fake pull-status listener, mirroring
// modelpullapi's contract: POST answers 202 or 404 by connected, GET answers
// the given pulls or 404.
//
// pull 是单个副本的假模型拉取监听器，与 modelpullapi 的契约一致：POST 按
// connected 回 202 或 404，GET 回给定的 pulls 或 404。
func pull(t *testing.T, connected bool, pulls []map[string]any) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !connected {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"generated_at": time.Now().UTC().Format(time.RFC3339),
				"pulls":        pulls,
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func router(t *testing.T, endpoints ...string) *modelpullrouter.Router {
	t.Helper()
	return modelpullrouter.New(modelpullrouter.Config{Gateways: endpoints, Token: token, Timeout: time.Second})
}

func TestTriggerReportsConnectedWhenAnyReplicaHasTheNode(t *testing.T) {
	elsewhere := pull(t, false, nil)
	here := pull(t, true, nil)

	result, err := router(t, elsewhere, here).Trigger(context.Background(), "node-a", []string{"qwen3-coder:30b"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !result.Connected {
		t.Fatalf("Connected = false, want true when one replica has the node")
	}
	if len(result.Replicas) != 2 {
		t.Fatalf("got %d replica statuses, want 2", len(result.Replicas))
	}
	if result.Replicas[0].Endpoint != elsewhere || result.Replicas[0].Connected {
		t.Errorf("replica[0] = %+v, want the unconnected endpoint reported not connected", result.Replicas[0])
	}
	if result.Replicas[1].Endpoint != here || !result.Replicas[1].Connected {
		t.Errorf("replica[1] = %+v, want the connected endpoint reported connected", result.Replicas[1])
	}
}

func TestTriggerReportsNotConnectedWhenNoReplicaHasTheNode(t *testing.T) {
	a := pull(t, false, nil)
	b := pull(t, false, nil)

	result, err := router(t, a, b).Trigger(context.Background(), "node-a", []string{"qwen3-coder:30b"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if result.Connected {
		t.Fatalf("Connected = true, want false when no replica has the node")
	}
}

func TestStatusReturnsThePullsFromTheConnectedReplica(t *testing.T) {
	here := pull(t, true, []map[string]any{
		{"name": "qwen3-coder:30b", "state": "downloading", "bytes_downloaded": 1024, "bytes_total": 4096, "updated_at": time.Now().UTC().Format(time.RFC3339)},
	})
	elsewhere := pull(t, false, nil)

	result, err := router(t, elsewhere, here).Status(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !result.Connected {
		t.Fatalf("Connected = false, want true")
	}
	if len(result.Pulls) != 1 || result.Pulls[0].Name != "qwen3-coder:30b" || result.Pulls[0].State != "downloading" {
		t.Fatalf("Pulls = %+v, want the one entry the connected replica reported", result.Pulls)
	}
	if result.Pulls[0].BytesDownloaded != 1024 || result.Pulls[0].BytesTotal != 4096 {
		t.Errorf("Pulls[0] byte counters = %d/%d, want 1024/4096", result.Pulls[0].BytesDownloaded, result.Pulls[0].BytesTotal)
	}
}

// TestStatusPrefersTheFreshestReplica asserts that when the node is
// connected to more than one configured replica (the Agent maintains
// simultaneous connections for redundancy), the reply with the more
// recently updated entry wins rather than an arbitrary one.
//
// TestStatusPrefersTheFreshestReplica 断言当节点连到一个以上已配置副本时
// （Agent 为冗余同时维持多个连接），胜出的是更新时间更晚的那份回复，而不是
// 任意一份。
func TestStatusPrefersTheFreshestReplica(t *testing.T) {
	older := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	newer := time.Now().UTC().Format(time.RFC3339)

	stale := pull(t, true, []map[string]any{
		{"name": "qwen3-coder:30b", "state": "pending", "updated_at": older},
	})
	fresh := pull(t, true, []map[string]any{
		{"name": "qwen3-coder:30b", "state": "downloading", "updated_at": newer},
	})

	result, err := router(t, stale, fresh).Status(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(result.Pulls) != 1 || result.Pulls[0].State != "downloading" {
		t.Fatalf("Pulls = %+v, want the fresher replica's downloading state", result.Pulls)
	}
}

func TestFailuresAreClassifiedAndNeverCarryTransportText(t *testing.T) {
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(unauthorized.Close)

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{this is not json"))
	}))
	t.Cleanup(broken.Close)

	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	result, err := router(t, unauthorized.URL, broken.URL, goneURL).Status(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result.Connected {
		t.Fatalf("Connected = true, want false: every configured replica failed")
	}

	wantErrors := map[string]string{
		unauthorized.URL: "unauthorized",
		broken.URL:       "malformed",
		goneURL:          "unreachable",
	}
	if len(result.Replicas) != len(wantErrors) {
		t.Fatalf("got %d replica statuses, want %d", len(result.Replicas), len(wantErrors))
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshalling the result: %v", err)
	}
	for _, status := range result.Replicas {
		want, known := wantErrors[status.Endpoint]
		if !known {
			t.Errorf("unexpected endpoint %q", status.Endpoint)
			continue
		}
		if status.Error != want {
			t.Errorf("%s: Error = %q, want %q", status.Endpoint, status.Error, want)
		}
	}
	for _, leak := range []string{"connection refused", "dial tcp", "EOF", "invalid character"} {
		if containsText(string(body), leak) {
			t.Errorf("the result carried transport text %q:\n%s", leak, body)
		}
	}
}

// TestASlowReplicaDoesNotHoldTheAnswer asserts the timeout bounds one
// replica rather than the whole call: a replica that did answer is still
// reported.
//
// TestASlowReplicaDoesNotHoldTheAnswer 断言超时限制的是单个副本而不是整次调用：
// 已经作答的副本依然会被报告。
func TestASlowReplicaDoesNotHoldTheAnswer(t *testing.T) {
	fast := pull(t, true, nil)

	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(block)
		slow.Close()
	})

	r := modelpullrouter.New(modelpullrouter.Config{
		Gateways: []string{fast, slow.URL},
		Token:    token,
		Timeout:  100 * time.Millisecond,
	})

	started := time.Now()
	result, err := r.Trigger(context.Background(), "node-a", []string{"m"})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Trigger took %s, want it bounded by the 100ms per-replica timeout", elapsed)
	}
	if !result.Connected {
		t.Fatalf("Connected = false, want true: the fast replica answered")
	}
	for _, status := range result.Replicas {
		if status.Endpoint == slow.URL && status.Error != "timeout" {
			t.Errorf("slow replica Error = %q, want %q", status.Error, "timeout")
		}
	}
}

func TestUnconfiguredRouterIsDisabled(t *testing.T) {
	r := modelpullrouter.New(modelpullrouter.Config{})
	if r != nil {
		t.Fatal("New() returned a router for an empty configuration")
	}
	if _, err := r.Trigger(context.Background(), "node-a", []string{"m"}); err != modelpullrouter.ErrDisabled {
		t.Errorf("Trigger() error = %v, want ErrDisabled", err)
	}
	if _, err := r.Status(context.Background(), "node-a"); err != modelpullrouter.ErrDisabled {
		t.Errorf("Status() error = %v, want ErrDisabled", err)
	}
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
