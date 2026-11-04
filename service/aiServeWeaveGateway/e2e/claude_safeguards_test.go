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
	"slices"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

func claudeFixtureExtraBody() []byte {
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] != "--settings" {
			continue
		}
		data, err := os.ReadFile(os.Args[i+1])
		if err != nil {
			break
		}
		var settings struct {
			Env map[string]string `json:"env"`
		}
		if json.Unmarshal(data, &settings) == nil && settings.Env["CLAUDE_CODE_EXTRA_BODY"] != "" {
			return []byte(settings.Env["CLAUDE_CODE_EXTRA_BODY"])
		}
	}
	return []byte(os.Getenv("CLAUDE_CODE_EXTRA_BODY"))
}

func claudeSafeguardsProcessFixture(_ []byte, extra map[string]json.RawMessage) {
	prompt := []byte(extra["messages"])
	var betas []string
	var guards []struct {
		Type    string `json:"type"`
		Context struct {
			Round   int    `json:"fixture_round"`
			Outcome string `json:"fixture_outcome"`
		} `json:"classifier_context"`
	}
	if json.Unmarshal(extra["betas"], &betas) != nil || !slices.Contains(betas, "future-feature-2099-01-01") || json.Unmarshal(extra["safeguards"], &guards) != nil || len(guards) != 1 || guards[0].Type != "dangerous_tool_use" || !strings.Contains(os.Getenv("ANTHROPIC_CUSTOM_HEADERS"), "anthropic-version: 2023-06-01") {
		os.Exit(3)
	}
	emit := func(event any) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "stream_event", "event": event})
	}
	emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_guarded", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
	stop := "end_turn"
	calls := map[string]any{}
	if guards[0].Context.Round == 1 {
		var tools []struct{ Name string }
		if json.Unmarshal(extra["tools"], &tools) != nil || len(tools) != 1 || tools[0].Name != "read_file" {
			os.Exit(3)
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "guarded-upstream-id", "name": "read_file", "input": map[string]any{}, "caller": map[string]string{"type": "direct"}}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":"caller-only.txt"}`}})
		stop = "tool_use"
		calls["guarded-upstream-id"] = map[string]any{"type": "evaluated", "outcome": guards[0].Context.Outcome, "opaque": "upstream-verdict"}
	} else {
		text := "caller-safe-result"
		if guards[0].Context.Outcome == "flagged" {
			text = "caller-denied"
		}
		if guards[0].Context.Round != 2 || !bytes.Contains(prompt, []byte(text)) {
			os.Exit(3)
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": text}})
		for _, cited := range []string{"first-citation", "second-citation"} {
			emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "citations_delta", "citation": map[string]any{"type": "char_location", "cited_text": cited, "document_index": 0, "start_char_index": 0, "end_char_index": 1}}})
		}
	}
	emit(map[string]any{"type": "content_block_stop", "index": 0})
	emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "future_delta": true, "safeguard_results": []any{map[string]any{"type": "dangerous_tool_use", "status": map[string]any{"type": "available", "tool_uses": calls}}}}, "context_management": map[string]any{"applied_edits": []any{}}, "usage": map[string]int{"output_tokens": 7}})
	emit(map[string]string{"type": "message_stop"})
	if stop == "tool_use" {
		time.Sleep(30 * time.Second)
		return
	}
	fmt.Println(`{"type":"result","subtype":"success"}`)
}

func TestClaudeSafeguardsThroughTunnel(t *testing.T) {
	f := newFleetWithBackend(t, 2, runtime.KindClaude, claudecode.NewFactory(os.Args[0]))
	f.awaitReady(f.replicas...)

	var servers []*httptest.Server
	for _, replica := range f.replicas {
		api := httpapi.New(scheduler.New(replica.server, scheduler.Config{}), httpapi.Config{Verifier: claudeLiveVerifier{}})
		t.Cleanup(api.Close)
		server := httptest.NewServer(api)
		t.Cleanup(server.Close)
		servers = append(servers, server)
	}
	for _, tc := range []struct {
		name, outcome string
		stream        bool
	}{
		{"JSON allow", "not_flagged", false}, {"SSE deny", "flagged", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []any{map[string]any{"role": "user", "content": "request caller tool with server safeguards"}}
			post := func(replica, round int, key string) (int, []byte) {
				body, _ := json.Marshal(map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "stream": tc.stream, "messages": messages, "tools": []any{map[string]any{"name": "read_file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}}}}, "safeguards": []any{map[string]any{"type": "dangerous_tool_use", "classifier_context": map[string]any{"v": 1, "fixture_round": round, "fixture_outcome": tc.outcome}}}})
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, servers[replica].URL+"/v1/messages?beta=true", bytes.NewReader(body))
				req.Header.Set("X-Api-Key", key)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Anthropic-Version", "2023-06-01")
				req.Header.Set("Anthropic-Beta", "dangerous-tool-use-2026-09-03, future-feature-2099-01-01")
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
				return response.StatusCode, data
			}
			status, first := post(0, 1, "claude-live-first")
			if status != 200 {
				t.Fatalf("status/body=%d/%s, want 200 and reviewed tool", status, first)
			}
			var response struct {
				Future  bool             `json:"future_delta"`
				Context json.RawMessage  `json:"context_management"`
				Content []map[string]any `json:"content"`
				Results []struct {
					Status struct {
						Calls map[string]map[string]any `json:"tool_uses"`
					} `json:"status"`
				} `json:"safeguard_results"`
			}
			if tc.stream {
				for _, line := range strings.Split(string(first), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event struct {
						Type  string
						Block map[string]any `json:"content_block"`
						Delta json.RawMessage
					}
					_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event)
					if event.Type == "content_block_start" {
						event.Block["input"] = map[string]string{"path": "caller-only.txt"}
						response.Content = append(response.Content, event.Block)
					}
					if event.Type == "message_delta" {
						_ = json.Unmarshal(event.Delta, &response)
						if bytes.Contains(first, []byte(`"context_management"`)) {
							response.Context = json.RawMessage(`{}`)
						}
					}
				}
			} else if err := json.Unmarshal(first, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Content) != 1 || len(response.Results) != 1 {
				t.Fatalf("missing tool or safeguard verdict: %s", first)
			}
			if !response.Future || len(response.Context) == 0 || response.Content[0]["caller"] == nil {
				t.Fatalf("native response extensions lost: %s", first)
			}
			id, _ := response.Content[0]["id"].(string)
			if id != "guarded-upstream-id" || response.Results[0].Status.Calls[id]["outcome"] != tc.outcome || response.Results[0].Status.Calls[id]["opaque"] != "upstream-verdict" {
				t.Fatalf("wrong tool/verdict association: id=%s response=%s", id, first)
			}
			result := "caller-safe-result"
			if tc.outcome == "flagged" {
				result = "caller-denied"
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": response.Content}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": result, "is_error": tc.outcome == "flagged"}}})
			if status, body := post(1, 2, "claude-live-second"); status != 200 || !bytes.Contains(body, []byte(result)) {
				t.Fatalf("authenticated stateless request status=%d body=%s", status, body)
			}
			if status, final := post(1, 2, "claude-live-first"); status != 200 || !bytes.Contains(final, []byte(result)) || !bytes.Contains(final, []byte("safeguard_results")) {
				t.Fatalf("fresh-context continuation status/body=%d/%s", status, final)
			} else if tc.stream {
				if !bytes.Contains(final, []byte(`"type":"citations_delta"`)) || !bytes.Contains(final, []byte("second-citation")) {
					t.Fatalf("native citation stream lost: %s", final)
				}
			} else {
				var cited struct {
					Content []struct {
						Citations []struct {
							Text string `json:"cited_text"`
						}
					}
				}
				if json.Unmarshal(final, &cited) != nil || len(cited.Content) != 1 || len(cited.Content[0].Citations) != 2 || cited.Content[0].Citations[1].Text != "second-citation" {
					t.Fatalf("native JSON citations lost: %s", final)
				}
			}
			if status, body := post(0, 2, "claude-live-first"); status != 200 || !bytes.Contains(body, []byte(result)) {
				t.Fatalf("stateless retry status=%d body=%s", status, body)
			}
		})
	}
}
