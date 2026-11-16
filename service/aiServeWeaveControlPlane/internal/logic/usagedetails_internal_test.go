package logic

import (
	"encoding/json"
	"testing"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// TestApplyUsageDetailsStoresTheMeterTree checks the stored details JSON and
// the cache columns, including the parent-not-below-children correction.
// TestApplyUsageDetailsStoresTheMeterTree 检查保存的明细 JSON 与缓存列，包括「父项
// 不小于子项之和」的校正。
func TestApplyUsageDetailsStoresTheMeterTree(t *testing.T) {
	record := model.UsageRecord{TenantID: "tnt_1", PromptTokens: 1000, CompletionTokens: 50}
	applyUsageDetails(&record, map[string]int64{
		pricing.MeterCacheRead: 600, pricing.MeterCacheWrite1h: 100, pricing.MeterReasoning: 20,
	}, map[string]string{"service_tier": "flex"})

	if record.CacheReadTokens != 600 || record.CacheWriteTokens != 100 {
		t.Fatalf("cache columns = %d / %d, want 600 / 100", record.CacheReadTokens, record.CacheWriteTokens)
	}
	if record.Details == nil {
		t.Fatal("details = nil, want the meter tree")
	}
	var doc model.UsageDetails
	if err := json.Unmarshal([]byte(*record.Details), &doc); err != nil {
		t.Fatal(err)
	}
	want := []model.UsageMeterLine{
		{K: pricing.MeterInput, Q: 1000},
		{K: pricing.MeterCacheRead, Q: 600},
		{K: pricing.MeterCacheWrite, Q: 100}, // raised to its child / 提高到子项之和
		{K: pricing.MeterCacheWrite1h, Q: 100},
		{K: pricing.MeterOutput, Q: 50},
		{K: pricing.MeterReasoning, Q: 20},
	}
	if doc.V != model.UsageDetailsVersion || !doc.Adjusted || doc.Attributes["service_tier"] != "flex" || len(doc.Meters) != len(want) {
		t.Fatalf("details = %+v, want version %d, adjusted, the attribute and %d meters", doc, model.UsageDetailsVersion, len(want))
	}
	for i, line := range want {
		if doc.Meters[i] != line {
			t.Errorf("meter %d = %+v, want %+v", i, doc.Meters[i], line)
		}
	}
}

// TestApplyUsageDetailsDropsInvalidDetailsButKeepsTheRecord checks the
// backstop: details or attributes over the limits are dropped, and the
// record keeps its totals.
// TestApplyUsageDetailsDropsInvalidDetailsButKeepsTheRecord 检查兜底规则：超出上限的
// 明细或属性被丢弃，记录保留其总数。
func TestApplyUsageDetailsDropsInvalidDetailsButKeepsTheRecord(t *testing.T) {
	record := model.UsageRecord{TenantID: "tnt_1", PromptTokens: 10, CompletionTokens: 5}
	applyUsageDetails(&record, map[string]int64{pricing.MeterCacheRead: -1}, map[string]string{"Bad": "x"})
	if record.Details != nil || record.CacheReadTokens != 0 || record.PromptTokens != 10 {
		t.Fatalf("record = %+v, want its totals and no details", record)
	}

	record = model.UsageRecord{TenantID: "tnt_1", PromptTokens: 10}
	applyUsageDetails(&record, map[string]int64{pricing.MeterCacheRead: 4}, map[string]string{"Bad": "x"})
	if record.Details == nil || record.CacheReadTokens != 4 {
		t.Fatalf("record = %+v, want the valid details kept when only the attributes are invalid", record)
	}

	record = model.UsageRecord{TenantID: "tnt_1", PromptTokens: 10}
	applyUsageDetails(&record, nil, nil)
	if record.Details != nil {
		t.Fatal("a record without details must store none")
	}
}
