package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func testMessagesRequest() messagesRequest {
	return messagesRequest{Model: "sonnet", MaxTokens: 1024, Messages: []bridgeMessage{{Role: "user", Content: json.RawMessage(`"use the caller tool"`)}}, Tools: []bridgeTool{{Name: "read_file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}}}
}

func sendMessages(b *messagesBridge, req messagesRequest, key string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/messages", strings.NewReader(string(data)))
	r.Header.Set("X-Api-Key", key)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}

func emitFixture(w io.Writer, event any) {
	data, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "{\"type\":\"stream_event\",\"event\":%s}\n", data)
}

func fixtureRound(w io.Writer, toolID, text string) {
	emitFixture(w, map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
	block := messageBlock{Type: "text", Text: ""}
	if toolID != "" {
		block = messageBlock{Type: "tool_use", ID: toolID, Name: "mcp__aisw_bridge__tool_0", Input: json.RawMessage(`{}`)}
	}
	emitFixture(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
	delta := map[string]string{"type": "text_delta", "text": text}
	if toolID != "" {
		delta = map[string]string{"type": "input_json_delta", "partial_json": `{"path":"caller-only.txt"}`}
	}
	emitFixture(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
	emitFixture(w, map[string]any{"type": "content_block_stop", "index": 0})
	stop := "end_turn"
	if toolID != "" {
		stop = "tool_use"
	}
	emitFixture(w, map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 7}})
	emitFixture(w, map[string]string{"type": "message_stop"})
}

func fixtureStart(s *messagesSession) {
	r, w := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer w.Close()
		fixtureRound(w, randomID(), "")
		result, err := s.callTool(s.ctx, "read_file", json.RawMessage(`{"path":"caller-only.txt"}`))
		if err != nil {
			return
		}
		fixtureRound(w, "", result.Text)
		_, _ = fmt.Fprintln(w, `{"type":"result","subtype":"success"}`)
	}()
	stop := context.AfterFunc(s.ctx, func() { _ = r.Close(); _ = w.Close() })
	defer stop()
	final, err := bridgeEvents(r, s)
	_ = r.Close()
	<-writerDone
	if err != nil {
		_ = s.send(nil, err)
		return
	}
	_ = s.send(final, nil)
}

func continuation(t *testing.T, req messagesRequest, w *httptest.ResponseRecorder, result string) messagesRequest {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; fixed error = %s", w.Code, w.Body)
	}
	var response struct {
		Content    []messageBlock `json:"content"`
		StopReason string         `json:"stop_reason"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("invalid tool round JSON")
	}
	var calls []messageBlock
	for _, block := range response.Content {
		if block.Type == "tool_use" {
			calls = append(calls, block)
		}
	}
	if len(calls) != 1 || calls[0].Name != "read_file" || response.StopReason != "tool_use" {
		t.Fatalf("tool round shape invalid; want one read_file tool_use and tool_use stop")
	}
	body, _ := json.Marshal(response.Content)
	req.Messages = append(req.Messages, bridgeMessage{Role: "assistant", Content: body})
	text, _ := json.Marshal(result)
	body, _ = json.Marshal([]messageBlock{{Type: "tool_result", ToolUseID: calls[0].ID, Content: text}})
	req.Messages = append(req.Messages, bridgeMessage{Role: "user", Content: body})
	return req
}

// TestMessagesRoundTrip verifies a real HTTP boundary between tool request and result.
// TestMessagesRoundTrip 验证工具请求与结果之间存在真实 HTTP 请求边界。
func TestMessagesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{{"JSON", false}, {"SSE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			b := newMessagesBridge("unused", "sonnet", map[string]principal{"first": {"t1", "k1"}}, runtime.NewSystemClock())
			b.start = fixtureStart
			defer b.close()
			req := testMessagesRequest()
			req = continuation(t, req, sendMessages(b, req, "first"), "unique-caller-result")
			req.Stream = tc.stream
			w := sendMessages(b, req, "first")
			if w.Code != 200 || !strings.Contains(w.Body.String(), "unique-caller-result") {
				t.Fatalf("status/content = %d/%t, want 200/true", w.Code, strings.Contains(w.Body.String(), "unique-caller-result"))
			}
			if tc.stream && strings.Count(w.Body.String(), "event: message_stop") != 1 {
				t.Fatalf("message_stop count = %d, want 1", strings.Count(w.Body.String(), "event: message_stop"))
			}
		})
	}
}

// TestMessagesContinuationGuards keeps invalid continuations from consuming results.
// TestMessagesContinuationGuards 保证非法续接不会消费待决工具结果。
func TestMessagesContinuationGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*messagesRequest)
		key    string
	}{
		{"other identity", func(*messagesRequest) {}, "second"},
		{"changed history", func(r *messagesRequest) { r.Messages[0].Content = json.RawMessage(`"changed"`) }, "first"},
		{"changed tools", func(r *messagesRequest) { r.Tools[0].Description = "changed" }, "first"},
		{"changed model", func(r *messagesRequest) { r.Model = "opus" }, "first"},
		{"unknown ID", func(r *messagesRequest) {
			r.Messages[2].Content = json.RawMessage(`[{"type":"tool_result","tool_use_id":"other","content":"no"}]`)
		}, "first"},
		{"duplicate result", func(r *messagesRequest) {
			var list []messageBlock
			_ = json.Unmarshal(r.Messages[2].Content, &list)
			list = append(list, list[0])
			r.Messages[2].Content, _ = json.Marshal(list)
		}, "first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newMessagesBridge("unused", "sonnet", map[string]principal{"first": {"t1", "k1"}, "second": {"t2", "k2"}}, runtime.NewSystemClock())
			b.start = fixtureStart
			defer b.close()
			req := testMessagesRequest()
			req = continuation(t, req, sendMessages(b, req, "first"), "expected")
			data, _ := json.Marshal(req)
			var invalid messagesRequest
			_ = json.Unmarshal(data, &invalid)
			tc.mutate(&invalid)
			if w := sendMessages(b, invalid, tc.key); w.Code != 400 && w.Code != 409 {
				t.Fatalf("invalid status = %d, want 400 or 409", w.Code)
			}
			if w := sendMessages(b, req, "first"); w.Code != 200 || !strings.Contains(w.Body.String(), "expected") {
				t.Fatalf("valid status/result = %d/%t, want 200/true", w.Code, strings.Contains(w.Body.String(), "expected"))
			}
			if w := sendMessages(b, req, "first"); w.Code != 409 {
				t.Fatalf("replay status = %d, want 409", w.Code)
			}
		})
	}
}

// TestMessagesUsers verifies that identical tools do not mix callers' results.
// TestMessagesUsers 验证相同工具不会混用不同调用方的结果。
func TestMessagesUsers(t *testing.T) {
	b := newMessagesBridge("unused", "sonnet", map[string]principal{"first": {"t1", "k1"}, "second": {"t2", "k2"}}, runtime.NewSystemClock())
	b.start = fixtureStart
	defer b.close()
	requests := make([]messagesRequest, 2)
	keys := []string{"first", "second"}
	for i, key := range keys {
		req := testMessagesRequest()
		requests[i] = continuation(t, req, sendMessages(b, req, key), "result-"+key)
	}
	if w := sendMessages(b, testMessagesRequest(), "first"); w.Code != 409 {
		t.Fatalf("capacity status = %d, want 409", w.Code)
	}
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := sendMessages(b, requests[i], key)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "result-"+key) || strings.Contains(w.Body.String(), "result-"+keys[1-i]) {
				t.Errorf("identity %d status/correct/cross = %d/%t/%t, want 200/true/false", i, w.Code, strings.Contains(w.Body.String(), "result-"+key), strings.Contains(w.Body.String(), "result-"+keys[1-i]))
			}
		}()
	}
	wg.Wait()
}

// TestMessagesStrictInput rejects unsupported extensions rather than dropping them.
// TestMessagesStrictInput 拒绝不支持的扩展，避免静默丢弃。
func TestMessagesStrictInput(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"thinking", `{"model":"sonnet","max_tokens":1024,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"adaptive"}}`, 400},
		{"cache", `{"model":"sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]}]}`, 400},
		{"bad JSON", `{"SECRET`, 400},
		{"oversize", strings.Repeat("x", maxRequestBytes+1), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newMessagesBridge("unused", "sonnet", map[string]principal{"key": {"t", "k"}}, runtime.NewSystemClock())
			defer b.close()
			r := httptest.NewRequest("POST", "http://127.0.0.1/v1/messages", strings.NewReader(tc.body))
			r.Header.Set("X-Api-Key", "key")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			b.ServeHTTP(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "SECRET") {
				t.Fatalf("status/leak = %d/%t, want %d/false", w.Code, strings.Contains(w.Body.String(), "SECRET"), tc.status)
			}
		})
	}
}

type messagesClock struct{ created chan chan time.Time }

func (c messagesClock) Now() time.Time { return time.Unix(0, 0) }
func (c messagesClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	ch := make(chan time.Time, 1)
	c.created <- ch
	return ch, func() bool { return true }
}

// TestMessagesExpiry advances an injected clock and observes session cleanup.
// TestMessagesExpiry 推进注入时钟，并观察会话回收。
func TestMessagesExpiry(t *testing.T) {
	clock := messagesClock{created: make(chan chan time.Time, 8)}
	b := newMessagesBridge("unused", "sonnet", nil, clock)
	b.start = func(s *messagesSession) { <-s.ctx.Done() }
	defer b.close()
	s, err := b.acquire(principal{"t", "k"}, testMessagesRequest())
	if err != nil {
		t.Fatal(err)
	}
	<-clock.created
	phase := <-clock.created
	phase <- time.Unix(1, 0)
	<-s.done
	if s.ctx.Err() != context.Canceled {
		t.Fatalf("expired context = %v, want canceled", s.ctx.Err())
	}
	b.mu.Lock()
	n := len(b.sessions)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("sessions = %d, want 0", n)
	}
}
