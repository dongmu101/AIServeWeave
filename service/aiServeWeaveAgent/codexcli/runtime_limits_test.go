//go:build darwin || linux || windows

package codexcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestServerArgsSetFeaturesThroughConfig keeps feature switches on -c so a
// feature name a newer CLI dropped cannot stop app-server from starting, and
// keeps unbounded reconnects off.
// TestServerArgsSetFeaturesThroughConfig 确保功能开关经 -c 设置，使新版 CLI 删掉的功能名
// 不会让 app-server 无法启动，并保持关闭无限重连。
func TestServerArgsSetFeaturesThroughConfig(t *testing.T) {
	for _, image := range []bool{false, true} {
		args := serverArgs("/tmp/isolated", image)
		if slices.Contains(args, "--disable") || slices.Contains(args, "--enable") {
			t.Fatalf("args=%q, want features set only through -c", args)
		}
		for _, want := range []string{"features.unbounded_connection_retries=false", "features.shell_tool=false", "features.skip_host_skill_discovery=true", "features.sleep_tool=false", "features.computer_use=false"} {
			if !slices.Contains(args, want) {
				t.Fatalf("image=%t args=%q, want %s", image, args, want)
			}
		}
		if got := slices.Contains(args, "features.image_generation=false"); got == image {
			t.Fatalf("image=%t args=%q, want image_generation disabled only for chat", image, args)
		}
	}
}

// TestRuntimeLongStreamIsNotCut streams far more frames than a turn of
// ordinary length, which a frame count used to cut off.
// TestRuntimeLongStreamIsNotCut 流式发送远多于普通长度回合的帧，以前按帧数设限会截断它。
func TestRuntimeLongStreamIsNotCut(t *testing.T) {
	const deltas = 25000
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	send := func(v any) { _ = enc.Encode(v) }
	send(map[string]any{"id": 1, "result": map[string]any{}})
	send(map[string]any{"id": 2, "result": map[string]any{"account": map[string]string{"type": "chatgpt"}}})
	send(map[string]any{"id": 3, "result": map[string]any{"thread": map[string]string{"id": "thread1"}, "model": "fixture-model", "instructionSources": []string{}}})
	send(map[string]any{"id": 4, "result": map[string]any{}})
	send(map[string]any{"id": 5, "result": map[string]any{"turn": map[string]string{"id": "turn1"}}})
	for i := 0; i < deltas; i++ {
		send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread1", "turnId": "turn1", "itemId": "msg1", "delta": "x"}})
	}
	send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread1", "turn": map[string]string{"id": "turn1", "status": "completed"}}})
	scanner := bufio.NewScanner(&b)
	scanner.Buffer(make([]byte, 4096), maxFrameBytes+1)
	p := &protocol{scanner: scanner, output: io.Discard}
	req := chatRequest()
	input, err := prepareChat(req)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	finish := ""
	err = runChat(p, "/tmp/isolated", req, input, nil, func(e runtime.ChatEvent) error {
		text.WriteString(e.Delta.Content)
		if e.FinishReason != "" {
			finish = e.FinishReason
		}
		return nil
	})
	if err != nil || text.Len() != deltas || finish != "stop" {
		t.Fatalf("error=%v text=%d finish=%q, want nil, %d and stop", err, text.Len(), finish, deltas)
	}
}

// TestRetryNoticeIsNotActivity keeps app-server's own retry notices from
// resetting the idle deadline, while every other frame still does.
// TestRetryNoticeIsNotActivity 确保 app-server 自己的重试通知不会重置空闲期限，其余帧照旧重置。
func TestRetryNoticeIsNotActivity(t *testing.T) {
	frames := strings.Join([]string{
		`{"method":"error","params":{"threadId":"thread1","turnId":"turn1","willRetry":true,"error":{"message":"Reconnecting..."}}}`,
		`{"method":"error","params":{"threadId":"thread1","turnId":"turn1","willRetry":false,"error":{"message":"failed"}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread1","turnId":"turn1","itemId":"msg1","delta":"x"}}`,
	}, "\n") + "\n"
	touches := 0
	p := &protocol{scanner: bufio.NewScanner(strings.NewReader(frames)), output: io.Discard, activity: func() { touches++ }}
	// The first frame is a retry notice; each later frame adds one touch.
	// 第一帧是重试通知；之后每帧增加一次计数。
	for want := 0; want < 3; want++ {
		if _, err := p.read(); err != nil {
			t.Fatal(err)
		}
		if touches != want {
			t.Fatalf("frame %d: touches=%d, want %d", want, touches, want)
		}
	}
}

// TestTurnFailureClassification answers request errors with 400 outside the
// breaker, overload as a retryable busy node, and anything else as before.
// TestTurnFailureClassification 把请求错误以不计入熔断的 400 应答，过载作为可重试的繁忙节点，
// 其余照旧。
func TestTurnFailureClassification(t *testing.T) {
	r := fixtureRuntime(t)
	for _, tc := range []struct {
		info      string
		code      runtime.ErrorCode
		retryable bool
	}{
		{`"contextWindowExceeded"`, runtime.ErrorInvalidConfig, false},
		{`"badRequest"`, runtime.ErrorInvalidConfig, false},
		{`"cyberPolicy"`, runtime.ErrorInvalidConfig, false},
		{`"misalignmentPolicyViolation"`, runtime.ErrorInvalidConfig, false},
		{`"serverOverloaded"`, runtime.ErrorBackpressure, true},
		{`"internalServerError"`, runtime.ErrorUpstream, false},
		{`{"responseStreamDisconnected":{"httpStatusCode":null}}`, runtime.ErrorUpstream, false},
	} {
		t.Run(tc.info, func(t *testing.T) {
			err := turnFailure(nil, json.RawMessage(`{"message":"SECRET","codexErrorInfo":`+tc.info+`}`))
			if !errors.Is(err, ErrTurn) {
				t.Fatalf("error=%v, want it to remain an ErrTurn", err)
			}
			var rtErr *runtime.RuntimeError
			if !errors.As(r.wrap("chat_stream", err), &rtErr) || rtErr.Code != tc.code || rtErr.Retryable != tc.retryable || strings.Contains(rtErr.Message, "SECRET") {
				t.Fatalf("wrapped=%+v, want code %s retryable=%t without upstream text", rtErr, tc.code, tc.retryable)
			}
		})
	}
}

// TestRuntimeInjectsLargeHistory sends a history several times the old 1 MiB
// bound through a real subprocess.
// TestRuntimeInjectsLargeHistory 经真实子进程发送数倍于旧 1 MiB 上限的历史。
func TestRuntimeInjectsLargeHistory(t *testing.T) {
	r := fixtureRuntime(t)
	req := chatRequest()
	req.Messages = append([]runtime.ChatMessage{{Role: "user", Content: strings.Repeat("x", 3*maxFrameBytes)}, {Role: "assistant", Content: "noted"}}, req.Messages...)
	response, err := r.Chat(t.Context(), req)
	if err != nil || response.Message.Content != "Hello world" {
		t.Fatalf("response=%+v error=%v, want Hello world", response, err)
	}
}

// TestHealthChecksLoginOnly keeps Health off the model catalogue, which
// Discover still reads and reports.
// TestHealthChecksLoginOnly 确保 Health 不读取模型目录；目录仍由 Discover 读取并报告。
func TestHealthChecksLoginOnly(t *testing.T) {
	r := fixtureRuntime(t)
	r.env = append(r.env, "AISW_CODEX_CATALOG_FAILS=1")
	if report, err := r.Health(t.Context()); err != nil || report.State != runtime.StateHealthy {
		t.Fatalf("report=%+v error=%v, want healthy without a catalogue read", report, err)
	}
	if _, err := r.Discover(t.Context()); err == nil {
		t.Fatal("discover succeeded, want the catalogue failure reported")
	}
}

// TestBackendFailuresAre502 reports a broken stream, an unadmitted item and
// unapproved instructions as backend failures, like the Claude runtime.
// TestBackendFailuresAre502 把流损坏、未放行的条目与未获准的指令报告为后端失败，与 Claude 运行时一致。
func TestBackendFailuresAre502(t *testing.T) {
	r := fixtureRuntime(t)
	for _, err := range []error{ErrProtocol, ErrUnexpectedTool, ErrIsolation} {
		var rtErr *runtime.RuntimeError
		if !errors.As(r.wrap("chat_stream", err), &rtErr) || rtErr.Code != runtime.ErrorUpstream {
			t.Fatalf("%v wrapped=%+v, want upstream_error", err, rtErr)
		}
	}
	var rtErr *runtime.RuntimeError
	if !errors.As(r.wrap("chat_stream", ErrConfig), &rtErr) || rtErr.Code != runtime.ErrorInvalidConfig {
		t.Fatalf("wrapped=%+v, want an invalid request to stay invalid_config", rtErr)
	}
}

// TestFailedServerReportsStderrCategory names the fixed category of what a
// failed app-server wrote to standard error, never the text.
// TestFailedServerReportsStderrCategory 指明失败的 app-server 写入标准错误内容的固定分类，从不包含文本。
func TestFailedServerReportsStderrCategory(t *testing.T) {
	r := fixtureRuntime(t)
	r.env = append(r.env, "AISW_CODEX_STDERR_FIXTURE=1")
	_, err := r.Chat(t.Context(), chatRequest())
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || !strings.Contains(rtErr.Message, "(cli_stderr: login_required)") || strings.Contains(rtErr.Error(), "PRIVATE_MARKER") {
		t.Fatalf("error=%v, want the login_required category without the text", err)
	}
	if _, err := r.Health(t.Context()); err == nil || !strings.Contains(err.Error(), "cli_stderr: login_required") {
		t.Fatalf("health error=%v, want the category in the health summary", err)
	}
}

// TestFunctionCallOutputItemIsAdmitted admits the item that records a tool
// result, which runs nothing on this machine.
// TestFunctionCallOutputItemIsAdmitted 放行记录工具结果的条目，它不在本机执行任何操作。
func TestFunctionCallOutputItemIsAdmitted(t *testing.T) {
	frame := `{"method":"item/completed","params":{"threadId":"thread1","turnId":"turn1","item":{"type":"functionCallOutput","id":"i1"}}}` + "\n"
	p := &protocol{scanner: bufio.NewScanner(strings.NewReader(frame)), output: io.Discard}
	if _, err := p.read(); err != nil {
		t.Fatalf("error=%v, want functionCallOutput admitted", err)
	}
	frame = `{"method":"item/started","params":{"threadId":"thread1","turnId":"turn1","item":{"type":"commandExecution","id":"i2"}}}` + "\n"
	p = &protocol{scanner: bufio.NewScanner(strings.NewReader(frame)), output: io.Discard}
	if _, err := p.read(); !errors.Is(err, ErrUnexpectedTool) {
		t.Fatalf("error=%v, want local command execution refused", err)
	}
}
