package claudecode

import (
	"encoding/json"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestMessagesImageValidation accepts inline images without fetching caller URLs.
// TestMessagesImageValidation 接受内联图片，不抓取调用方提供的 URL。
func TestMessagesImageValidation(t *testing.T) {
	for _, tc := range []struct {
		name, role, content string
		valid               bool
	}{
		{"PNG", "user", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]`, true},
		{"JPEG", "user", `[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"aW1hZ2U="}}]`, true},
		{"GIF", "user", `[{"type":"image","source":{"type":"base64","media_type":"image/gif","data":"aW1hZ2U="}}]`, true},
		{"WebP", "user", `[{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"aW1hZ2U="}}]`, true},
		{"tool result", "user", `[{"type":"tool_result","tool_use_id":"call","content":[{"type":"text","text":"screenshot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]`, true},
		{"missing source", "user", `[{"type":"image"}]`, false},
		{"empty data", "user", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]`, false},
		{"bad base64", "user", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"***"}}]`, false},
		{"wrong MIME", "user", `[{"type":"image","source":{"type":"base64","media_type":"text/plain","data":"aW1hZ2U="}}]`, false},
		{"URL", "user", `[{"type":"image","source":{"type":"url","url":"http://127.0.0.1/private"}}]`, false},
		{"assistant image", "assistant", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]`, false},
		{"image disguised as text", "user", `[{"type":"text","text":"hello","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]`, false},
		{"nested invalid image", "user", `[{"type":"tool_result","tool_use_id":"call","content":[{"type":"image"}]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testMessagesRequest()
			req.Messages = []bridgeMessage{{Role: tc.role, Content: json.RawMessage(tc.content)}}
			if err := req.validate(req.Model); (err == nil) != tc.valid {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
		})
	}
}

// TestMessagesImageContinuation rejects altered images before delivering tools.
// TestMessagesImageContinuation 在投递工具结果前拒绝被改动的历史图片。
func TestMessagesImageContinuation(t *testing.T) {
	b := newMessagesBridge("unused", "sonnet", map[string]principal{"key": {"tenant", "key"}}, runtime.NewSystemClock())
	b.start = fixtureStart
	defer b.close()
	req := testMessagesRequest()
	req.Messages[0].Content = json.RawMessage(`[{"type":"text","text":"use caller tool"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"b25l"}}]`)
	req = continuation(t, req, sendMessages(b, req, "key"), "caller-result")
	changed := req
	changed.Messages = append([]bridgeMessage(nil), req.Messages...)
	changed.Messages[0].Content = json.RawMessage(`[{"type":"text","text":"use caller tool"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"dHdv"}}]`)
	if w := sendMessages(b, changed, "key"); w.Code != 409 {
		t.Fatalf("changed image status=%d, want 409", w.Code)
	}
	if w := sendMessages(b, req, "key"); w.Code != 200 {
		t.Fatalf("original history status=%d, want 200", w.Code)
	}
}

// TestImageHistoryIntegrity keeps image bytes in continuation authentication.
// TestImageHistoryIntegrity 确保图片字节参与续接历史校验。
func TestImageHistoryIntegrity(t *testing.T) {
	history := func(data string) []bridgeMessage {
		return []bridgeMessage{{Role: "user", Content: json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]`)}}
	}
	if historyHash(history("b25l")) == historyHash(history("dHdv")) {
		t.Fatal("different images must not authenticate as the same history")
	}
}
