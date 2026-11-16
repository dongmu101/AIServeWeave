package cliagent

import (
	"encoding/json"
	"testing"

	"AIServeWeave/common/pricing"
)

// TestUsageWithDetailsKeepsCacheReadsAndWritesApart checks the Claude-style
// result usage: cache reads and writes still count toward the prompt, and
// now also as details, the one-hour writes included.
// TestUsageWithDetailsKeepsCacheReadsAndWritesApart 检查 Claude 风格的 result 用量：
// 缓存读取与写入仍计入 prompt，同时也作为明细保留，包括 1 小时缓存的写入。
func TestUsageWithDetailsKeepsCacheReadsAndWritesApart(t *testing.T) {
	var obj map[string]json.RawMessage
	raw := `{"input_tokens":10,"cache_read_input_tokens":500,"cache_creation_input_tokens":90,
		"cache_creation":{"ephemeral_1h_input_tokens":40},"output_tokens":7}`
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	usage, err := usageWithDetails(obj,
		[][]string{{"input_tokens"}, {"cache_read_input_tokens"}, {"cache_creation_input_tokens"}},
		[]string{"", pricing.MeterCacheRead, pricing.MeterCacheWrite},
		[][]string{{"output_tokens"}})
	if err != nil {
		t.Fatal(err)
	}
	if usage.PromptTokens != 600 || usage.CompletionTokens != 7 {
		t.Fatalf("usage = %+v, want prompt 600 and completion 7", usage)
	}
	want := map[string]int64{pricing.MeterCacheRead: 500, pricing.MeterCacheWrite: 90, pricing.MeterCacheWrite1h: 40}
	if len(usage.Details) != len(want) {
		t.Fatalf("details = %v, want %v", usage.Details, want)
	}
	for name, n := range want {
		if usage.Details[name] != n {
			t.Errorf("%s = %d, want %d", name, usage.Details[name], n)
		}
	}

	// usageFrom stays as it was: no details.
	// usageFrom 保持原样：没有明细。
	plain, err := usageFrom(obj, [][]string{{"input_tokens"}}, [][]string{{"output_tokens"}})
	if err != nil || plain.Details != nil || plain.PromptTokens != 10 {
		t.Fatalf("usageFrom = %+v, %v; want prompt 10 and no details", plain, err)
	}

	_ = json.Unmarshal([]byte(`{"input_tokens":1,"cache_creation":{"ephemeral_1h_input_tokens":-1},"cache_creation_input_tokens":2}`), &obj)
	if _, err := usageWithDetails(obj, [][]string{{"input_tokens"}, {"cache_creation_input_tokens"}}, []string{"", pricing.MeterCacheWrite}, nil); err == nil {
		t.Fatal("a negative one-hour count must be a protocol error")
	}
}
