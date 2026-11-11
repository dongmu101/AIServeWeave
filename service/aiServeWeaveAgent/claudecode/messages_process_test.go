package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"AIServeWeave/common/runtime"
)

// TestBridgeEventsFailureLocation identifies a rejected event without retaining its payload.
// TestBridgeEventsFailureLocation 标记被拒事件的位置，不保留事件载荷。
func TestBridgeEventsFailureLocation(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		reader       io.Reader
		line         float64
	}{
		{"invalid JSON", "invalid_json", strings.NewReader("private-payload\n"), 1},
		{"unknown event", "unsupported_envelope", strings.NewReader("{\"type\":\"private-payload\"}\n"), 1},
		{"unknown stop reason", "unsupported_stop_reason", strings.NewReader("{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"}}\n{\"type\":\"stream_event\",\"event\":{\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"private-payload\"}}}\n"), 2},
		{"read failure", "stream_read_failed", iotest.ErrReader(errors.New("private-payload")), 1},
		{"partial line read failure", "stream_read_failed", io.MultiReader(strings.NewReader(`{"type":"system","subtype":"status"}`), iotest.ErrReader(errors.New("private-payload"))), 1},
		{"next line read failure", "stream_read_failed", io.MultiReader(strings.NewReader("{\"type\":\"system\",\"subtype\":\"status\"}\n"), iotest.ErrReader(errors.New("private-payload"))), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &messagesSession{ctx: context.Background(), request: testMessagesRequest(), events: make(chan bridgeEvent, 4)}
			_, cause := bridgeEvents(tc.reader, s)
			if !errors.Is(cause, errCLIProtocol) {
				t.Fatalf("error=%v, want CLI protocol error", cause)
			}
			var output bytes.Buffer
			rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&output, nil))})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			defer r.Close()
			public := r.roundError(cause)
			var record map[string]any
			if json.Unmarshal(output.Bytes(), &record) != nil || record["cli_event_error"] != tc.reason || record["cli_event_line"] != tc.line {
				t.Fatalf("diagnostic=%s, want line %v and reason %s", &output, tc.line, tc.reason)
			}
			if strings.Contains(output.String()+public.Error(), "private-payload") {
				t.Fatal("event payload reached logs or caller")
			}
		})
	}
}

func TestUnsafeToolFailureLocation(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		wire         string
		line         float64
	}{
		{"local registration", "unexpected_local_tool", "{\"type\":\"system\",\"subtype\":\"init\",\"tools\":[\"private-payload\"]}\n", 1},
		{"undeclared upstream tool", "undeclared_upstream_tool", "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"}}\n{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"private-payload\",\"id\":\"fixture\",\"input\":{}}}}\n", 2},
		{"missing upstream id", "missing_upstream_tool_id", "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"}}\n{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"read_file\",\"input\":{}}}}\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 4)}
			_, cause := bridgeEvents(strings.NewReader(tc.wire), s)
			if !errors.Is(cause, errUnsafeTools) {
				t.Fatalf("tool isolation was bypassed: %v", cause)
			}
			var logs bytes.Buffer
			r := &Runtime{cfg: runtime.Config{ID: "fixture"}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			public := r.roundError(cause)
			var record map[string]any
			if json.Unmarshal(logs.Bytes(), &record) != nil || record["reason"] != "unsafe_tools" || record["cli_event_error"] != tc.reason || record["cli_event_line"] != tc.line {
				t.Fatalf("unsafe tool location missing: %s", &logs)
			}
			if strings.Contains(logs.String()+public.Error()+cause.Error(), "private-payload") {
				t.Fatal("tool payload reached diagnostics")
			}
		})
	}
}

// TestBridgeEventsTrailingNotifications allows CLI lifecycle notices after a successful result.
// TestBridgeEventsTrailingNotifications 允许成功结果之后的 CLI 生命周期通知。
func TestBridgeEventsTrailingNotifications(t *testing.T) {
	for _, tc := range []struct {
		name, trailing string
		wantErr        bool
	}{
		{"prompt suggestion", `{"type":"prompt_suggestion","suggestion":"private-payload"}`, false},
		{"status notice", `{"type":"system","subtype":"status","status":null}`, false},
		{"new init", `{"type":"system","subtype":"init","tools":["Bash"]}`, true},
		{"new model output", `{"type":"stream_event","event":{"type":"message_start"}}`, true},
		{"duplicate result", `{"type":"result","subtype":"success"}`, true},
		{"unknown envelope", `{"type":"private-payload"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input bytes.Buffer
			fixtureRound(&input, "", "answer")
			fmt.Fprintln(&input, `{"type":"result","subtype":"success"}`)
			fmt.Fprintln(&input, tc.trailing)
			s := &messagesSession{ctx: context.Background(), request: testMessagesRequest(), events: make(chan bridgeEvent, 16)}
			final, err := bridgeEvents(&input, s)
			if tc.wantErr {
				if !errors.Is(err, errCLIProtocol) {
					t.Fatalf("error=%v, want protocol error", err)
				}
				return
			}
			if err != nil || string(final) != `{"type":"message_stop"}` {
				t.Fatalf("final/error=%s/%v, want message_stop/nil", final, err)
			}
			close(s.events)
			for item := range s.events {
				if bytes.Contains(item.data, []byte("private-payload")) {
					t.Fatal("CLI notification reached the public stream")
				}
			}
		})
	}
}

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
