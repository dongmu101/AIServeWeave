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
		{"conversation reset", "unsupported_envelope", strings.NewReader("{\"type\":\"conversation_reset\"}\n"), 1},
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
	upstreamCall := func(name string) string {
		return "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"}}\n{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"" + name + "\",\"id\":\"fixture\",\"input\":{}}}}\n"
	}
	for _, tc := range []struct {
		name, reason string
		request      messagesRequest
		wire         string
		line         float64
	}{
		{"local registration", "unexpected_local_tool", guardedRequest(), "{\"type\":\"system\",\"subtype\":\"init\",\"tools\":[\"private-payload\"]}\n", 1},
		// A bridged round runs calls through the local MCP bridge, so an
		// undeclared name is refused. 桥接回合经本机 MCP 桥执行调用，未声明的名称被拒绝。
		{"undeclared upstream tool in a bridged round", "undeclared_upstream_tool", testMessagesRequest(), upstreamCall("private-payload"), 2},
		// A native round still refuses names the local CLI could run itself.
		// 原生回合仍拒绝本机 CLI 自己能执行的名称。
		{"undeclared local CLI tool in a native round", "undeclared_upstream_tool", guardedRequest(), upstreamCall("EndConversation"), 2},
		{"undeclared bridge tool in a native round", "undeclared_upstream_tool", guardedRequest(), upstreamCall("mcp__aisw_bridge__tool_0"), 2},
		{"missing upstream id", "missing_upstream_tool_id", guardedRequest(), "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"}}\n{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"read_file\",\"input\":{}}}}\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &messagesSession{ctx: context.Background(), request: tc.request, events: make(chan bridgeEvent, 4)}
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
		{"conversation reset", `{"type":"conversation_reset"}`, true},
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

func TestBridgeEventsLifecycleNotifications(t *testing.T) {
	for _, notification := range []string{
		`{"type":"auth_status","isAuthenticating":true,"output":["private-lifecycle-payload"]}`,
		`{"type":"tool_progress","tool_use_id":"fixture","tool_name":"private-lifecycle-payload","elapsed_time_seconds":1}`,
		`{"type":"tool_use_summary","summary":"private-lifecycle-payload","preceding_tool_use_ids":[]}`,
		`{"type":"system","subtype":"task_progress","description":"private-lifecycle-payload"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
	} {
		var kind struct{ Type string }
		_ = json.Unmarshal([]byte(notification), &kind)
		for _, native := range []bool{false, true} {
			for _, trailing := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/native=%t/trailing=%t", kind.Type, native, trailing), func(t *testing.T) {
					var round, input bytes.Buffer
					fixtureRound(&round, "", "answer")
					round.WriteString("{\"type\":\"result\",\"subtype\":\"success\"}\n")
					if !trailing {
						fmt.Fprintln(&input, notification)
					}
					input.Write(round.Bytes())
					if trailing {
						fmt.Fprintln(&input, notification)
					}
					req := testMessagesRequest()
					if native {
						req = guardedRequest()
					}
					s := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 16)}
					final, err := bridgeEvents(&input, s)
					if err != nil || string(final) != `{"type":"message_stop"}` {
						t.Fatalf("documented lifecycle notification broke inference: %v / %s", err, final)
					}
					close(s.events)
					var reply bytes.Buffer
					for item := range s.events {
						reply.Write(item.data)
					}
					if bytes.Contains(reply.Bytes(), []byte("private-lifecycle-payload")) || !bytes.Contains(reply.Bytes(), []byte("answer")) {
						t.Fatal("lifecycle notice leaked into the inference reply or displaced the answer")
					}
				})
			}
		}
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

// TestNativeForwardsUndeclaredTool forwards a native call to a tool the
// caller did not declare, unchanged, so the caller can answer it ("no such
// tool") as it would against the API, instead of failing the round. This is
// what a model reaching for a deferred tool whose schema was not loaded does.
//
// TestNativeForwardsUndeclaredTool 原样转发原生回合中对调用方未声明工具的调用，让调用方
// 像直连 API 时一样答复（「没有这个工具」），而不是让整轮失败。模型调用尚未载入 schema 的
// 延迟工具时就是这种情况。
func TestNativeForwardsUndeclaredTool(t *testing.T) {
	plain := testMessagesRequest()
	plain.native = true
	for name, req := range map[string]messagesRequest{"native": plain, "native_safeguards": guardedRequest()} {
		t.Run(name, func(t *testing.T) {
			var input bytes.Buffer
			emitFixture(&input, map[string]any{"type": "message_start"})
			emitFixture(&input, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_upstream_1", "name": "Monitor", "input": map[string]any{}}})
			emitFixture(&input, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": `{"command":"true"}`}})
			emitFixture(&input, map[string]any{"type": "content_block_stop", "index": 0})
			emitFixture(&input, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}})
			emitFixture(&input, map[string]any{"type": "message_stop"})
			session := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 16), pending: make(map[string]*pendingTool), changed: make(chan struct{})}
			_, err := bridgeEvents(&input, session)
			if errors.Is(err, errUnsafeTools) {
				t.Fatalf("an undeclared tool failed a native round: %v", err)
			}
			if err != nil && !errors.Is(err, errTerminalStop) {
				t.Fatalf("native stream rejected: %v", err)
			}
			close(session.events)
			var output strings.Builder
			for event := range session.events {
				output.Write(event.data)
			}
			if !strings.Contains(output.String(), `"name":"Monitor"`) || !strings.Contains(output.String(), `"id":"toolu_upstream_1"`) ||
				!strings.Contains(output.String(), `"stop_reason":"tool_use"`) {
				t.Fatalf("undeclared call was not forwarded unchanged: %s", &output)
			}
		})
	}
}
