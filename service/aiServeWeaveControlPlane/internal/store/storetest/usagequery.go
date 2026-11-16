package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// UsageQueries checks the per-request list (newest first, filters, cursor
// pages that cross a month's partition), the unpriced count and the cache
// and cost sums of the summary (.scratch/token-pricing/spec.md「API」).
//
// UsageQueries 检查逐笔列表（最新优先、筛选条件、跨越月分区的游标分页）、未定价计数，
// 以及汇总中的缓存与费用合计（.scratch/token-pricing/spec.md「API」）。
func UsageQueries(t *testing.T, st store.UsageRecords) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	var records []model.UsageRecord
	for i := 0; i < 7; i++ {
		r := model.UsageRecord{
			ID: fmt.Sprintf("req_q%d", i), TenantID: "tnt_q", Model: "m1", Endpoint: "chat", KeyID: "key_a",
			PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CacheReadTokens: 60, CacheWriteTokens: 20,
			CostMicro: 5, PriceSource: model.PriceSourceCatalog, CreatedAt: t0.Add(time.Duration(i) * 30 * time.Second),
		}
		if i%2 == 1 {
			r.Model, r.KeyID = "m2", "key_b"
		}
		if i == 6 {
			r.PriceSource, r.CostMicro = model.PriceSourceNone, 0
		}
		records = append(records, r)
	}
	records = append(records, model.UsageRecord{ID: "req_other", TenantID: "tnt_other", Model: "m1", Endpoint: "chat",
		PromptTokens: 1, TotalTokens: 1, PriceSource: model.PriceSourceNone, CreatedAt: t0})
	if err := st.CreateUsageRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	// Pages of three, newest first, across the October/November boundary.
	// 每页三条，最新优先，跨越十月与十一月的分界。
	var ids []string
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := st.ListUsageRecords(ctx, store.ListQuery{Limit: 3, Cursor: cursor}, store.UsageRecordListFilter{TenantID: "tnt_q"})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			ids = append(ids, r.ID)
		}
		if page.NextCursor == "" {
			break
		}
		if pages > 5 {
			t.Fatal("pagination does not end")
		}
		cursor = page.NextCursor
	}
	want := []string{"req_q6", "req_q5", "req_q4", "req_q3", "req_q2", "req_q1", "req_q0"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("pages = %v, want %v", ids, want)
	}

	filtered, err := st.ListUsageRecords(ctx, store.ListQuery{}, store.UsageRecordListFilter{
		TenantID: "tnt_q", Model: "m2", KeyID: "key_b", Since: t0.Add(time.Minute), Until: t0.Add(150 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].ID != "req_q3" || filtered.Items[0].CacheReadTokens != 60 {
		t.Fatalf("filtered = %+v, want only req_q3 with its cache columns", filtered.Items)
	}
	all, err := st.ListUsageRecords(ctx, store.ListQuery{Limit: 100}, store.UsageRecordListFilter{})
	if err != nil || len(all.Items) != 8 {
		t.Fatalf("cross-tenant list = %d records (%v), want 8", len(all.Items), err)
	}

	if n, err := st.CountUsageRecordsBySource(ctx, model.PriceSourceNone, t0); err != nil || n != 2 {
		t.Fatalf("unpriced since t0 = %d (%v), want 2", n, err)
	}
	if n, err := st.CountUsageRecordsBySource(ctx, model.PriceSourceNone, t0.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("unpriced since t0+1s = %d (%v), want 1", n, err)
	}

	summary, err := st.SummarizeUsage(ctx, store.UsageRecordFilter{TenantID: "tnt_q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) != 2 {
		t.Fatalf("summary = %+v, want m1 and m2", summary)
	}
	m1 := summary[0]
	if m1.Model != "m1" || m1.RequestCount != 4 || m1.CacheReadTokens != 240 || m1.CacheWriteTokens != 80 || m1.CostMicro != 15 {
		t.Fatalf("m1 = %+v, want 4 requests, 240 cache reads, 80 writes and 15 micro", m1)
	}
}
