package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestMessagesCapabilityRejection(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"context management", "API Error: 400 context_management: Extra inputs are not permitted PRIVATE_MARKER", "context_management: Extra inputs are not permitted"},
		{"adaptive thinking", "API Error: 400 thinking.type: adaptive is not supported PRIVATE_MARKER", "capability_rejected: thinking_type:adaptive"},
		{"enabled thinking", "API Error: 400 thinking.type: enabled is not supported PRIVATE_MARKER", "capability_rejected: thinking_type:enabled"},
		{"thinking signature", "API Error: 400 Invalid signature in thinking block PRIVATE_MARKER", "capability_rejected: thinking_signature"},
		{"thinking binding", "API Error: 400 thinking block is bound to a different conversation PRIVATE_MARKER", "capability_rejected: thinking_signature"},
		{"effort", "API Error: 400 This model does not support the effort parameter PRIVATE_MARKER", "capability_rejected: effort_unsupported"},
		{"format", "API Error: 400 output_config.format: Extra inputs are not permitted PRIVATE_MARKER", "capability_rejected: structured_outputs_unsupported"},
		{"mid system", `API Error: 400 Unexpected role "system" PRIVATE_MARKER`, "capability_rejected: mid_conv_system"},
		{"beta", "API Error: 400 Unexpected value(s) future-feature-2099-01-01 for anthropic-beta PRIVATE_MARKER", "capability_rejected: beta_header:future-feature-2099-01-01"},
		{"beta prefix collision", "API Error: 400 Unexpected value(s) future-feature-2099-01-01-other for anthropic-beta PRIVATE_MARKER", ""},
		{"canonical token", "API Error: 400 capability_rejected: thinking_signature", "capability_rejected: thinking_signature"},
		{"JSON error", `API Error: 400 {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long PRIVATE_MARKER"}}`, "capability_rejected: prompt_too_long"},
		{"413 context", "API Error: 413 input exceeds the model context window PRIVATE_MARKER", "capability_rejected: prompt_too_long"},
		{"CLI formatted prompt", "Prompt is too long · this conversation is a single exchange and cannot be compacted PRIVATE_MARKER", "capability_rejected: prompt_too_long"},
		{"unknown beta", "API Error: 400 Unexpected value(s) private-beta for anthropic-beta PRIVATE_MARKER", ""},
		{"beta mentioned without rejection", "API Error: 400 request with anthropic-beta future-feature-2099-01-01 failed PRIVATE_MARKER", ""},
		{"unknown error", "API Error: 400 PRIVATE_MARKER", ""},
		{"server failure", "API Error: 500 context_management: Extra inputs are not permitted PRIVATE_MARKER", ""},
		{"policy blocked", `API Error: 400 {"type":"error","error":{"type":"policy_blocked","message":"prompt is too long PRIVATE_MARKER"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input bytes.Buffer
			status := 0
			if tc.name == "CLI formatted prompt" {
				status = 400
			}
			_ = json.NewEncoder(&input).Encode(map[string]any{"type": "assistant", "error": "unknown", "is_api_error_message": true, "message": map[string]any{"model": "<synthetic>", "content": []any{map[string]string{"type": "text", "text": tc.text}}}, "api_error_status": status})
			input.WriteString("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":true}\n")
			req := guardedRequest()
			req.betaFeatures = "future-feature-2099-01-01"
			s := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 4)}
			_, cause := bridgeEvents(&input, s)
			var logs bytes.Buffer
			r := &Runtime{cfg: runtime.Config{ID: "fixture"}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			public := r.roundError(cause)
			var failure *runtime.RuntimeError
			if !errors.As(public, &failure) {
				t.Fatalf("unexpected error: %v", public)
			}
			if tc.want != "" && (failure.Code != runtime.ErrorCapability || failure.Message != tc.want || failure.Retryable) {
				t.Fatalf("public rejection=%+v, want non-retryable capability %q", failure, tc.want)
			}
			if tc.want == "" && failure.Code != runtime.ErrorUpstream {
				t.Fatalf("unknown failure became a capability rejection: %+v", failure)
			}
			if strings.Contains(logs.String()+public.Error()+cause.Error(), "PRIVATE_MARKER") {
				t.Fatal("upstream payload escaped the classifier")
			}
		})
	}
}

func TestMessagesRejectionIgnoresOrdinaryAssistant(t *testing.T) {
	var input bytes.Buffer
	_ = json.NewEncoder(&input).Encode(map[string]any{"type": "assistant", "message": map[string]any{"model": "real-model", "content": []any{map[string]string{"type": "text", "text": "API Error: 400 prompt is too long"}}}})
	fixtureRound(&input, "", "answer")
	input.WriteString("{\"type\":\"result\",\"subtype\":\"success\"}\n")
	s := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 16)}
	if _, err := bridgeEvents(&input, s); err != nil {
		t.Fatalf("ordinary assistant text classified as an API error: %v", err)
	}
}

func TestMessagesRejectionRequiresFailedAPIRound(t *testing.T) {
	for _, tc := range []struct {
		name              string
		marked, recovered bool
		status            int
	}{
		{"unmarked synthetic message", false, false, 400},
		{"status conflicts with text", true, false, 500},
		{"CLI recovered internally", true, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input bytes.Buffer
			_ = json.NewEncoder(&input).Encode(map[string]any{"type": "assistant", "error": "unknown", "is_api_error_message": tc.marked, "api_error_status": tc.status, "message": map[string]any{"model": "<synthetic>", "content": []any{map[string]string{"type": "text", "text": "API Error: 400 prompt is too long"}}}})
			if tc.recovered {
				fixtureRound(&input, "", "answer")
				input.WriteString("{\"type\":\"result\",\"subtype\":\"success\"}\n")
			} else {
				input.WriteString("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":true}\n")
			}
			s := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 16)}
			_, err := bridgeEvents(&input, s)
			if tc.recovered && err != nil {
				t.Fatalf("recovered round failed: %v", err)
			}
			if !tc.recovered && !errors.Is(err, errCLIResult) {
				t.Fatalf("unverified error became a rejection: %v", err)
			}
		})
	}
}

// TestLoginRefreshBusyIsRetryable reports a login refresh another process
// holds as a retryable busy node in bridged and native rounds, whether the
// CLI says so in a synthetic message, in its result, or ends without one,
// and never matches the same words in model output.
// TestLoginRefreshBusyIsRetryable 在桥接与原生回合中，把另一个进程正在刷新登录报告为可重试的
// 繁忙节点；无论 CLI 在合成消息、结果中说明，还是没有结果就结束；模型输出中的相同字句从不匹配。
func TestLoginRefreshBusyIsRetryable(t *testing.T) {
	const notice = "Failed to refresh OAuth token: another Claude Code process is refreshing it or exited mid-refresh. PRIVATE_MARKER"
	synthetic := `{"type":"assistant","error":"authentication_failed","is_api_error_message":true,"message":{"model":"<synthetic>","content":[{"type":"text","text":"` + notice + `"}]}}` + "\n"
	result := `{"type":"result","subtype":"success","is_error":true,"result":"` + notice + `"}` + "\n"
	plainResult := `{"type":"result","subtype":"success","is_error":true,"result":"PRIVATE_MARKER"}` + "\n"
	modelText := `{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"` + notice + `"}]}}` + "\n"
	for _, tc := range []struct {
		name, wire string
		want       error
	}{
		{"synthetic message then result", synthetic + plainResult, errLoginRefreshBusy},
		{"result only", result, errLoginRefreshBusy},
		{"synthetic message without result", synthetic, errLoginRefreshBusy},
		{"model output is not a CLI notice", modelText + plainResult, errCLIResult},
	} {
		for _, request := range []messagesRequest{testMessagesRequest(), guardedRequest()} {
			t.Run(tc.name+"/native="+map[bool]string{true: "yes", false: "no"}[request.isNative()], func(t *testing.T) {
				s := &messagesSession{ctx: context.Background(), request: request, events: make(chan bridgeEvent, 4)}
				_, cause := bridgeEvents(strings.NewReader(tc.wire), s)
				if !errors.Is(cause, tc.want) {
					t.Fatalf("error=%v, want %v", cause, tc.want)
				}
				if tc.want != errLoginRefreshBusy {
					return
				}
				var logs bytes.Buffer
				r := &Runtime{cfg: runtime.Config{ID: "fixture"}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
				var failure *runtime.RuntimeError
				if !errors.As(r.roundError(cause), &failure) || failure.Code != runtime.ErrorBackpressure || !failure.Retryable {
					t.Fatalf("public=%+v, want retryable backpressure", failure)
				}
				var record map[string]any
				if json.Unmarshal(logs.Bytes(), &record) != nil || record["reason"] != "login_refresh_busy" {
					t.Fatalf("diagnostic=%s, want reason login_refresh_busy", &logs)
				}
				if strings.Contains(logs.String()+failure.Error(), "PRIVATE_MARKER") {
					t.Fatal("CLI text reached logs or caller")
				}
			})
		}
	}
}

// TestProbeEnvSetsCLIDefaults sets the Agent's fixed CLI variables even when
// the inherited environment carries other values, which filtering drops.
// TestProbeEnvSetsCLIDefaults 即使继承环境带有其他值（过滤会丢掉它们），也设置 Agent 固定的 CLI 变量。
func TestProbeEnvSetsCLIDefaults(t *testing.T) {
	env := probeEnv([]string{"PATH=/bin", "DISABLE_AUTOUPDATER=0", "CLAUDE_CODE_OAUTH_401_WAIT_MS=1", "ANTHROPIC_API_KEY=PRIVATE_MARKER"})
	want := []string{"PATH=/bin", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_OAUTH_401_WAIT_MS=30000"}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env=%q, want %q", env, want)
	}
}
