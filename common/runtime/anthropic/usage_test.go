package anthropic

import (
	"encoding/json"
	"testing"

	"AIServeWeave/common/pricing"
)

// TestUsageCountsCacheTokensAsInputAndDetails checks that cache reads and
// writes, which the Messages API reports beside input_tokens, count as
// prompt tokens and are kept apart as details, the one-hour writes too.
// TestUsageCountsCacheTokensAsInputAndDetails 检查 Messages API 放在 input_tokens 旁边
// 报告的缓存读取与写入计为 prompt token，并作为明细分开保留，包括 1 小时缓存的写入。
func TestUsageCountsCacheTokensAsInputAndDetails(t *testing.T) {
	var u usage
	raw := `{"input_tokens":100,"output_tokens":40,"cache_read_input_tokens":600,"cache_creation_input_tokens":300,
		"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100}}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	got := u.toRuntime(u.OutputTokens)
	if got.PromptTokens != 1000 || got.CompletionTokens != 40 || got.TotalTokens != 1040 {
		t.Fatalf("usage = %+v, want prompt 1000, completion 40, total 1040", got)
	}
	want := map[string]int64{pricing.MeterCacheRead: 600, pricing.MeterCacheWrite: 300, pricing.MeterCacheWrite1h: 100}
	if len(got.Details) != len(want) {
		t.Fatalf("details = %v, want %v", got.Details, want)
	}
	for name, n := range want {
		if got.Details[name] != n {
			t.Errorf("%s = %d, want %d", name, got.Details[name], n)
		}
	}

	var plain usage
	_ = json.Unmarshal([]byte(`{"input_tokens":7,"output_tokens":3}`), &plain)
	if got := plain.toRuntime(3); got.Details != nil || got.PromptTokens != 7 {
		t.Fatalf("usage = %+v, want prompt 7 and no details", got)
	}
}
