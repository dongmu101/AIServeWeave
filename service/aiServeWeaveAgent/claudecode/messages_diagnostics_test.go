package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestMessagesAdmissionDiagnostics exercises rejections before a generation round starts.
// TestMessagesAdmissionDiagnostics 覆盖进入生成回合之前的拒绝，不泄露请求内容。
func TestMessagesAdmissionDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, reason, changed string
		mutate                func(*messagesRequest)
		otherOwner            bool
		version, beta         string
		consumed              bool
	}{
		{name: "history", reason: "continuation_history_mismatch", mutate: func(r *messagesRequest) {
			r.Messages[0].Content = json.RawMessage(`"private-marker changed history"`)
		}},
		{name: "tools", reason: "continuation_config_mismatch", changed: "tools", mutate: func(r *messagesRequest) {
			r.Tools[0].Description = "private-marker changed tool"
		}},
		{name: "system", reason: "continuation_config_mismatch", changed: "system", mutate: func(r *messagesRequest) {
			r.System = json.RawMessage(`"private-marker system"`)
		}},
		{name: "output budget", reason: "continuation_config_mismatch", changed: "max_tokens", mutate: func(r *messagesRequest) { r.MaxTokens++ }},
		{name: "extra user text", reason: "continuation_extra_content", mutate: func(r *messagesRequest) {
			var list []messageBlock
			_ = json.Unmarshal(r.Messages[2].Content, &list)
			list = append(list, messageBlock{Type: "text", Text: "private-marker reminder"})
			r.Messages[2].Content, _ = json.Marshal(list)
		}},
		{name: "duplicate tool result", reason: "duplicate_tool_result", mutate: func(r *messagesRequest) {
			var list []messageBlock
			_ = json.Unmarshal(r.Messages[2].Content, &list)
			r.Messages[2].Content, _ = json.Marshal(append(list, list[0]))
		}},
		{name: "identity", reason: "continuation_owner_mismatch", otherOwner: true},
		{name: "version", reason: "continuation_config_mismatch", changed: "api_version", version: "2023-06-01"},
		{name: "beta and altered history", reason: "continuation_config_mismatch", changed: "beta_features", beta: "future-feature", mutate: func(r *messagesRequest) {
			r.Messages[0].Content = json.RawMessage(`"private-marker changed history"`)
		}},
		{name: "consumed", reason: "continuation_consumed", consumed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{
				Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			defer r.Close()
			r.models = map[string]struct{}{"sonnet": {}}
			r.bridge.keys = map[string]principal{"first": {"tenant", "key"}}
			r.bridge.start = fixtureStart
			req := testMessagesRequest()
			req.Messages[0].Content = json.RawMessage(`"private-marker use the caller tool"`)
			valid := continuation(t, req, sendMessages(r.bridge, req, "first"), "private-marker tool output")
			if tc.consumed {
				if w := sendMessages(r.bridge, valid, "first"); w.Code != 200 {
					t.Fatalf("valid round failed: %d", w.Code)
				}
			}
			data, _ := json.Marshal(valid)
			var changed messagesRequest
			_ = json.Unmarshal(data, &changed)
			if tc.mutate != nil {
				tc.mutate(&changed)
			}
			data, _ = json.Marshal(changed)
			request := runtime.MessagesRequest{Model: "sonnet", TenantID: "tenant", KeyID: "key", JSON: data, APIVersion: tc.version, BetaFeatures: tc.beta}
			if tc.otherOwner {
				request.KeyID = "private-marker-other-owner"
			}
			_, err = r.Messages(t.Context(), request)
			var failure *runtime.RuntimeError
			if !errors.As(err, &failure) || failure.Code != runtime.ErrorInvalidConfig {
				t.Fatalf("error = %v, want invalid_config", err)
			}
			var record struct {
				Message string   `json:"msg"`
				Runtime string   `json:"runtime_id"`
				Reason  string   `json:"reason"`
				Changes []string `json:"config_changes"`
			}
			if json.Unmarshal(logs.Bytes(), &record) != nil || record.Message != "Claude Messages request rejected" || record.Runtime != "local" || record.Reason != tc.reason {
				t.Fatalf("diagnostic = %s, want runtime local, reason %s", &logs, tc.reason)
			}
			if tc.changed != "" && (len(record.Changes) != 1 || record.Changes[0] != tc.changed) {
				t.Fatalf("config_changes = %v, want [%s]", record.Changes, tc.changed)
			}
			if failure.RuntimeID != "local" || strings.Contains(logs.String()+failure.Error(), "private-marker") {
				t.Fatalf("rejection lost runtime identity or leaked request data")
			}
			if tc.consumed {
				return
			}
			// A diagnostic must not advance the pending tool call.
			// 诊断不能消费待决工具结果；原始请求仍可正常续接。
			data, _ = json.Marshal(valid)
			request.KeyID, request.JSON = "key", data
			request.APIVersion, request.BetaFeatures = "", ""
			stream, err := r.Messages(t.Context(), request)
			if err != nil {
				t.Fatalf("valid continuation after rejection: %v", err)
			}
			defer stream.Close()
			for {
				_, err = stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMessagesPreflightDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		code         runtime.ErrorCode
		mutate       func(*runtime.MessagesRequest)
	}{
		{"principal", "invalid_principal", runtime.ErrorInvalidConfig, func(r *runtime.MessagesRequest) { r.KeyID = "" }},
		{"protocol", "invalid_protocol_selectors", runtime.ErrorCapability, func(r *runtime.MessagesRequest) { r.APIVersion = "private-marker\n" }},
		{"JSON", "unsupported_request_shape", runtime.ErrorCapability, func(r *runtime.MessagesRequest) { r.JSON = json.RawMessage(`{"private-marker":"unsupported"}`) }},
		{"model", "unknown_model", runtime.ErrorCapability, func(r *runtime.MessagesRequest) { r.Model = "private-marker-unknown" }},
		{"config", "unsupported_request_config", runtime.ErrorCapability, func(r *runtime.MessagesRequest) {
			req := testMessagesRequest()
			req.MaxTokens = 0
			r.JSON, _ = json.Marshal(req)
		}},
		{"assistant end", "last_message_not_user", runtime.ErrorInvalidConfig, func(r *runtime.MessagesRequest) {
			req := testMessagesRequest()
			req.Messages[0].Role = "assistant"
			r.JSON, _ = json.Marshal(req)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{
				Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			defer r.Close()
			r.models = map[string]struct{}{"sonnet": {}}
			data, _ := json.Marshal(testMessagesRequest())
			req := runtime.MessagesRequest{Model: "sonnet", TenantID: "tenant", KeyID: "key", JSON: data}
			tc.mutate(&req)
			_, err = r.Messages(t.Context(), req)
			var failure *runtime.RuntimeError
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.RuntimeID != "local" {
				t.Fatalf("failure = %v, want runtime local, code %s", err, tc.code)
			}
			var record map[string]any
			if json.Unmarshal(logs.Bytes(), &record) != nil || record["reason"] != tc.reason {
				t.Fatalf("diagnostic = %s, want reason %s", &logs, tc.reason)
			}
			if strings.Contains(logs.String()+failure.Error(), "private-marker") {
				t.Fatal("request data leaked")
			}
		})
	}
}
