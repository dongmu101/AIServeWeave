package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestSafeguardsCompaction(t *testing.T) {
	for _, tc := range []struct {
		name, stop    string
		stream, delta bool
	}{
		{"threshold JSON", "end_turn", false, true},
		{"threshold SSE", "end_turn", true, true},
		{"paused threshold JSON", "compaction", false, true},
		{"on demand JSON", "compaction", false, false},
		{"on demand SSE", "compaction", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newMessagesBridge("unused", "sonnet", map[string]principal{"fixture": {"t", "k"}}, runtime.NewSystemClock())
			defer b.close()
			b.start = func(s *messagesSession) {
				var input bytes.Buffer
				emitFixture(&input, map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_compact", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
				block := map[string]any{"type": "compaction", "content": "summary", "signature": "original-signature", "future": true}
				if tc.delta {
					block["content"] = nil
				}
				emitFixture(&input, map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
				if tc.delta {
					emitFixture(&input, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "compaction_delta", "content": "summary", "encrypted_content": "opaque-summary"}})
				}
				emitFixture(&input, map[string]any{"type": "content_block_stop", "index": 0})
				emitFixture(&input, map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": tc.stop}, "usage": map[string]int{"output_tokens": 7}})
				emitFixture(&input, map[string]string{"type": "message_stop"})
				input.WriteString("{\"type\":\"result\",\"subtype\":\"success\"}\n")
				final, err := bridgeEvents(&input, s)
				if errors.Is(err, errTerminalStop) {
					err = nil
				}
				_ = s.send(final, err)
			}
			raw := `{"model":"sonnet","max_tokens":1024,"messages":[{"role":"user","content":"summarize"}],"safeguards":[{"type":"dangerous_tool_use","classifier_context":{}}]}`
			var doc map[string]any
			_ = json.Unmarshal([]byte(raw), &doc)
			doc["stream"] = tc.stream
			if !tc.delta {
				doc["compaction"] = map[string]string{"type": "summarize"}
			}
			data, _ := json.Marshal(doc)
			r := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(data))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Api-Key", "fixture")
			w := httptest.NewRecorder()
			b.ServeHTTP(w, r)
			if w.Code != 200 || strings.Contains(w.Body.String(), "event: error") {
				t.Fatalf("compaction failed: %d %s", w.Code, w.Body.String())
			}
			if tc.stream {
				for _, want := range []string{`"type":"compaction"`, `"signature":"original-signature"`, `"stop_reason":"` + tc.stop + `"`} {
					if !strings.Contains(w.Body.String(), want) {
						t.Fatalf("missing %s: %s", want, w.Body.String())
					}
				}
				return
			}
			var response struct {
				Stop    string           `json:"stop_reason"`
				Content []map[string]any `json:"content"`
			}
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Content) != 1 {
				t.Fatalf("invalid compaction response: %s", w.Body.String())
			}
			block := response.Content[0]
			if response.Stop != tc.stop || block["content"] != "summary" || block["signature"] != "original-signature" || block["future"] != true || (tc.delta && block["encrypted_content"] != "opaque-summary") {
				t.Fatalf("compaction lost native fields: %s", w.Body.String())
			}
		})
	}
}
