package openai

import (
	"encoding/json"
	"testing"

	"AIServeWeave/common/pricing"
)

// TestUsageDTOKeepsTheCachedShareAsADetail checks that the cached share of
// prompt_tokens becomes a detail while the total stays as reported, since
// OpenAI's prompt_tokens already counts it.
// TestUsageDTOKeepsTheCachedShareAsADetail 检查 prompt_tokens 中命中缓存的部分成为
// 明细、而总数保持原样，因为 OpenAI 的 prompt_tokens 已经计入了它。
func TestUsageDTOKeepsTheCachedShareAsADetail(t *testing.T) {
	var dto usageDTO
	raw := `{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,
		"prompt_tokens_details":{"cached_tokens":600},"completion_tokens_details":{"reasoning_tokens":20}}`
	if err := json.Unmarshal([]byte(raw), &dto); err != nil {
		t.Fatal(err)
	}
	usage := dto.toRuntime()
	if usage.PromptTokens != 1000 || usage.CompletionTokens != 50 || usage.TotalTokens != 1050 || usage.ReasoningTokens != 20 {
		t.Fatalf("usage = %+v, want the totals as reported", usage)
	}
	if len(usage.Details) != 1 || usage.Details[pricing.MeterCacheRead] != 600 {
		t.Fatalf("details = %v, want only input.cache_read=600", usage.Details)
	}

	var plain usageDTO
	_ = json.Unmarshal([]byte(`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":0}}`), &plain)
	if got := plain.toRuntime(); got.Details != nil {
		t.Fatalf("details = %v, want nil when nothing was cached", got.Details)
	}
}
