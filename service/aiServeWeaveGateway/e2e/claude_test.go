//go:build darwin || linux

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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
	var imageInput bool
	var documentInput, convertedInput, structuredInput bool
	var prompt []byte
	if slices.Contains(os.Args, "--input-format") {
		var request struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Message   struct {
				Role    string
				Content []struct {
					Type, Text string
					Source     struct {
						Type, Data string
						MediaType  string `json:"media_type"`
					}
				}
			}
		}
		if json.NewDecoder(os.Stdin).Decode(&request) != nil {
			os.Exit(3)
		}
		if request.Type == "user" {
			structuredInput = true
			if request.Message.Role != "user" {
				os.Exit(3)
			}
			for _, block := range request.Message.Content {
				if block.Type == "image" {
					if block.Source.MediaType == "image/jpeg" {
						data, err := base64.StdEncoding.DecodeString(block.Source.Data)
						if err != nil || !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
							os.Exit(3)
						}
						convertedInput = true
						continue
					}
					if block.Source.Type != "base64" || block.Source.Data != claudeFixtureImage || block.Source.MediaType != "image/png" {
						os.Exit(3)
					}
					imageInput = true
				} else if block.Type == "document" {
					data, err := base64.StdEncoding.DecodeString(block.Source.Data)
					if err != nil || block.Source.MediaType != "application/pdf" || !bytes.HasPrefix(data, []byte("%PDF-")) {
						os.Exit(3)
					}
					documentInput = true
				} else if block.Type == "text" {
					if strings.Contains(block.Text, claudeFixtureImage) {
						os.Exit(3)
					}
					prompt = append(prompt, block.Text...)
				}
			}
			if !imageInput && !documentInput && !convertedInput {
				os.Exit(3)
			}
		} else {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": request.RequestID, "response": map[string]any{"models": []any{
					map[string]string{"value": "sonnet", "resolvedModel": "claude-sonnet-4-6", "displayName": "Sonnet 4.6"},
					map[string]string{"value": "opus", "resolvedModel": "fixture-opus", "display_name": "Opus fixture"},
					map[string]string{"value": "default", "resolvedModel": "fixture-opus", "displayName": "Default"},
				}},
			}})
			return
		}
	}
	var path string
	var model string
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--mcp-config" {
			path = os.Args[i+1]
		}
		if os.Args[i] == "--model" {
			model = os.Args[i+1]
		}
	}
	if !structuredInput {
		prompt, _ = io.ReadAll(os.Stdin)
	}
	var extra map[string]json.RawMessage
	if json.Unmarshal(claudeFixtureExtraBody(), &extra) == nil && len(extra["safeguards"]) > 0 {
		claudeSafeguardsProcessFixture(prompt, extra)
		return
	}
	wantModel := "claude-sonnet-4-6"
	if strings.HasSuffix(string(prompt), "opus") {
		wantModel = "fixture-opus"
	}
	if model != wantModel {
		os.Exit(3)
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
	toolPath := "caller-only.txt"
	round := func(tool bool, text string) {
		emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
		block := map[string]any{"type": "text", "text": ""}
		delta := map[string]any{"type": "text_delta", "text": text}
		stop := "end_turn"
		if tool {
			block = map[string]any{"type": "tool_use", "id": "native-fixture-id", "name": "mcp__aisw_bridge__tool_0", "input": map[string]any{}}
			input, _ := json.Marshal(map[string]string{"path": toolPath})
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
			stop = "tool_use"
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
		emit(map[string]any{"type": "content_block_stop", "index": 0})
		emit(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 7}})
		emit(map[string]string{"type": "message_stop"})
	}
	round(true, "")
	if strings.Contains(string(prompt), "multiple-tool-rounds") {
		// The CLI continues while the first MCP callback is delayed. The old
		// delivered mailbox must not count towards the second round's tool set.
		// 第一轮 MCP 回调迟到，但 CLI 已继续；旧邮箱不能污染第二轮的结果集合。
		toolPath = "next-only.txt"
		round(true, "")
		call := func(path string) string {
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": path, "method": "tools/call", "params": map[string]any{"name": "tool_0", "arguments": map[string]string{"path": path}}})
			req, _ := http.NewRequest(http.MethodPost, mcp.URL, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", mcp.Headers["Authorization"])
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				os.Exit(3)
			}
			defer response.Body.Close()
			var result struct {
				Result struct{ Content []struct{ Text string } }
			}
			if json.NewDecoder(response.Body).Decode(&result) != nil || len(result.Result.Content) != 1 {
				os.Exit(3)
			}
			return result.Result.Content[0].Text
		}
		second := call("next-only.txt")
		first := call("caller-only.txt")
		round(false, first+"|"+second)
		fmt.Println(`{"type":"result","subtype":"success"}`)
		return
	}
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
		Result struct {
			Content []struct {
				Type, Text, Data string
				MIMEType         string `json:"mimeType"`
			}
		}
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || len(result.Result.Content) == 0 {
		os.Exit(3)
	}
	if (imageInput || strings.Contains(string(prompt), "tool-image")) && (len(result.Result.Content) != 2 || result.Result.Content[1].Type != "image" || result.Result.Content[1].Data != claudeFixtureImage || result.Result.Content[1].MIMEType != "image/png") {
		os.Exit(3)
	}
	if documentInput || convertedInput {
		images := 0
		var texts string
		for _, part := range result.Result.Content {
			if part.Type == "image" && part.MIMEType == "image/jpeg" {
				images++
			}
			texts += part.Text
		}
		if images == 0 || (documentInput && !strings.Contains(texts, "extracted table marker")) || (convertedInput && (!strings.Contains(texts, "spoken marker") || !strings.Contains(string(prompt), "spoken marker"))) {
			os.Exit(3)
		}
	}
	round(false, result.Result.Content[0].Text)
	fmt.Println(`{"type":"result","subtype":"success"}`)
}

// bridgeOnly withholds anthropic_messages_native, the operator override that
// keeps a node's unguarded requests on the compatibility bridge these tests
// exercise.
//
// bridgeOnly 撤销 anthropic_messages_native，即让节点不带 safeguards 的请求继续走本组
// 测试所覆盖的兼容桥接的运维覆盖项。
func bridgeOnly(factory runtime.Factory) runtime.Factory {
	return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		cfg.CapabilityOverrides = map[runtime.Capability]runtime.SupportLevel{runtime.CapabilityMessagesNative: runtime.SupportUnsupported}
		return factory(cfg, deps)
	}
}

// TestClaudeLongToolContinuation keeps per-round ID sets across real mTLS tunnels and MCP callbacks.
// TestClaudeLongToolContinuation 经真实 mTLS 隧道与 MCP 回调验证连续工具回合的独立集合。
func TestClaudeLongToolContinuation(t *testing.T) {
	f := newFleetWithBackend(t, 2, runtime.KindClaude, bridgeOnly(claudecode.NewFactory(os.Args[0])))
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
	messages := []any{map[string]string{"role": "user", "content": "multiple-tool-rounds claude-sonnet-4-6"}}
	tools := []any{map[string]any{"name": "read_file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}}}}
	for round := 0; round < 3; round++ {
		body, _ := json.Marshal(map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "messages": messages, "tools": tools})
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, servers[round%2].URL+"/v1/messages", bytes.NewReader(body))
		req.Header.Set("X-Api-Key", "claude-live-first")
		req.Header.Set("Content-Type", "application/json")
		beta := []string{"feature-a,feature-b", "feature-b, feature-a", "feature-a,feature-b,feature-a"}[round]
		req.Header.Set("Anthropic-Beta", beta)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("round %d: HTTP %d: %s", round, response.StatusCode, data)
		}
		var result struct {
			Content []struct {
				Type, ID, Text string
				Input          map[string]string
			}
			StopReason string `json:"stop_reason"`
		}
		if json.Unmarshal(data, &result) != nil || len(result.Content) != 1 {
			t.Fatalf("round %d: invalid response %s", round, data)
		}
		if round == 2 {
			if result.StopReason != "end_turn" || result.Content[0].Text != "first caller result|second caller result" {
				t.Fatalf("lost/mixed callback results: %s", data)
			}
			break
		}
		if result.StopReason != "tool_use" || result.Content[0].Type != "tool_use" || result.Content[0].Input["path"] != []string{"caller-only.txt", "next-only.txt"}[round] {
			t.Fatalf("round %d: invalid tool round %s", round, data)
		}
		var content json.RawMessage
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(data, &raw)
		content = raw["content"]
		messages = append(messages, map[string]any{"role": "assistant", "content": content})
		output := []string{"first caller result", "second caller result"}[round]
		messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]string{"type": "tool_result", "tool_use_id": result.Content[0].ID, "content": output}}})
	}
}

const claudeFixtureImage = "aW1hZ2UtYnl0ZXM="

func claudeImageBlock() map[string]any {
	return map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/png", "data": claudeFixtureImage}}
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
	f := newFleetWithBackend(t, 2, runtime.KindClaude, bridgeOnly(claudecode.NewFactoryWithAttachments(os.Args[0], attachmentFixtureProcessor(t))))
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
	var catalog struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
		}
	}
	err = json.NewDecoder(models.Body).Decode(&catalog)
	_ = models.Body.Close()
	var ids []string
	for _, model := range catalog.Data {
		ids = append(ids, model.ID)
	}
	if err != nil || models.StatusCode != 200 || !slices.Equal(ids, []string{"claude-sonnet-4-6", "fixture-opus"}) {
		t.Fatalf("model catalog=%v, status=%d, error=%v; want [claude-sonnet-4-6 fixture-opus], 200, nil", ids, models.StatusCode, err)
	}
	for i, want := range ids {
		if got := catalog.Data[i]; got.Name != want || got.DisplayName != want {
			t.Fatalf("model=%+v, want name and display_name=%q", got, want)
		}
	}
	for _, tc := range []struct {
		name   string
		model  string
		stream bool
		path   string
		image  string
	}{
		{"JSON sonnet", "claude-sonnet-4-6", false, "/v1/messages", ""},
		{"SSE opus", "fixture-opus", true, "/v1/messages", ""},
		{"unified base JSON", "claude-sonnet-4-6", false, "/v1/v1/messages", ""},
		{"unified base SSE", "fixture-opus", true, "/v1/v1/messages", ""},
		{"image JSON", "claude-sonnet-4-6", false, "/v1/messages", "single"},
		{"image SSE", "claude-sonnet-4-6", true, "/v1/messages", "single"},
		{"image history", "claude-sonnet-4-6", false, "/v1/messages", "history"},
		{"tool image only", "claude-sonnet-4-6", false, "/v1/messages", "tool only"},
		{"tool image history", "claude-sonnet-4-6", false, "/v1/messages", "tool history"},
		{"PDF JSON", "claude-sonnet-4-6", false, "/v1/messages", "pdf"},
		{"PDF SSE", "claude-sonnet-4-6", true, "/v1/messages", "pdf"},
		{"PDF history", "claude-sonnet-4-6", false, "/v1/messages", "pdf history"},
		{"Office JSON", "claude-sonnet-4-6", false, "/v1/messages", "office"},
		{"video speech SSE", "claude-sonnet-4-6", true, "/v1/messages", "video"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []any{map[string]any{"role": "user", "content": "use caller tool " + tc.model}}
			if tc.image != "" {
				messages[0] = map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "use caller tool " + tc.model}, claudeImageBlock()}}
				if tc.image == "history" {
					messages = append(messages, map[string]string{"role": "assistant", "content": "I saw the image."}, map[string]string{"role": "user", "content": "Continue using the caller tool."})
				}
				if tc.image == "tool only" {
					messages = []any{map[string]string{"role": "user", "content": "use caller tool-image " + tc.model}}
				}
				if tc.image == "tool history" {
					messages = []any{
						map[string]string{"role": "user", "content": "read an earlier image"},
						map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "earlier", "name": "read_file", "input": map[string]string{"path": "earlier.png"}}}},
						map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "earlier", "content": []any{claudeImageBlock()}}}},
						map[string]string{"role": "assistant", "content": "I saw the earlier image."},
						map[string]string{"role": "user", "content": "Continue using the caller tool."},
					}
				}
			}
			tools := []any{map[string]any{"name": "read_file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}}}}
			var attachmentBlock map[string]any
			if tc.image == "pdf" || tc.image == "pdf history" || tc.image == "office" || tc.image == "video" {
				kind, mime, data := "document", "application/pdf", "%PDF-1.4 attachment fixture"
				if tc.image == "office" {
					mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
					data = "office fixture"
				}
				if tc.image == "video" {
					kind, mime, data = "video", "video/mp4", "video fixture"
				}
				attachmentBlock = map[string]any{"type": kind, "title": "fixture", "source": map[string]string{"type": "base64", "media_type": mime, "data": base64.StdEncoding.EncodeToString([]byte(data))}}
				messages = []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "use caller tool"}, attachmentBlock}}}
				if tc.image == "pdf history" {
					messages = append(messages, map[string]string{"role": "assistant", "content": "I read the PDF."}, map[string]string{"role": "user", "content": "Continue with the caller tool."})
				}
			}
			post := func(index int, stream bool) []byte {
				body, _ := json.Marshal(map[string]any{"model": tc.model, "max_tokens": 1024, "messages": messages, "tools": tools, "stream": stream})
				path := tc.path
				if index == 1 {
					path = "/v1/messages"
				}
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, servers[index].URL+path, bytes.NewReader(body))
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
			firstStream := tc.stream && tc.image != ""
			first := post(0, firstStream)
			var result struct {
				Content    []map[string]any
				StopReason string `json:"stop_reason"`
			}
			if firstStream {
				var arguments string
				for _, line := range bytes.Split(first, []byte("\n")) {
					if !bytes.HasPrefix(line, []byte("data: ")) {
						continue
					}
					var event struct {
						Type  string
						Block map[string]any `json:"content_block"`
						Delta struct {
							PartialJSON string `json:"partial_json"`
							StopReason  string `json:"stop_reason"`
						}
					}
					if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &event); err != nil {
						t.Fatal(err)
					}
					switch event.Type {
					case "content_block_start":
						result.Content = append(result.Content, event.Block)
					case "content_block_delta":
						arguments += event.Delta.PartialJSON
					case "message_delta":
						result.StopReason = event.Delta.StopReason
					}
				}
				if len(result.Content) != 1 {
					t.Fatal("stream must request one caller tool")
				}
				var input map[string]any
				if err := json.Unmarshal([]byte(arguments), &input); err != nil {
					t.Fatal(err)
				}
				result.Content[0]["input"] = input
			} else if err := json.Unmarshal(first, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Content) != 1 || result.StopReason != "tool_use" {
				t.Fatal("first round must request one caller tool")
			}
			id, _ := result.Content[0]["id"].(string)
			if !strings.HasPrefix(id, "toolu_aisw_") {
				t.Fatal("tool ID must identify owned native continuation")
			}
			var toolContent any = "unique-caller-result"
			if tc.image != "" {
				toolContent = []any{map[string]string{"type": "text", "text": "unique-caller-result"}, claudeImageBlock()}
			}
			if attachmentBlock != nil {
				toolContent = []any{map[string]string{"type": "text", "text": "unique-caller-result"}, attachmentBlock}
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": result.Content}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": toolContent}}})
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
