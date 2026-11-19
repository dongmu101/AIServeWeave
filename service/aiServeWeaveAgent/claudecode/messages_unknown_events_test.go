package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func nativeRequest() messagesRequest {
	req := testMessagesRequest()
	req.native = true
	return req
}

// nativeTextRound writes a complete native text round; between runs extra
// lines after message_start.
// nativeTextRound 写出一个完整的原生文本回合；between 是插在 message_start 之后的额外行。
func nativeTextRound(between string, stopReason string) string {
	var b bytes.Buffer
	emitFixture(&b, map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "content": []any{}}})
	b.WriteString(between)
	emitFixture(&b, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	emitFixture(&b, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "hello"}})
	emitFixture(&b, map[string]any{"type": "content_block_stop", "index": 0})
	emitFixture(&b, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason}})
	emitFixture(&b, map[string]any{"type": "message_stop"})
	return b.String()
}

func runBridge(t *testing.T, req messagesRequest, wire string) (*messagesSession, string, error) {
	t.Helper()
	s := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 64), pending: make(map[string]*pendingTool), changed: make(chan struct{})}
	_, err := bridgeEvents(strings.NewReader(wire), s)
	close(s.events)
	var out strings.Builder
	for event := range s.events {
		out.Write(event.data)
		out.WriteByte('\n')
	}
	return s, out.String(), err
}

// TestNativeRoundSkipsUnknownTopLevelEvents skips top-level types a newer
// CLI may add, before and after the result, and counts them; a bridged round
// and the two types that change meaning still fail.
// TestNativeRoundSkipsUnknownTopLevelEvents 跳过新版 CLI 可能新增的顶层类型（结果之前与之后）
// 并计数；桥接回合以及两种会改变含义的类型仍然失败。
func TestNativeRoundSkipsUnknownTopLevelEvents(t *testing.T) {
	future := `{"type":"future_notice","payload":"PRIVATE_MARKER"}` + "\n"
	result := `{"type":"result","subtype":"success"}` + "\n"
	s, out, err := runBridge(t, nativeRequest(), future+nativeTextRound(future, "end_turn")+result+future)
	if err != nil || !strings.Contains(out, `"text":"hello"`) || strings.Contains(out, "PRIVATE_MARKER") {
		t.Fatalf("error=%v output=%s, want the round forwarded without the skipped events", err, out)
	}
	if got := s.ignoredEvents.Load(); got != 3 {
		t.Fatalf("ignored=%d, want 3", got)
	}
	for _, tc := range []struct {
		name string
		req  messagesRequest
		line string
	}{
		{"bridged round", testMessagesRequest(), future},
		{"conversation reset", nativeRequest(), `{"type":"conversation_reset"}` + "\n"},
		{"control request", nativeRequest(), `{"type":"control_request","request_id":"r1"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runBridge(t, tc.req, tc.line)
			var failure *cliEventError
			if !errors.As(err, &failure) || failure.reason != "unsupported_envelope" {
				t.Fatalf("error=%v, want unsupported_envelope", err)
			}
		})
	}
	if _, _, err := runBridge(t, testMessagesRequest(), `{"type":"keep_alive"}`+"\n"+result); !errors.Is(err, errMissingResult) && !errors.Is(err, errCLIResult) {
		// keep_alive is skipped in every round; the bare result then fails for lacking a reply.
		// keep_alive 在所有回合中都被跳过；随后孤立的结果因缺少回复而失败。
		t.Fatalf("error=%v, want keep_alive skipped and the empty round to fail on its result", err)
	}
}

// TestNativeRoundForwardsUnknownStreamContent forwards stream events, deltas
// and stop reasons a native round does not know, as the Messages API would;
// a bridged round and an error event still fail.
// TestNativeRoundForwardsUnknownStreamContent 像 Messages API 一样转发原生回合不认识的流事件、
// 增量与停止原因；桥接回合与 error 事件仍然失败。
func TestNativeRoundForwardsUnknownStreamContent(t *testing.T) {
	var event bytes.Buffer
	emitFixture(&event, map[string]any{"type": "future_event", "data": "kept"})
	_, out, err := runBridge(t, nativeRequest(), nativeTextRound(event.String(), "end_turn")+`{"type":"result","subtype":"success"}`+"\n")
	if err != nil || !strings.Contains(out, `"type":"future_event"`) {
		t.Fatalf("error=%v output=%s, want the unknown event forwarded", err, out)
	}
	_, out, err = runBridge(t, nativeRequest(), nativeTextRound("", "future_stop"))
	if !errors.Is(err, errTerminalStop) || !strings.Contains(out, `"stop_reason":"future_stop"`) {
		t.Fatalf("error=%v output=%s, want an unknown stop reason forwarded as terminal", err, out)
	}
	var delta bytes.Buffer
	emitFixture(&delta, map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "content": []any{}}})
	emitFixture(&delta, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	emitFixture(&delta, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "future_delta", "value": "kept"}})
	_, out, err = runBridge(t, nativeRequest(), delta.String())
	if !errors.Is(err, errMissingResult) || !strings.Contains(out, `"type":"future_delta"`) {
		t.Fatalf("error=%v output=%s, want the unknown delta forwarded", err, out)
	}
	var stream bytes.Buffer
	emitFixture(&stream, map[string]any{"type": "error", "error": map[string]string{"type": "overloaded_error"}})
	for _, tc := range []struct {
		name, reason, wire string
		req                messagesRequest
	}{
		{"bridged unknown event", "unsupported_stream_event", event.String(), testMessagesRequest()},
		{"native error event", "unsupported_stream_event", stream.String(), nativeRequest()},
		{"bridged unknown delta", "unsupported_block_delta", delta.String(), testMessagesRequest()},
		{"bridged unknown stop reason", "unsupported_stop_reason", nativeTextRound("", "future_stop"), testMessagesRequest()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runBridge(t, tc.req, tc.wire)
			var failure *cliEventError
			if !errors.As(err, &failure) || failure.reason != tc.reason {
				t.Fatalf("error=%v, want %s", err, tc.reason)
			}
		})
	}
}

// TestStartupFailureReasonIsLogged logs the reason a CLI gave for refusing to
// start, bounded to an identifier, and keeps the round a backend failure.
// TestStartupFailureReasonIsLogged 记录 CLI 给出的拒绝启动原因（限制为标识符），回合仍是后端失败。
func TestStartupFailureReasonIsLogged(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{
		{"config_file_invalid", "config_file_invalid"},
		{"PRIVATE_MARKER text", "other"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			_, _, cause := runBridge(t, nativeRequest(), `{"type":"result","subtype":"error_during_execution","is_error":true,"startup_failure_reason":"`+tc.reason+`"}`+"\n")
			if !errors.Is(cause, errCLIResult) {
				t.Fatalf("error=%v, want a CLI result failure", cause)
			}
			var logs bytes.Buffer
			r := &Runtime{cfg: runtime.Config{ID: "fixture"}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			var failure *runtime.RuntimeError
			if !errors.As(r.roundError(cause), &failure) || failure.Code != runtime.ErrorUpstream {
				t.Fatalf("public=%+v, want upstream_error", failure)
			}
			var record map[string]any
			if json.Unmarshal(logs.Bytes(), &record) != nil || record["reason"] != "cli_result" || record["cli_startup_failure"] != tc.want || strings.Contains(logs.String(), "PRIVATE_MARKER") {
				t.Fatalf("diagnostic=%s, want cli_startup_failure=%s", &logs, tc.want)
			}
		})
	}
}

// TestCLIStderrCategoryIsLogged runs a CLI that fails with a message on
// standard error and logs only its fixed category.
// TestCLIStderrCategoryIsLogged 运行一个在标准错误上报错后失败的 CLI，只记录其固定分类。
func TestCLIStderrCategoryIsLogged(t *testing.T) {
	var logs bytes.Buffer
	rt, err := NewFactory(os.Args[0])(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	defer r.Close()
	r.models = map[string]struct{}{"claude-opus-5-5": {}}
	r.bridge.env = append(r.bridge.cliEnv(), "AISW_CLAUDE_STDERR_FIXTURE=1")
	body, _ := json.Marshal(testMessagesRequest())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", TenantID: "tenant", KeyID: "key", JSON: body})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for {
		if _, err = stream.Recv(); err != nil {
			break
		}
	}
	var failure *runtime.RuntimeError
	if err == io.EOF || !errors.As(err, &failure) || failure.Code != runtime.ErrorUpstream {
		t.Fatalf("error=%v, want a backend failure", err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "Claude Messages round failed" {
			found = record["cli_stderr"] == "login_required"
		}
	}
	if !found || strings.Contains(logs.String(), "PRIVATE_MARKER") {
		t.Fatalf("logs=%s, want cli_stderr=login_required without the stderr text", &logs)
	}
}
