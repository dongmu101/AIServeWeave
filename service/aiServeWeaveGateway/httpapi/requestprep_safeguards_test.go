package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSafeguardsModelSuffixPreservesInference(t *testing.T) {
	for _, tc := range []struct {
		name, model, thinking string
		maxTokens             int
	}{
		{"adaptive", "claude-opus-5-5(high)", `{"type":"adaptive","display":"omitted"}`, 4096},
		{"disabled", "claude-opus-5-5(16384)", `{"type":"disabled"}`, 4096},
		{"small output", "claude-opus-5-5(high)[1m]", `{"type":"adaptive"}`, 128},
		{"omitted", "claude-opus-5-5(none)", "", 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]json.RawMessage{
				"model": json.RawMessage(`"` + tc.model + `"`), "max_tokens": json.RawMessage("4096"),
				"messages":   json.RawMessage(`[{"role":"user","content":"question"}]`),
				"safeguards": json.RawMessage(`[{"type":"dangerous_tool_use","classifier_context":{"opaque":"caller-policy"}}]`),
			}
			doc["max_tokens"], _ = json.Marshal(tc.maxTokens)
			if tc.thinking != "" {
				doc["thinking"] = json.RawMessage(tc.thinking)
			}
			raw, _ := json.Marshal(doc)
			_, rewritten, err := prepareRequest(httptest.NewRequest("POST", "/v1/messages", nil), raw, dialectAnthropic)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(rewritten, &got); err != nil {
				t.Fatal(err)
			}
			doc["model"] = json.RawMessage(`"claude-opus-5-5"`)
			if !reflect.DeepEqual(got, doc) {
				t.Fatalf("native inference changed: got %s, want only the routing model normalized", rewritten)
			}
		})
	}
}
