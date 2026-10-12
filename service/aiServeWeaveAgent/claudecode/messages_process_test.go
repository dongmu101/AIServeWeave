package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// TestBridgeEventsInternalThinking verifies hidden CLI blocks preserve public tool and text indices.
// TestBridgeEventsInternalThinking 验证 CLI 内部思考块不会破坏对外工具和文本的索引。
func TestBridgeEventsInternalThinking(t *testing.T) {
	for _, tc := range []struct {
		name, kind, delta string
		wantErr           bool
	}{
		{"thinking", "thinking", "thinking_delta", false},
		{"redacted thinking", "redacted_thinking", "", false},
		{"invalid thinking delta", "thinking", "text_delta", true},
		{"invalid redacted delta", "redacted_thinking", "thinking_delta", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input bytes.Buffer
			start := func() { emitFixture(&input, map[string]string{"type": "message_start"}) }
			hidden := func(index int) {
				emitFixture(&input, map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]string{"type": tc.kind, "thinking": "private fixture", "data": "private fixture"}})
				if tc.delta != "" {
					emitFixture(&input, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]string{"type": tc.delta, "thinking": "private fixture"}})
				}
				if tc.kind == "thinking" {
					emitFixture(&input, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]string{"type": "signature_delta", "signature": "private fixture"}})
				}
				emitFixture(&input, map[string]any{"type": "content_block_stop", "index": index})
			}
			visible := func(index int, tool bool) {
				block := messageBlock{Type: "text"}
				delta := map[string]string{"type": "text_delta", "text": "OK"}
				if tool {
					block = messageBlock{Type: "tool_use", ID: "fixture-call", Name: "mcp__aisw_bridge__tool_0", Input: json.RawMessage(`{}`)}
					delta = map[string]string{"type": "input_json_delta", "partial_json": `{"path":"fixture.txt"}`}
				}
				emitFixture(&input, map[string]any{"type": "content_block_start", "index": index, "content_block": block})
				emitFixture(&input, map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
				emitFixture(&input, map[string]any{"type": "content_block_stop", "index": index})
			}
			stop := func(reason string) {
				emitFixture(&input, map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": reason}})
				emitFixture(&input, map[string]string{"type": "message_stop"})
			}
			start()
			hidden(0)
			visible(1, false)
			hidden(2)
			visible(3, true)
			stop("tool_use")
			start()
			hidden(0)
			visible(1, false)
			stop("end_turn")
			fmt.Fprintln(&input, `{"type":"result","subtype":"success"}`)
			s := &messagesSession{ctx: context.Background(), request: testMessagesRequest(), events: make(chan bridgeEvent, 32), pending: make(map[string]*pendingTool), changed: make(chan struct{})}
			final, err := bridgeEvents(&input, s)
			if tc.wantErr {
				if !errors.Is(err, errCLIProtocol) {
					t.Fatalf("error=%v, want fixed protocol error", err)
				}
				return
			}
			if err != nil || string(final) != `{"type":"message_stop"}` {
				t.Fatalf("error/final=%v/%s, want nil/message_stop", err, final)
			}
			close(s.events)
			var indices []int
			for item := range s.events {
				if bytes.Contains(item.data, []byte("private fixture")) || bytes.Contains(item.data, []byte("thinking")) {
					t.Fatal("internal thinking reached public stream, want text/tools only")
				}
				var event struct {
					Index *int `json:"index"`
				}
				if err := json.Unmarshal(item.data, &event); err != nil {
					t.Fatal(err)
				}
				if event.Index != nil {
					indices = append(indices, *event.Index)
				}
			}
			want := []int{0, 0, 0, 1, 1, 1, 0, 0, 0}
			if !reflect.DeepEqual(indices, want) || len(s.pending) != 1 {
				t.Fatalf("indices/pending=%v/%d, want %v/1", indices, len(s.pending), want)
			}
		})
	}
}
