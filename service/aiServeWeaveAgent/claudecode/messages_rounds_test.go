package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

// delayedCallbackFixture leaves an earlier delivered result awaiting its MCP callback.
// delayedCallbackFixture 模拟 CLI 已继续生成，但前一轮结果仍待迟到的 MCP 回调取走。
func delayedCallbackFixture(s *messagesSession) {
	r, w := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer w.Close()
		for round := 0; round < 3; round++ {
			var output bytes.Buffer
			fixtureRound(&output, fmt.Sprintf("native-%d", round), "")
			_, _ = io.WriteString(w, strings.ReplaceAll(output.String(), "caller-only.txt", fmt.Sprintf("file-%d.txt", round)))
			for {
				s.mu.Lock()
				delivered := false
				for _, call := range s.pending {
					if call.delivered && strings.Contains(string(call.block.Input), fmt.Sprintf("file-%d.txt", round)) {
						delivered = true
					}
				}
				changed := s.changed
				s.mu.Unlock()
				if delivered {
					break
				}
				select {
				case <-changed:
				case <-s.ctx.Done():
					return
				}
			}
		}
		fixtureRound(w, "", "all rounds completed")
		_, _ = fmt.Fprintln(w, `{"type":"result","subtype":"success"}`)
	}()
	stop := context.AfterFunc(s.ctx, func() { _ = r.Close(); _ = w.Close() })
	defer stop()
	final, err := bridgeEvents(r, s)
	_ = r.Close()
	<-done
	_ = s.send(final, err)
}

// TestContinuationRoundKeepsOnlyCurrentTools reproduces the observed invalid_config path.
// TestContinuationRoundKeepsOnlyCurrentTools 覆盖连续工具回合中历史邮箱污染本轮数量校验。
func TestContinuationRoundKeepsOnlyCurrentTools(t *testing.T) {
	var logs bytes.Buffer
	rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{
		Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	defer r.Close()
	r.models = map[string]struct{}{"sonnet": {}}
	r.bridge.keys = map[string]principal{"first": {"tenant", "key"}}
	r.bridge.start = delayedCallbackFixture
	req := testMessagesRequest()
	req = continuation(t, req, sendMessages(r.bridge, req, "first"), "first output")
	req = continuation(t, req, sendMessages(r.bridge, req, "first"), "second output")
	data, _ := json.Marshal(req)
	stream, err := r.Messages(t.Context(), runtime.MessagesRequest{Model: "sonnet", TenantID: "tenant", KeyID: "key", JSON: data})
	if err != nil {
		t.Fatalf("valid second continuation rejected: %v; diagnostic: %s", err, &logs)
	}
	defer stream.Close()
	// Collect the native stream and continue through the HTTP boundary once more.
	// 收集同一原生流并再次跨 HTTP 边界续接，确认后续回合也能正常推进。
	var content []messageBlock
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var block struct {
			Type  string
			Block messageBlock `json:"content_block"`
			Delta struct {
				Partial string `json:"partial_json"`
			}
		}
		_ = json.Unmarshal(event.JSON, &block)
		if block.Type == "content_block_start" {
			block.Block.Input = nil
			content = append(content, block.Block)
		}
		if block.Type == "content_block_delta" {
			content[len(content)-1].Input = append(content[len(content)-1].Input, block.Delta.Partial...)
		}
	}
	response := httptest.NewRecorder()
	_ = json.NewEncoder(response).Encode(map[string]any{"content": content, "stop_reason": "tool_use"})
	// The first delayed callback must still receive its own earlier result.
	// 迟到的第一轮回调仍应取得第一轮结果，不能被本轮集合替换或误删。
	r.bridge.mu.Lock()
	var session *messagesSession
	for s := range r.bridge.sessions {
		session = s
	}
	r.bridge.mu.Unlock()
	late, err := session.callTool(t.Context(), "read_file", json.RawMessage(`{"path":"file-0.txt"}`))
	if err != nil || len(late.Content) != 1 || late.Content[0].Text != "first output" {
		t.Fatalf("late callback lost its result: %v/%+v", err, late)
	}
	req = continuation(t, req, response, "third output")
	if final := sendMessages(r.bridge, req, "first"); final.Code != 200 || !strings.Contains(final.Body.String(), "all rounds completed") {
		t.Fatalf("final status/body = %d/%s", final.Code, final.Body)
	}
}

func TestContinuationRequiresCompleteParallelResults(t *testing.T) {
	for _, beta := range []string{"", "new-feature"} {
		t.Run("beta="+beta, func(t *testing.T) {
			b := newMessagesBridge("unused", "sonnet", map[string]principal{"first": {"tenant", "key"}}, runtime.NewSystemClock())
			defer b.close()
			b.start = func(s *messagesSession) {
				r, w := io.Pipe()
				done := make(chan struct{})
				go func() {
					defer close(done)
					defer w.Close()
					emitFixture(w, map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]int{"input_tokens": 1}}})
					for i := 0; i < 2; i++ {
						emitFixture(w, map[string]any{"type": "content_block_start", "index": i, "content_block": messageBlock{Type: "tool_use", ID: fmt.Sprint(i), Name: "mcp__aisw_bridge__tool_0", Input: json.RawMessage(`{}`)}})
						emitFixture(w, map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]string{"type": "input_json_delta", "partial_json": fmt.Sprintf(`{"path":"file-%d"}`, i)}})
						emitFixture(w, map[string]any{"type": "content_block_stop", "index": i})
					}
					emitFixture(w, map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "tool_use"}})
					emitFixture(w, map[string]string{"type": "message_stop"})
					for i := 0; i < 2; i++ {
						if _, err := s.callTool(s.ctx, "read_file", json.RawMessage(fmt.Sprintf(`{"path":"file-%d"}`, i))); err != nil {
							return
						}
					}
					fixtureRound(w, "", "both results applied")
					_, _ = fmt.Fprintln(w, `{"type":"result","subtype":"success"}`)
				}()
				stop := context.AfterFunc(s.ctx, func() { _ = r.Close(); _ = w.Close() })
				defer stop()
				final, err := bridgeEvents(r, s)
				_ = r.Close()
				<-done
				_ = s.send(final, err)
			}
			req := testMessagesRequest()
			response := sendMessages(b, req, "first")
			var first struct{ Content []messageBlock }
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &first) != nil || len(first.Content) != 2 {
				t.Fatal("expected two parallel calls")
			}
			body, _ := json.Marshal(first.Content)
			req.Messages = append(req.Messages, bridgeMessage{Role: "assistant", Content: body})
			results := []messageBlock{
				{Type: "tool_result", ToolUseID: first.Content[0].ID, Content: json.RawMessage(`"output one"`)},
				{Type: "tool_result", ToolUseID: first.Content[1].ID, Content: json.RawMessage(`"output two"`)},
			}
			body, _ = json.Marshal(results[:1])
			req.Messages = append(req.Messages, bridgeMessage{Role: "user", Content: body})
			if partial := sendMessagesBeta(b, req, beta); partial.Code != 409 {
				t.Fatalf("partial results accepted: %d", partial.Code)
			}
			body, _ = json.Marshal(results)
			req.Messages[len(req.Messages)-1].Content = body
			if full := sendMessages(b, req, "first"); full.Code != 200 || !strings.Contains(full.Body.String(), "both results applied") {
				t.Fatalf("partial attempt consumed complete continuation: %d/%s", full.Code, full.Body)
			}
		})
	}
}

func sendMessagesBeta(b *messagesBridge, req messagesRequest, beta string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/messages", bytes.NewReader(data))
	r.Header.Set("X-Api-Key", "first")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Anthropic-Beta", beta)
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}

func TestContinuationEquivalentBetaHeaders(t *testing.T) {
	b := newMessagesBridge("unused", "sonnet", map[string]principal{"first": {"tenant", "key"}}, runtime.NewSystemClock())
	b.start = fixtureStart
	defer b.close()
	req := testMessagesRequest()
	req = continuation(t, req, sendMessagesBeta(b, req, "feature-a,feature-b"), "expected result")
	w := sendMessagesBeta(b, req, "feature-b, feature-a,feature-b")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "expected result") {
		t.Fatalf("equivalent beta headers broke continuation: status=%d, body=%s", w.Code, w.Body)
	}
}

func TestContinuationChangedBetaRestartsCLI(t *testing.T) {
	b := newMessagesBridge("unused", "sonnet", map[string]principal{"first": {"tenant", "key"}}, runtime.NewSystemClock())
	defer b.close()
	started := make(chan *messagesSession, 2)
	b.start = func(s *messagesSession) {
		started <- s
		last, _ := blocks(s.messages[len(s.messages)-1].Content)
		if len(last) == 0 || last[0].Type != "tool_result" {
			fixtureStart(s)
			return
		}
		prompt := sessionPrompt(s.messages)
		if !strings.HasPrefix(prompt, resumeInstruction) || !strings.Contains(prompt, "already executed result") {
			_ = s.send(nil, errCLIProtocol)
			return
		}
		var events bytes.Buffer
		fixtureRound(&events, "", "continued from existing result")
		_, _ = fmt.Fprintln(&events, `{"type":"result","subtype":"success"}`)
		final, err := bridgeEvents(&events, s)
		_ = s.send(final, err)
	}
	req := testMessagesRequest()
	req = continuation(t, req, sendMessagesBeta(b, req, "feature-a,feature-b"), "already executed result")
	first := <-started
	response := sendMessagesBeta(b, req, "feature-a")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "continued from existing result") {
		t.Fatalf("beta fallback broke continuation: status=%d, body=%s", response.Code, response.Body)
	}
	second := <-started
	if second == first || first.ctx.Err() == nil || second.request.betaFeatures != "feature-a" {
		t.Fatal("changed beta was silently reused on the old CLI")
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	for _, call := range first.pending {
		if call.delivered {
			t.Fatal("replacement delivered results to the old CLI")
		}
	}
}
