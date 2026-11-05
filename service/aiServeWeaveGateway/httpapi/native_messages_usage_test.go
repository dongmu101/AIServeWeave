package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func TestNativeCompactionUsageIsChargedAndLedgered(t *testing.T) {
	for _, tc := range []struct {
		name, raw     string
		input, output int
	}{
		{"threshold", `{"input_tokens":3,"output_tokens":7,"iterations":[{"type":"compaction","input_tokens":100,"output_tokens":20},{"type":"message","input_tokens":3,"output_tokens":7}]}`, 103, 27},
		{"on demand", `{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"compaction","input_tokens":100,"output_tokens":20}]}`, 100, 20},
		{"ordinary", `{"input_tokens":3,"output_tokens":7,"iterations":null}`, 3, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]any
			_ = json.Unmarshal([]byte(tc.raw), &raw)
			usage, err := nativeMessagesBilledUsage(raw)
			if err != nil {
				t.Fatal(err)
			}
			ledger, sink := &fakeUsageLedgerSink{}, &usageSink{}
			h := &handlers{usageLedger: ledger, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
			ctx := context.WithValue(context.Background(), identityKey{}, Identity{TenantID: "tenant", KeyID: "key"})
			ctx = context.WithValue(ctx, usageSinkKey{}, sink)
			h.recordUsageOn(ctx, "node", usage, time.Second, UsageEndpointAnthropicMessages, "claude-opus-5-5")
			if len(ledger.records) != 1 || ledger.records[0].PromptTokens != tc.input || ledger.records[0].CompletionTokens != tc.output || sink.tokens != tc.input+tc.output {
				t.Fatalf("compaction usage not billed: records=%+v quota=%d", ledger.records, sink.tokens)
			}
			var original map[string]any
			_ = json.Unmarshal([]byte(tc.raw), &original)
			if raw["input_tokens"] != original["input_tokens"] || raw["output_tokens"] != original["output_tokens"] {
				t.Fatal("public usage changed")
			}
		})
	}
}

func TestNativeCompactionUsageRejectsInvalidCounts(t *testing.T) {
	for _, raw := range []string{
		`{"iterations":[{"input_tokens":-1,"output_tokens":20}]}`,
		`{"iterations":[{"input_tokens":1.5,"output_tokens":20}]}`,
		`{"iterations":[{"input_tokens":9223372036854775808,"output_tokens":20}]}`,
		`{"iterations":{"input_tokens":10,"output_tokens":20}}`,
	} {
		var value map[string]any
		_ = json.Unmarshal([]byte(raw), &value)
		if _, err := nativeMessagesBilledUsage(value); err == nil {
			t.Fatalf("invalid billing counts accepted: %s", raw)
		}
	}
}
