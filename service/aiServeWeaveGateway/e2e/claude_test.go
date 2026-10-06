//go:build darwin || linux

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/messagesession"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

func claudeProcessFixture() {
	if os.Args[1] == "auth" {
		fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai"}`)
		return
	}
	var path string
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--mcp-config" {
			path = os.Args[i+1]
		}
		if os.Args[i] == "--model" && os.Args[i+1] != "claude-sonnet-4-6" {
			os.Exit(3)
		}
	}
	data, _ := os.ReadFile(path)
	var config struct {
		Servers map[string]struct {
			URL     string
			Headers map[string]string
		} `json:"mcpServers"`
	}
	if json.Unmarshal(data, &config) != nil {
		os.Exit(3)
	}
	mcp := config.Servers["aisw_bridge"]
	emit := func(event any) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "stream_event", "event": event})
	}
	round := func(tool bool, text string) {
		emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
		block := map[string]any{"type": "text", "text": ""}
		delta := map[string]any{"type": "text_delta", "text": text}
		stop := "end_turn"
		if tool {
			block = map[string]any{"type": "tool_use", "id": "native-fixture-id", "name": "mcp__aisw_bridge__tool_0", "input": map[string]any{}}
			delta = map[string]any{"type": "input_json_delta", "partial_json": `{"path":"caller-only.txt"}`}
			stop = "tool_use"
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
		emit(map[string]any{"type": "content_block_stop", "index": 0})
		emit(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 7}})
		emit(map[string]string{"type": "message_stop"})
	}
	round(true, "")
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tool_0","arguments":{"path":"caller-only.txt"}}}`
	req, _ := http.NewRequest(http.MethodPost, mcp.URL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", mcp.Headers["Authorization"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		os.Exit(3)
	}
	defer resp.Body.Close()
	var result struct {
		Result struct{ Content []struct{ Text string } }
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || len(result.Result.Content) != 1 {
		os.Exit(3)
	}
	round(false, result.Result.Content[0].Text)
	fmt.Println(`{"type":"result","subtype":"success"}`)
}

// fixtureMessagesStore is shared only by offline tests, never by production Gateway wiring.
// fixtureMessagesStore 仅供离线测试共享，生产 Gateway 从不使用。
type fixtureMessagesStore struct {
	mu     sync.Mutex
	rounds map[string]messagesession.Record
}

func (s *fixtureMessagesStore) Publish(_ context.Context, record messagesession.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range record.IDs {
		if _, ok := s.rounds[id]; ok {
			return messagesession.ErrUnavailable
		}
	}
	for _, id := range record.IDs {
		s.rounds[id] = record
	}
	return nil
}

func (s *fixtureMessagesStore) Claim(_ context.Context, ids []string, tenant, key, model string) (messagesession.Record, func(context.Context, bool) error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) != 1 {
		return messagesession.Record{}, nil, messagesession.ErrUnavailable
	}
	record, ok := s.rounds[ids[0]]
	if !ok || record.TenantID != tenant || record.KeyID != key || record.Model != model {
		return messagesession.Record{}, nil, messagesession.ErrUnavailable
	}
	delete(s.rounds, ids[0])
	return record, func(_ context.Context, commit bool) error {
		if !commit {
			s.mu.Lock()
			s.rounds[ids[0]] = record
			s.mu.Unlock()
		}
		return nil
	}, nil
}

// TestClaudeMessagesThroughTunnel verifies native tools across two authenticated mTLS replicas offline.
// TestClaudeMessagesThroughTunnel 离线验证原生工具跨两个已认证 mTLS 副本续接。
func TestClaudeMessagesThroughTunnel(t *testing.T) {
	f := newFleetWithBackend(t, 2, runtime.KindClaude, claudecode.NewFactory(os.Args[0]))
	f.awaitReady(f.replicas...)
	store := &fixtureMessagesStore{rounds: make(map[string]messagesession.Record)}
	var servers []*httptest.Server
	for _, replica := range f.replicas {
		api := httpapi.New(scheduler.New(replica.server, scheduler.Config{}), httpapi.Config{Verifier: claudeLiveVerifier{}, MessagesSessions: store})
		t.Cleanup(api.Close)
		server := httptest.NewServer(api)
		t.Cleanup(server.Close)
		servers = append(servers, server)
	}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, servers[0].URL+"/v1/models", nil)
	request.Header.Set("Authorization", "Bearer claude-live-first")
	models, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct{ Data []struct{ ID string } }
	err = json.NewDecoder(models.Body).Decode(&catalog)
	_ = models.Body.Close()
	if err != nil || models.StatusCode != 200 || len(catalog.Data) != 1 || catalog.Data[0].ID != "sonnet" {
		t.Fatal("model catalog must publish the native sonnet runtime")
	}
	for _, tc := range []struct {
		name   string
		stream bool
	}{{"JSON", false}, {"SSE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []any{map[string]any{"role": "user", "content": "use caller tool"}}
			tools := []any{map[string]any{"name": "read_file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}}}}
			post := func(index int, stream bool) []byte {
				body, _ := json.Marshal(map[string]any{"model": "sonnet", "max_tokens": 1024, "messages": messages, "tools": tools, "stream": stream})
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, servers[index].URL+"/v1/messages", bytes.NewReader(body))
				req.Header.Set("X-Api-Key", "claude-live-first")
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				if resp.StatusCode != 200 {
					t.Fatalf("status=%d,want 200", resp.StatusCode)
				}
				return data
			}
			first := post(0, false)
			var result struct {
				Content    []map[string]any
				StopReason string `json:"stop_reason"`
			}
			if json.Unmarshal(first, &result) != nil || len(result.Content) != 1 || result.StopReason != "tool_use" {
				t.Fatal("first round must request one caller tool")
			}
			id, _ := result.Content[0]["id"].(string)
			if !strings.HasPrefix(id, "toolu_aisw_") {
				t.Fatal("tool ID must identify owned native continuation")
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": result.Content}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "unique-caller-result"}}})
			final := post(1, tc.stream)
			if !bytes.Contains(final, []byte("unique-caller-result")) {
				t.Fatal("final text did not preserve caller result")
			}
			if tc.stream && bytes.Count(final, []byte("event: message_stop")) != 1 {
				t.Fatal("SSE must contain exactly one final stop")
			}
		})
	}
}
