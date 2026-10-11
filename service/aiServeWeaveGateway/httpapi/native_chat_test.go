package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// TestNativeChatRejectsUnknownFields names known unsupported controls without echoing arbitrary input.
// TestNativeChatRejectsUnknownFields 点名已知但不支持的控制字段，不回显任意输入。
func TestNativeChatRejectsUnknownFields(t *testing.T) {
	for _, tc := range []struct{ name, extra, want string }{
		{"unknown field", `"sensitive_input":true`, "unsupported Claude CLI Chat parameter"},
		{"persistent storage", `"store":true`, "Claude CLI Chat supports only store=false; persistent Chat storage is unavailable"},
		{"strict tool schema", `"tools":[{"type":"function","function":{"name":"read","strict":false}}]`, "Claude CLI Chat does not support tools[].function.strict"},
		{"parallel tool control", `"parallel_tool_calls":false`, "Claude CLI Chat does not support parallel_tool_calls"},
		{"reasoning control", `"reasoning_effort":"high"`, "Claude CLI Chat does not support reasoning_effort"},
		{"cache key", `"prompt_cache_key":"sensitive_input"`, "Claude CLI Chat does not support prompt_cache_key or prompt_cache_retention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"model":"real-model","messages":[{"role":"user","content":"hello"}],` + tc.extra + `}`)
			var req chatCompletionRequest
			_ = json.Unmarshal(raw, &req)
			w := httptest.NewRecorder()
			(&handlers{}).nativeChat(w, httptest.NewRequest("POST", "/v1/chat/completions", nil), req, raw, time.Time{})
			var body openAIErrorBody
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != 400 || body.Error.Code != "unsupported_parameter" || body.Error.Message != tc.want {
				t.Fatalf("status=%d error=%+v, want 400, unsupported_parameter and message %q", w.Code, body.Error, tc.want)
			}
		})
	}
}

// TestNativeChatFailureIsNotSuccessfulCompletion checks JSON and post-header SSE errors.
// TestNativeChatFailureIsNotSuccessfulCompletion 验证 JSON 和已发出响应头后的 SSE 错误不会伪装成功。
func TestNativeChatFailureIsNotSuccessfulCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{{"JSON", false}, {"SSE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			o := &nativeChatOutput{w: w, model: "real-model"}
			if tc.stream {
				if err := o.chunk(chatMessageDeltaJSON{Role: "assistant"}, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			o.fail(503, "api_error", "session_unavailable", "session unavailable")
			if !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "[DONE]") || (!tc.stream && w.Code != 503) {
				t.Fatalf("status=%d body=%s, want explicit error without DONE", w.Code, w.Body.String())
			}
		})
	}
}

// TestNativeChatValidation checks unsupported semantics before any CLI is started.
// TestNativeChatValidation 验证不支持的语义在启动 CLI 前就被拒绝。
func TestNativeChatValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"text", `{"messages":[{"role":"user","content":"hello"}]}`, false},
		{"conversation", `{"messages":[{"role":"developer","content":"be concise"},{"role":"user","content":"remember 42"},{"role":"assistant","content":"OK"},{"role":"user","content":"what number?"}]}`, false},
		{"token alias", `{"max_completion_tokens":32,"messages":[{"role":"user","content":"hello"}]}`, false},
		{"conflicting limits", `{"max_tokens":32,"max_completion_tokens":33,"messages":[{"role":"user","content":"hello"}]}`, true},
		{"image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image"}}]}]}`, true},
		{"sampling", `{"top_p":0.5,"messages":[{"role":"user","content":"hello"}]}`, true},
		{"forced tool", `{"tool_choice":"required","messages":[{"role":"user","content":"hello"}]}`, true},
		{"invalid role", `{"messages":[{"role":"unknown","content":"hello"}]}`, true},
		{"late system", `{"messages":[{"role":"user","content":"hello"},{"role":"system","content":"new"}]}`, true},
		{"invalid arguments", `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"[]"}}]},{"role":"tool","tool_call_id":"call","content":"OK"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req chatCompletionRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			native, err := req.toNativeMessages()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, want error=%t", err, tc.wantError)
			}
			if tc.name == "conversation" && err == nil {
				var text string
				_ = json.Unmarshal(native.Messages[0].Content, &text)
				if len(native.Messages) != 1 || !strings.Contains(text, "remember 42") || !strings.Contains(text, `"role":"assistant"`) || string(native.System) != `"be concise"` {
					t.Fatalf("native=%+v, want complete role-labelled history and developer instruction", native)
				}
			}
		})
	}
}

// TestNativeChatStreamPreservesParallelCalls verifies indexes, empty arguments and usage framing.
// TestNativeChatStreamPreservesParallelCalls 验证并行工具索引、空参数和用量帧。
func TestNativeChatStreamPreservesParallelCalls(t *testing.T) {
	w := httptest.NewRecorder()
	o := &nativeChatOutput{w: w, model: "real-model", toolIndexes: map[int]int{}, toolArguments: map[int]bool{}, includeUsage: true}
	for _, event := range []string{
		`{"type":"message_start","message":{"id":"msg-test"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"checking"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"first","name":"read","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"second","name":"ping","input":{}}}`,
		`{"type":"content_block_stop","index":2}`,
	} {
		if err := o.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	o.finish(map[string]any{"id": "msg-test", "stop_reason": "tool_use"}, nil, runtime.Usage{PromptTokens: 3, CompletionTokens: 7, TotalTokens: 10}, true)
	var calls [2]functionCallJSON
	text, finish, usage := "", "", false
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.Model != "real-model" || chunk.ID != "msg-test" {
			t.Fatalf("chunk=%+v, want stable model and ID", chunk)
		}
		if chunk.Usage != nil {
			usage = len(chunk.Choices) == 0 && chunk.Usage.TotalTokens == 10
		}
		for _, choice := range chunk.Choices {
			text += choice.Delta.Content
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
			for _, call := range choice.Delta.ToolCalls {
				if call.Index == nil || *call.Index < 0 || *call.Index > 1 {
					t.Fatalf("index=%v, want 0 or 1", call.Index)
				}
				calls[*call.Index].Name += call.Function.Name
				calls[*call.Index].Arguments += call.Function.Arguments
			}
		}
	}
	if text != "checking" || finish != "tool_calls" || !usage || calls[0].Arguments != `{"path":"a"}` || calls[1].Arguments != "{}" || strings.Count(w.Body.String(), "data: [DONE]") != 1 {
		t.Fatalf("text=%q finish=%q usage=%t calls=%+v, want checking, tool_calls, true, exact tool JSON and one DONE", text, finish, usage, calls)
	}
}
