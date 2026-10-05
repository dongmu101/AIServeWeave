//go:build darwin || linux

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/codexcli"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// TestMain supplies an offline Codex subprocess and verifies package goroutine cleanup.
// TestMain 提供离线 Codex 子进程，并统一验证包的协程回收。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "app-server" {
		codexServerFixture()
		os.Exit(0)
	}
	before := goruntime.NumGoroutine()
	code := m.Run()
	deadline := time.Now().Add(3 * time.Second)
	for code == 0 && goruntime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "goroutines remain, want initial count")
			code = 1
			break
		}
		goruntime.Gosched()
	}
	os.Exit(code)
}

func codexServerFixture() {
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(2)
		}
	}
	var output string
	for {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if dec.Decode(&msg) != nil {
			return
		}
		switch msg.Method {
		case "initialized":
			continue
		case "account/read":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"account": map[string]string{"type": "chatgpt"}}})
		case "model/list":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"data": []any{map[string]string{"model": "codex-fixture"}}, "nextCursor": nil}})
		case "thread/start":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"thread": map[string]string{"id": "thread1"}, "model": "codex-fixture", "instructionSources": []string{}}})
		case "thread/inject_items":
			var p struct {
				Items []struct {
					Type   string `json:"type"`
					Output string `json:"output"`
				} `json:"items"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			for _, item := range p.Items {
				if item.Type == "function_call_output" {
					output = item.Output
				}
			}
			send(map[string]any{"id": msg.ID, "result": map[string]any{}})
		case "turn/start":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"turn": map[string]string{"id": "turn1"}}})
			if output == "" {
				send(map[string]any{"id": "request1", "method": "item/tool/call", "params": map[string]any{"threadId": "thread1", "turnId": "turn1", "callId": "call1", "tool": "echo", "arguments": map[string]any{}}})
				continue
			}
			send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread1", "turnId": "turn1", "itemId": "message1", "delta": output}})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread1", "turn": map[string]string{"id": "turn1", "status": "completed"}}})
		default:
			send(map[string]any{"id": msg.ID, "result": map[string]any{}})
		}
	}
}

// TestCodexResponsesThroughTunnel verifies authenticated caller tool loops over real mTLS.
// TestCodexResponsesThroughTunnel 验证已鉴权调用方经真实 mTLS 隧道完成工具循环。
func TestCodexResponsesThroughTunnel(t *testing.T) {
	f := newFleetWithBackend(t, 1, runtime.KindCodex, codexcli.NewFactory(os.Args[0]))
	f.awaitReady(f.replicas...)
	api := httpapi.New(scheduler.New(f.replicas[0].server, scheduler.Config{}), httpapi.Config{APIKeys: []string{"test-key"}})
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()
	var wg sync.WaitGroup
	for _, stream := range []bool{false, true} {
		wg.Go(func() {
			input := []any{map[string]any{"role": "user", "content": "call echo then return the caller result"}}
			tools := []any{map[string]any{"type": "function", "name": "echo", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
			post := func(body any) []byte {
				data, _ := json.Marshal(body)
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/responses", bytes.NewReader(data))
				req.Header.Set("Authorization", "Bearer test-key")
				req.Header.Set("Content-Type", "application/json")
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Error(err)
					return nil
				}
				defer resp.Body.Close()
				data, _ = io.ReadAll(resp.Body)
				if resp.StatusCode != 200 {
					t.Errorf("status=%d body=%s, want 200", resp.StatusCode, data)
					return nil
				}
				return data
			}
			first := post(map[string]any{"model": "codex-fixture", "input": input, "tools": tools, "store": false})
			var obj struct {
				Output []json.RawMessage `json:"output"`
			}
			if json.Unmarshal(first, &obj) != nil || len(obj.Output) != 1 {
				t.Errorf("response=%s, want one tool item", first)
				return
			}
			var call struct {
				CallID string `json:"call_id"`
				Type   string `json:"type"`
			}
			_ = json.Unmarshal(obj.Output[0], &call)
			if call.Type != "function_call" || call.CallID != "call1" {
				t.Errorf("call=%+v, want correlated function call", call)
				return
			}
			token := fmt.Sprintf("caller-result-stream-%t", stream)
			input = append(input, obj.Output[0], map[string]any{"type": "function_call_output", "call_id": call.CallID, "output": token})
			second := post(map[string]any{"model": "codex-fixture", "input": input, "tools": tools, "store": false, "stream": stream})
			if !strings.Contains(string(second), token) {
				t.Errorf("response=%s, want caller token %s", second, token)
			}
			if stream && (!strings.Contains(string(second), "response.output_text.delta") || !strings.Contains(string(second), "response.completed")) {
				t.Errorf("response=%s, want delta and completed events", second)
			}
		})
	}
	wg.Wait()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"codex-fixture","input":"SECRET"}`))
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthorized status=%d, want 401", resp.StatusCode)
	}
}
