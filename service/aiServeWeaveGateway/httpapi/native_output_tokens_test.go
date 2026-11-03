package httpapi

import (
	"encoding/json"
	"testing"
)

// TestNativeChatOutputTokens preserves positive client limits for both token fields.
// TestNativeChatOutputTokens 保留客户端通过两个 token 字段指定的正整数输出上限。
func TestNativeChatOutputTokens(t *testing.T) {
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		t.Run(field, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				tokens    int
				wantError bool
			}{
				{"negative", -1, true},
				{"zero", 0, true},
				{"minimum", 1, false},
				{"above original maximum", 64001, false},
				{"previous maximum", 128000, false},
				{"above previous maximum", 128001, false},
				{"large client limit", 1000000, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := chatCompletionRequest{Model: "real-model", Messages: []chatMessageJSON{{Role: "user", Content: chatContentJSON{Text: "hello"}}}}
					if field == "max_tokens" {
						req.MaxTokens = &tc.tokens
					} else {
						req.MaxCompletionTokens = &tc.tokens
					}
					native, err := req.toNativeMessages()
					if (err != nil) != tc.wantError {
						t.Fatalf("%s=%d: error=%v, want error=%t", field, tc.tokens, err, tc.wantError)
					}
					if err == nil && native.MaxTokens != tc.tokens {
						t.Fatalf("max_tokens=%d, want %d", native.MaxTokens, tc.tokens)
					}
				})
			}
		})
	}
}

// TestNativeResponsesOutputTokens preserves positive client limits through conversion to Messages.
// TestNativeResponsesOutputTokens 在转换为 Messages 的全程保留客户端指定的正整数输出上限。
func TestNativeResponsesOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tokens    int
		wantError bool
	}{
		{"negative", -1, true},
		{"zero", 0, true},
		{"minimum", 1, false},
		{"above original maximum", 64001, false},
		{"previous maximum", 128000, false},
		{"above previous maximum", 128001, false},
		{"large client limit", 1000000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := responsesRequest{Model: "real-model", Input: json.RawMessage(`"hello"`), MaxOutputTokens: &tc.tokens}
			canonical, _, err := req.toRuntime()
			if err != nil {
				t.Fatalf("toRuntime error=%v, want nil", err)
			}
			chat, err := req.toNativeChat(canonical)
			var native anthropicMessagesRequest
			if err == nil {
				native, err = chat.toNativeMessages()
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("max_output_tokens=%d: error=%v, want error=%t", tc.tokens, err, tc.wantError)
			}
			if err == nil && native.MaxTokens != tc.tokens {
				t.Fatalf("max_tokens=%d, want %d", native.MaxTokens, tc.tokens)
			}
		})
	}
}
