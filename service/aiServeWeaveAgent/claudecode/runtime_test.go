package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// terminalProcessFixture emits a terminal response, then attempts the CLI's automatic recovery.
// terminalProcessFixture 发出终止响应后尝试 CLI 的自动恢复，用于验证桥接会停止进程。
func terminalProcessFixture() {
	reason := os.Getenv("AISW_CLAUDE_TERMINAL_FIXTURE")
	var input bytes.Buffer
	fixtureRound(&input, "", "partial answer")
	fmt.Print(strings.ReplaceAll(input.String(), `"stop_reason":"end_turn"`, `"stop_reason":"`+reason+`"`))
	if reason != "end_turn" {
		emitFixture(os.Stdout, map[string]string{"type": "message_start"})
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("AISW_CLAUDE_RESULT_FAILURE") == "1" {
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true}`)
		return
	}
	fmt.Println(`{"type":"result","subtype":"success"}`)
}

// TestRuntimeTerminalStopReasons preserves valid terminal responses instead of failing the stream.
// TestRuntimeTerminalStopReasons 保留合法的终止响应，不把它们误报为流故障。
func TestRuntimeTerminalStopReasons(t *testing.T) {
	for _, tc := range []struct {
		reason  string
		failure bool
	}{
		{"end_turn", false}, {"max_tokens", false}, {"stop_sequence", false}, {"refusal", false}, {"model_context_window_exceeded", false}, {"end_turn", true},
	} {
		t.Run(fmt.Sprintf("%s/failure=%t", tc.reason, tc.failure), func(t *testing.T) {
			rt, err := NewFactory(os.Args[0])(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			defer r.Close()
			r.models = map[string]struct{}{"claude-opus-5-5": {}}
			r.bridge.env = append(r.bridge.cliEnv(), "AISW_CLAUDE_TERMINAL_FIXTURE="+tc.reason)
			if tc.failure {
				r.bridge.env = append(r.bridge.env, "AISW_CLAUDE_RESULT_FAILURE=1")
			}
			body, _ := json.Marshal(testMessagesRequest())
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", TenantID: "tenant", KeyID: "key", JSON: body})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var gotReason, text string
			stops := 0
			for {
				item, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					if tc.failure {
						var failure *runtime.RuntimeError
						if !errors.As(err, &failure) || failure.Code != runtime.ErrorProtocol {
							t.Fatalf("error=%v, want protocol error", err)
						}
						return
					}
					t.Fatalf("legal stop_reason=%s failed: %v", tc.reason, err)
				}
				var event struct {
					Type  string
					Delta struct {
						Text       string
						StopReason string `json:"stop_reason"`
					}
				}
				if err := json.Unmarshal(item.JSON, &event); err != nil {
					t.Fatal(err)
				}
				text += event.Delta.Text
				if event.Type == "message_delta" {
					gotReason = event.Delta.StopReason
				}
				if event.Type == "message_stop" {
					stops++
				}
			}
			if tc.failure {
				t.Fatal("unsuccessful CLI result was accepted")
			}
			if gotReason != tc.reason || text != "partial answer" || stops != 1 {
				t.Fatalf("reason/text/stops=%s/%q/%d, want %s/partial answer/1", gotReason, text, stops, tc.reason)
			}
		})
	}
}

// TestRoundFailureLoggedAfterDisconnect records an observed parser failure even if its caller leaves.
// TestRoundFailureLoggedAfterDisconnect 在调用方断开时仍记录已观察到的解析失败。
func TestRoundFailureLoggedAfterDisconnect(t *testing.T) {
	var output bytes.Buffer
	rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&output, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	defer r.Close()
	r.models = map[string]struct{}{"claude-opus-5-5": {}}
	observed := make(chan struct{})
	r.bridge.start = func(s *messagesSession) {
		_ = s.send(json.RawMessage(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`), nil)
		_ = s.send(nil, errCLIProtocol)
		close(observed)
		<-s.ctx.Done()
	}
	body, _ := json.Marshal(testMessagesRequest())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", TenantID: "tenant", KeyID: "key", JSON: body})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	<-observed // respond has taken the failure and is blocked writing its error event / respond 已接收失败，并阻塞在写错误事件
	cancel()
	_ = stream.Close()
	if !strings.Contains(output.String(), `"reason":"cli_protocol"`) {
		t.Fatalf("diagnostic=%s, want observed protocol failure despite disconnect", &output)
	}
}

// TestRoundFailureDiagnostics keeps useful local causes while withholding arbitrary error output.
// TestRoundFailureDiagnostics 保留有用的本机错误分类，不泄露任意错误输出。
func TestRoundFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		cause        error
	}{
		{"protocol", "cli_protocol", fmt.Errorf("private-error-detail: %w", errCLIProtocol)},
		{"result", "cli_result", errCLIResult},
		{"exit", "cli_exit", errCLIExit},
		{"missing result", "missing_result", errMissingResult},
		{"unsafe tools", "unsafe_tools", errUnsafeTools},
		{"unknown", "unknown", errors.New("private-error-detail")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&output, nil))})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			defer r.Close()
			public := r.roundError(tc.cause)
			var record map[string]any
			if json.Unmarshal(output.Bytes(), &record) != nil || record["reason"] != tc.reason || record["runtime_id"] != "local" {
				t.Fatalf("diagnostic=%s, want runtime local and reason %s", &output, tc.reason)
			}
			if strings.Contains(output.String()+public.Error(), "private-error-detail") {
				t.Fatal("private error detail reached logs or caller")
			}
		})
	}
}

// TestRuntimeMessages keeps a completed tool session alive after its tunnel request closes.
// TestRuntimeMessages 在隧道请求关闭后仍保留已完成回合的工具会话。
func TestRuntimeMessages(t *testing.T) {
	rt, err := NewFactory("unused")(runtime.Config{ID: "claude-local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	r.bridge.start = fixtureStart
	r.models = map[string]struct{}{"claude-sonnet-4-6": {}}
	defer r.Close()
	req := testMessagesRequest()
	round := func(parent context.Context, req messagesRequest) []messageBlock {
		t.Helper()
		data, _ := json.Marshal(req)
		stream, err := r.Messages(parent, runtime.MessagesRequest{Model: "claude-sonnet-4-6", TenantID: "tenant", KeyID: "key", JSON: data})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var content []messageBlock
		stops := 0
		for {
			item, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			var event struct {
				Type  string
				Index int
				Block messageBlock `json:"content_block"`
				Delta struct {
					Type, Text string
					Partial    string `json:"partial_json"`
				}
			}
			if json.Unmarshal(item.JSON, &event) != nil {
				t.Fatal("invalid native JSON event")
			}
			switch event.Type {
			case "content_block_start":
				content = append(content, event.Block)
				if event.Block.Type == "tool_use" {
					content[event.Index].Input = nil
				}
			case "content_block_delta":
				content[event.Index].Text += event.Delta.Text
				content[event.Index].Input = append(content[event.Index].Input, event.Delta.Partial...)
			case "message_stop":
				stops++
			}
		}
		if stops != 1 {
			t.Fatalf("message_stop count=%d, want 1", stops)
		}
		return content
	}
	ctx, cancel := context.WithCancel(t.Context())
	content := round(ctx, req)
	cancel()
	if len(content) != 1 || content[0].Type != "tool_use" {
		t.Fatal("first round must return one tool")
	}
	data, _ := json.Marshal(content)
	req.Messages = append(req.Messages, bridgeMessage{Role: "assistant", Content: data})
	data, _ = json.Marshal([]messageBlock{{Type: "tool_result", ToolUseID: content[0].ID, Content: json.RawMessage(`"caller-result"`)}})
	req.Messages = append(req.Messages, bridgeMessage{Role: "user", Content: data})
	content = round(t.Context(), req)
	if len(content) != 1 || content[0].Text != "caller-result" {
		t.Fatal("final text did not match caller result")
	}
}

// TestToolSchemaDefaults permits declared defaults without accepting altered arguments.
// TestToolSchemaDefaults 允许显式声明的默认值，但不接受修改已有参数。
func TestToolSchemaDefaults(t *testing.T) {
	tools := []bridgeTool{{Name: "Edit", InputSchema: json.RawMessage(`{"properties":{"replace_all":{"default":false}}}`)}}
	for _, tc := range []struct {
		name, before, after string
		equal               bool
	}{
		{"declared default", `{"path":"a"}`, `{"path":"a","replace_all":false}`, true},
		{"changed explicit value", `{"path":"a","replace_all":true}`, `{"path":"a","replace_all":false}`, false},
		{"undeclared argument", `{"path":"a"}`, `{"path":"a","offset":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := canonical(normalizedToolInput("Edit", json.RawMessage(tc.before), tools))
			b := canonical(normalizedToolInput("Edit", json.RawMessage(tc.after), tools))
			if (a == b) != tc.equal {
				t.Fatalf("equivalent=%t, want %t", a == b, tc.equal)
			}
		})
	}
}

// TestNativeOwnership rejects missing attribution before starting a CLI.
// TestNativeOwnership 在启动 CLI 前拒绝缺失的归属信息。
func TestNativeOwnership(t *testing.T) {
	rt, _ := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	r := rt.(*Runtime)
	defer r.Close()
	for _, tc := range []struct{ name, tenant, key string }{
		{"missing tenant", "", "key"}, {"missing key", "tenant", ""}, {"oversized key", "tenant", strings.Repeat("k", 257)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Messages(t.Context(), runtime.MessagesRequest{Model: "claude-sonnet-4-6", TenantID: tc.tenant, KeyID: tc.key, JSON: json.RawMessage(`{}`)})
			if err == nil {
				t.Fatal("error=nil, want invalid config")
			}
		})
	}
}

// TestRuntimeCloseUnconsumedStream releases synchronous writers during shutdown.
// TestRuntimeCloseUnconsumedStream 在关闭时释放同步等待消费的写入方。
func TestRuntimeCloseUnconsumedStream(t *testing.T) {
	rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	r.bridge.start = fixtureStart
	r.models = map[string]struct{}{"claude-sonnet-4-6": {}}
	data, _ := json.Marshal(testMessagesRequest())
	stream, err := r.Messages(t.Context(), runtime.MessagesRequest{Model: "claude-sonnet-4-6", TenantID: "tenant", KeyID: "key", JSON: data})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeCapacityBound keeps even an unlimited configuration within maxSessions resident processes.
// TestClaudeCapacityBound 即使配置无限并发也将驻留进程限制在 maxSessions 以内。
func TestClaudeCapacityBound(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configured, want int
	}{
		{"unlimited", -1, maxSessions},
		{"default", 0, defaultSessions},
		{"one", 1, 1},
		{"many", 16, 16},
		{"at cap", maxSessions, maxSessions},
		{"over cap", maxSessions + 1, maxSessions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude, MaxConcurrent: tc.configured}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			if got := rt.Descriptor().MaxConcurrent; got != tc.want {
				t.Fatalf("capacity=%d,want %d", got, tc.want)
			}
		})
	}
}
