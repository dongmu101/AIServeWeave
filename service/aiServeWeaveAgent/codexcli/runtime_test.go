//go:build darwin || linux

package codexcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/codexcli/internal/codextest"
)

func fixtureRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := NewFactory(os.Args[0])(runtime.Config{ID: "codex-local", Kind: runtime.KindCodex}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	r.env = append(os.Environ(), "AISW_CODEX_FIXTURE=runtime")
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func chatRequest() runtime.ChatRequest {
	return runtime.ChatRequest{Model: "fixture-model", Messages: []runtime.ChatMessage{{Role: "user", Content: "Hello"}}, Tools: []runtime.Tool{{Type: "function", Function: runtime.FunctionDefinition{Name: "echo", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}}}
}

// TestRuntimeLifecycle exercises real subprocess discovery, streaming, and tool history.
// TestRuntimeLifecycle 验证真实子进程的发现、流式输出及工具历史。
func TestRuntimeLifecycle(t *testing.T) {
	r := fixtureRuntime(t)
	models, err := r.ListModels(t.Context())
	if err != nil || len(models) != 1 || models[0].ID != "fixture-model" {
		t.Fatalf("models=%+v error=%v, want fixture-model and nil", models, err)
	}
	req := chatRequest()
	response, err := r.Chat(t.Context(), req)
	if err != nil || response.Message.Content != "Hello world" || response.FinishReason != "stop" || response.Usage.TotalTokens != 8 {
		t.Fatalf("response=%+v error=%v, want Hello world, stop and 8 tokens", response, err)
	}
	req.Model = "tool-model"
	first, err := r.Chat(t.Context(), req)
	if err != nil || first.FinishReason != "tool_calls" || len(first.Message.ToolCalls) != 1 {
		t.Fatalf("response=%+v error=%v, want one caller tool call", first, err)
	}
	call := first.Message.ToolCalls[0]
	req.Messages = append(req.Messages, first.Message, runtime.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: "unique caller result"})
	second, err := r.Chat(t.Context(), req)
	if err != nil || second.Message.Content != "unique caller result" {
		t.Fatalf("response=%+v error=%v, want caller result", second, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = r.Chat(t.Context(), chatRequest())
	if !errors.Is(err, runtime.ErrRuntimeClosed) {
		t.Fatalf("error=%v, want closed runtime", err)
	}
}

// TestPrepareChat rejects unsupported fields and malformed or incomplete history.
// TestPrepareChat 拒绝不支持的字段及无效、不完整的工具历史。
func TestPrepareChat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*runtime.ChatRequest)
		want   error
	}{
		{"plain", func(*runtime.ChatRequest) {}, nil},
		{"temperature", func(r *runtime.ChatRequest) { v := 0.0; r.Temperature = &v }, errUnsupported},
		{"forced tool", func(r *runtime.ChatRequest) { r.ToolChoice = "required" }, errUnsupported},
		{"duplicate tool", func(r *runtime.ChatRequest) { r.Tools = append(r.Tools, r.Tools[0]) }, ErrConfig},
		{"invalid schema", func(r *runtime.ChatRequest) { r.Tools[0].Function.Parameters = json.RawMessage(`null`) }, ErrConfig},
		{"orphan output", func(r *runtime.ChatRequest) {
			r.Messages = append(r.Messages, runtime.ChatMessage{Role: "tool", ToolCallID: "unknown", Content: "SECRET"})
		}, ErrConfig},
		{"missing output", func(r *runtime.ChatRequest) {
			r.Messages = append(r.Messages, runtime.ChatMessage{Role: "assistant", ToolCalls: []runtime.ToolCall{{ID: "call1", Type: "function", Function: runtime.FunctionCall{Name: "echo", Arguments: "{}"}}}})
		}, ErrConfig},
		{"image", func(r *runtime.ChatRequest) {
			r.Messages[0].Content = ""
			r.Messages[0].ContentParts = []runtime.ContentPart{{Type: "image_url"}}
		}, errUnsupported},
		{"empty", func(r *runtime.ChatRequest) { r.Messages = nil }, ErrConfig},
		{"oversized", func(r *runtime.ChatRequest) { r.Messages[0].Content = strings.Repeat("x", maxFrameBytes) }, ErrConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := chatRequest()
			tc.change(&req)
			_, err := prepareChat(req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
}

func protocolFixture(mode string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	send := func(v any) { _ = enc.Encode(v) }
	send(map[string]any{"id": 1, "result": map[string]any{}})
	account := "chatgpt"
	if mode == "auth" {
		account = "apiKey"
	}
	send(map[string]any{"id": 2, "result": map[string]any{"account": map[string]string{"type": account}}})
	sources := []string{}
	if mode == "instructions" {
		sources = []string{"/SECRET/AGENTS.md"}
	}
	send(map[string]any{"id": 3, "result": map[string]any{"thread": map[string]string{"id": "thread1"}, "model": "fixture-model", "instructionSources": sources}})
	send(map[string]any{"id": 4, "result": map[string]any{}})
	notification := func(method string, params map[string]any) {
		params["threadId"] = "thread1"
		params["turnId"] = "turn1"
		if mode == "cross-thread" {
			params["threadId"] = "SECRET"
		}
		if mode == "cross-turn" {
			params["turnId"] = "SECRET"
		}
		send(map[string]any{"method": method, "params": params})
	}
	if mode == "early" {
		notification("item/agentMessage/delta", map[string]any{"itemId": "msg1", "delta": "early"})
	}
	send(map[string]any{"id": 5, "result": map[string]any{"turn": map[string]string{"id": "turn1"}}})
	switch mode {
	case "tool", "unknown-tool", "namespace", "approval":
		tool, method := "echo", "item/tool/call"
		if mode == "unknown-tool" {
			tool = "shell"
		}
		if mode == "approval" {
			method = "item/commandExecution/requestApproval"
		}
		params := map[string]any{"threadId": "thread1", "turnId": "turn1", "callId": "call1", "tool": tool, "arguments": map[string]any{}}
		if mode == "namespace" {
			params["namespace"] = "other"
		}
		send(map[string]any{"id": "request1", "method": method, "params": params})
	case "native":
		notification("item/started", map[string]any{"item": map[string]string{"type": "commandExecution"}})
	case "eof":
	default:
		notification("item/agentMessage/delta", map[string]any{"itemId": "msg1", "delta": "Hello"})
		status := "completed"
		if mode == "failed" {
			status = "failed"
		}
		turn := "turn1"
		if mode == "wrong-terminal" {
			turn = "wrong"
		}
		notification("turn/completed", map[string]any{"turn": map[string]string{"id": turn, "status": status}})
	}
	return b.Bytes()
}

// TestRuntimeProtocol covers callback isolation, early deltas, and terminal failures.
// TestRuntimeProtocol 覆盖回调隔离、提前到达的增量及终态失败。
func TestRuntimeProtocol(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"ok", nil}, {"early", nil}, {"tool", nil}, {"auth", ErrAuthentication}, {"instructions", ErrIsolation}, {"unknown-tool", ErrProtocol}, {"namespace", ErrProtocol}, {"approval", ErrUnexpectedTool}, {"native", ErrUnexpectedTool}, {"cross-thread", ErrProtocol}, {"cross-turn", ErrProtocol}, {"failed", ErrTurn}, {"wrong-terminal", ErrProtocol}, {"eof", ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &protocol{scanner: bufio.NewScanner(bytes.NewReader(protocolFixture(tc.name))), output: io.Discard}
			req := chatRequest()
			input, _ := prepareChat(req)
			var events []runtime.ChatEvent
			err := runChat(p, "/tmp/isolated", req, input, nil, func(e runtime.ChatEvent) error { events = append(events, e); return nil })
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if tc.want == nil && (len(events) < 2 || events[len(events)-1].FinishReason == "") {
				t.Fatalf("events=%+v, want output followed by finish", events)
			}
		})
	}
}

// TestRuntimeCanceledCaller verifies cancellation without starting a model service.
// TestRuntimeCanceledCaller 验证调用方取消，不启动模型服务。
func TestRuntimeCanceledCaller(t *testing.T) {
	r := fixtureRuntime(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Chat(ctx, chatRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want canceled", err)
	}
}

// TestRuntimeBackpressureAndClose bounds process concurrency and unblocks producers.
// TestRuntimeBackpressureAndClose 限制进程并发，并使被背压阻塞的生产者退出。
func TestRuntimeBackpressureAndClose(t *testing.T) {
	r := fixtureRuntime(t)
	r.limiter = runtime.NewLimiter(1)
	first, err := r.ChatStream(t.Context(), chatRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.ChatStream(t.Context(), chatRequest())
	if !errors.Is(err, runtime.ErrConcurrencyLimit) {
		t.Fatalf("error=%v, want concurrency limit", err)
	}
	if first.Committed() {
		t.Fatal("committed=true, want false before receiving")
	}
	if _, err := first.Recv(); err != nil {
		t.Fatal(err)
	}
	if !first.Committed() {
		t.Fatal("committed=false, want true after receiving")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = first.Recv()
	if !errors.Is(err, runtime.ErrStreamClosed) {
		t.Fatalf("error=%v, want closed stream", err)
	}
	second, err := r.ChatStream(t.Context(), chatRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Recv(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = second.Recv()
	if !errors.Is(err, runtime.ErrRuntimeClosed) {
		t.Fatalf("error=%v, want closed runtime", err)
	}
	_ = second.Close()
}

// TestRuntimeClockDeadlines advances request and idle deadlines without wall-clock sleeps.
// TestRuntimeClockDeadlines 不使用真实睡眠，推进请求及空闲期限。
func TestRuntimeClockDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name        string
		total, idle time.Duration
	}{
		{"total", time.Minute, 0}, {"idle", 0, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixtureRuntime(t)
			clock := codextest.NewClock()
			r.deps.Clock = clock
			ctx, finish, _, err := r.operation(t.Context(), tc.total, tc.idle)
			if err != nil {
				t.Fatal(err)
			}
			defer finish()
			<-clock.Created
			clock.Advance(time.Minute)
			<-ctx.Done()
			if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				t.Fatalf("cause=%v, want deadline exceeded", context.Cause(ctx))
			}
		})
	}
}
