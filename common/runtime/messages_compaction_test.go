package runtime_test

import (
	"encoding/json"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestNativeCompactionSummaryValidation(t *testing.T) {
	for _, tc := range []struct {
		name, block, delta, want string
		invalid                  bool
	}{
		{"summary", `{"type":"compaction","signature":"signed"}`, `{"type":"compaction_delta","content":"summary","encrypted_content":"opaque"}`, `"summary"`, false},
		{"no summary", `{"type":"compaction","signature":"signed"}`, `{"type":"compaction_delta","content":null}`, `null`, false},
		{"escaped type", `{"type":"compac\u0074ion","signature":"signed"}`, `{"type":"compac\u0074ion_delta","content":"summary"}`, `"summary"`, false},
		{"wrong block", `{"type":"text"}`, `{"type":"compaction_delta","content":"summary"}`, "", true},
		{"malformed summary", `{"type":"compaction"}`, `{"type":"compaction_delta","content":{"private":"payload"}}`, "", true},
		{"malformed encrypted payload", `{"type":"compaction"}`, `{"type":"compaction_delta","encrypted_content":42}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var block map[string]json.RawMessage
			_ = json.Unmarshal([]byte(tc.block), &block)
			err := runtime.MergeMessagesCompaction(block, json.RawMessage(`{"delta":`+tc.delta+`}`))
			if (err != nil) != tc.invalid {
				t.Fatalf("validation error=%v, invalid=%t", err, tc.invalid)
			}
			if !tc.invalid && (string(block["content"]) != tc.want || string(block["signature"]) != `"signed"`) {
				t.Fatalf("summary or signature changed: %v", block)
			}
		})
	}
}
