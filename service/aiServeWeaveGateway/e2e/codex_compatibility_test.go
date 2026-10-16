//go:build darwin || linux

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/codexcli"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// TestCodexOutputLimitCompatibilityThroughTunnel exercises Harness-shaped requests over real mTLS.
// TestCodexOutputLimitCompatibilityThroughTunnel 通过真实 mTLS 验证 Harness 形状请求的输出上限兼容。
func TestCodexOutputLimitCompatibilityThroughTunnel(t *testing.T) {
	f := newFleetWithBackend(t, 1, runtime.KindCodex, codexcli.NewFactory(os.Args[0]))
	f.awaitReady(f.replicas...)
	api := httpapi.New(scheduler.New(f.replicas[0].server, scheduler.Config{}), httpapi.Config{APIKeys: []string{"test-key"}})
	t.Cleanup(api.Close)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name, path, parameter string
		stream                bool
	}{
		{"Chat JSON max_tokens", "/v1/chat/completions", "max_tokens", false},
		{"Chat SSE max_tokens", "/v1/chat/completions", "max_tokens", true},
		{"Harness Chat JSON", "/v1/chat/completions", "max_completion_tokens", false},
		{"Harness Chat SSE", "/v1/chat/completions", "max_completion_tokens", true},
		{"Chat both limit fields", "/v1/chat/completions", "max_tokens,max_completion_tokens", true},
		{"Responses JSON", "/v1/responses", "max_output_tokens", false},
		{"Responses SSE", "/v1/responses", "max_output_tokens", true},
		{"Messages JSON", "/v1/messages", "max_tokens", false},
		{"Messages SSE", "/v1/messages", "max_tokens", true},
		{"Ollama Chat JSON", "/api/chat", "options.num_predict", false},
		{"Ollama Chat NDJSON", "/api/chat", "options.num_predict", true},
		{"Ollama Generate JSON", "/api/generate", "options.num_predict", false},
		{"Ollama Generate NDJSON", "/api/generate", "options.num_predict", true},
		{"Chat without limit", "/v1/chat/completions", "", true},
		{"Responses without limit", "/v1/responses", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			function := map[string]any{"name": "echo", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}
			body := map[string]any{"model": "codex-fixture", "stream": tc.stream, "store": false}
			if tc.parameter != "" && tc.parameter != "options.num_predict" {
				for _, parameter := range strings.Split(tc.parameter, ",") {
					body[parameter] = 32768
				}
			}
			if tc.path == "/v1/chat/completions" {
				body["messages"] = []any{map[string]any{"role": "user", "content": "call echo"}}
				body["tools"] = []any{map[string]any{"type": "function", "function": function}}
				body["stream_options"] = map[string]bool{"include_usage": true}
			} else if tc.path == "/v1/responses" {
				body["input"] = "call echo"
				body["tools"] = []any{map[string]any{"type": "function", "name": "echo", "parameters": function["parameters"]}}
			} else if tc.path == "/v1/messages" {
				body["messages"] = []any{map[string]any{"role": "user", "content": "call echo"}}
				body["tools"] = []any{map[string]any{"name": "echo", "input_schema": function["parameters"]}}
				delete(body, "store")
			} else {
				delete(body, "store")
				body["options"] = map[string]any{"num_predict": 32768}
				if tc.path == "/api/chat" {
					body["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
				} else {
					body["prompt"] = "hello"
				}
			}
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+tc.path, bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err = io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d, want 200; body=%s", resp.StatusCode, data)
			}
			if got := resp.Header.Get("X-Gateway-Ignored-Parameters"); got != tc.parameter {
				t.Errorf("ignored parameters=%q, want %q", got, tc.parameter)
			}
			warning := resp.Header.Get("Warning")
			if tc.parameter != "" && !strings.Contains(warning, "output token limit is not enforced") {
				t.Errorf("Warning=%q, want explicit unenforced output token limit notice", warning)
			}
			if tc.parameter == "" && warning != "" {
				t.Errorf("Warning=%q, want no compatibility warning without a limit", warning)
			}
			want := "tool_calls"
			if tc.path == "/v1/responses" {
				want = "function_call"
			} else if tc.path == "/v1/messages" {
				want = "tool_use"
			} else if strings.HasPrefix(tc.path, "/api/") {
				want = "Hello world"
			}
			if !strings.Contains(string(data), want) {
				t.Errorf("response=%s, want %s from the actual Codex fixture", data, want)
			}
			if tc.stream && !strings.Contains(string(data), "[DONE]") && !strings.Contains(string(data), "response.completed") && !strings.Contains(string(data), "message_stop") && !strings.Contains(string(data), `"done":true`) {
				t.Errorf("stream=%s, want a successful completion marker", data)
			}
		})
	}
}

// TestLiveCodexOutputLimitCompatibility verifies the original Harness limit against local Codex inference.
// TestLiveCodexOutputLimitCompatibility 使用本机 Codex 真实推理验证 Harness 原有输出上限参数。
func TestLiveCodexOutputLimitCompatibility(t *testing.T) {
	if os.Getenv("AISW_CODEX_LIVE") != "1" {
		t.Skip("set AISW_CODEX_LIVE=1 to use local account quota")
	}
	f := newFleetWithBackend(t, 1, runtime.KindCodex, codexcli.New)
	f.awaitReady(f.replicas...)
	api := httpapi.New(scheduler.New(f.replicas[0].server, scheduler.Config{}), httpapi.Config{APIKeys: []string{"live-fixture-key"}})
	t.Cleanup(api.Close)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Reply OK only."}],"stream":true,"stream_options":{"include_usage":true},"store":false,"max_completion_tokens":32768}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer live-fixture-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Gateway-Ignored-Parameters"); got != "max_completion_tokens" {
		t.Errorf("ignored parameters=%q, want max_completion_tokens", got)
	}
	if got := resp.Header.Get("Warning"); !strings.Contains(got, "output token limit is not enforced") {
		t.Errorf("Warning=%q, want explicit output limit notice", got)
	}
	if !bytes.Contains(data, []byte("[DONE]")) || !bytes.Contains(data, []byte(`"content":"`)) || bytes.Contains(data, []byte(`"error"`)) {
		t.Fatal("response must contain text and successful stream completion without an error; contents withheld")
	}
}
