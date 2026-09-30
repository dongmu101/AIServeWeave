package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func probeRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:12345/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Content-Type", "application/json")
	return r
}

// TestMCPGuards checks local endpoint boundaries before tool dispatch.
// TestMCPGuards 检查工具分派前的本地端点边界。
func TestMCPGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"no token", func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"wrong token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer other") }, 401},
		{"browser", func(r *http.Request) { r.Header.Set("Origin", "http://evil.invalid") }, 403},
		{"host", func(r *http.Request) { r.Host = "evil.invalid" }, 403},
		{"get", func(r *http.Request) { r.Method = http.MethodGet }, 405},
		{"content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"path", func(r *http.Request) { r.URL.Path = "/other" }, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBroker(principal{"t", "k"})
			defer b.close()
			r := probeRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			tc.mutate(r)
			w := httptest.NewRecorder()
			mcpHandler(b, "test-token", "127.0.0.1:12345").ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// TestMCPProtocol checks JSON-RPC methods and malformed input.
// TestMCPProtocol 检查 JSON-RPC 方法及畸形输入。
func TestMCPProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, body, contains string
		status               int
	}{
		{"initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `"protocolVersion":"2025-06-18"`, 200},
		{"list", `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`, `"name":"probe_echo"`, 200},
		{"initialized", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "", 202},
		{"ping", `{"jsonrpc":"2.0","id":2,"method":"ping"}`, `"result":{}`, 200},
		{"unknown", `{"jsonrpc":"2.0","id":3,"method":"exec"}`, `"code":-32601`, 200},
		{"bad json", `{SECRET`, `invalid JSON-RPC request`, 400},
		{"trailing", `{"jsonrpc":"2.0","id":1,"method":"ping"} {}`, `invalid JSON-RPC request`, 400},
		{"bad version", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, `invalid JSON-RPC request`, 400},
		{"bad id", `{"jsonrpc":"2.0","id":{},"method":"ping"}`, `invalid JSON-RPC request`, 400},
		{"large", strings.Repeat("x", maxEventBytes+1), `invalid JSON-RPC request`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBroker(principal{"t", "k"})
			defer b.close()
			w := httptest.NewRecorder()
			mcpHandler(b, "test-token", "127.0.0.1:12345").ServeHTTP(w, probeRequest(tc.body))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("response = (%d, %s), want (%d, %s)", w.Code, w.Body.String(), tc.status, tc.contains)
			}
			if strings.Contains(w.Body.String(), "SECRET") {
				t.Fatal("response leaked input, want redacted error")
			}
		})
	}
}

// TestMCPToolRoundTrip forwards results through an authenticated request.
// TestMCPToolRoundTrip 通过已认证请求转发工具结果。
func TestMCPToolRoundTrip(t *testing.T) {
	b := newBroker(principal{"t", "k"})
	defer b.close()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mcpHandler(b, "test-token", "127.0.0.1:12345").ServeHTTP(w, probeRequest(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"probe_echo","arguments":{"label":"client"}}}`))
	}()
	c := <-b.calls
	if c.Label != "client" {
		t.Fatalf("label = %q, want client", c.Label)
	}
	if err := b.resolve(b.owner, c.ID, toolResult{Text: "client-result"}); err != nil {
		t.Fatal(err)
	}
	<-done
	var reply struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != 7 || len(reply.Result.Content) != 1 || reply.Result.Content[0].Text != "client-result" {
		t.Fatalf("reply = %+v, want id 7 and client-result", reply)
	}
}
