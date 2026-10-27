package memstore_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func TestCreateUsageRecordsSkipsADuplicateIDSilently(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	rec := model.UsageRecord{ID: "req_1", TenantID: "tnt_1", Model: "llama3", Endpoint: "chat", TotalTokens: 10, CreatedAt: time.Now()}

	if err := s.CreateUsageRecords(ctx, []model.UsageRecord{rec}); err != nil {
		t.Fatalf("first CreateUsageRecords() error = %v, want nil", err)
	}
	dup := rec
	dup.TotalTokens = 9999 // a different payload under the same id must not overwrite
	if err := s.CreateUsageRecords(ctx, []model.UsageRecord{dup}); err != nil {
		t.Fatalf("duplicate CreateUsageRecords() error = %v, want nil (silent skip, not an error)", err)
	}

	summary, err := s.SummarizeUsage(ctx, store.UsageRecordFilter{TenantID: "tnt_1"})
	if err != nil {
		t.Fatalf("SummarizeUsage() error = %v, want nil", err)
	}
	if len(summary) != 1 || summary[0].TotalTokens != 10 || summary[0].RequestCount != 1 {
		t.Fatalf("SummarizeUsage() = %+v, want exactly one group with TotalTokens=10, RequestCount=1 (duplicate must not double-count)", summary)
	}
}

func TestSummarizeUsageGroupsByTenantAndModelWithinTheWindow(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	records := []model.UsageRecord{
		{ID: "req_a1", TenantID: "tnt_a", Model: "llama3", Endpoint: "chat", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CreatedAt: base},
		{ID: "req_a2", TenantID: "tnt_a", Model: "llama3", Endpoint: "chat", PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30, CreatedAt: base.Add(time.Minute)},
		{ID: "req_a3", TenantID: "tnt_a", Model: "mistral", Endpoint: "embeddings", PromptTokens: 3, TotalTokens: 3, CreatedAt: base.Add(2 * time.Minute)},
		{ID: "req_b1", TenantID: "tnt_b", Model: "llama3", Endpoint: "chat", PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8, CreatedAt: base.Add(3 * time.Minute)},
	}
	if err := s.CreateUsageRecords(ctx, records); err != nil {
		t.Fatalf("CreateUsageRecords() error = %v, want nil", err)
	}

	tests := []struct {
		name   string
		filter store.UsageRecordFilter
		want   []store.UsageSummary
	}{
		{
			name:   "tenant scope excludes other tenants and groups by model",
			filter: store.UsageRecordFilter{TenantID: "tnt_a"},
			want: []store.UsageSummary{
				{TenantID: "tnt_a", Model: "llama3", PromptTokens: 30, CompletionTokens: 15, TotalTokens: 45, RequestCount: 2},
				{TenantID: "tnt_a", Model: "mistral", PromptTokens: 3, TotalTokens: 3, RequestCount: 1},
			},
		},
		{
			name:   "empty tenant means every tenant (operator settlement view)",
			filter: store.UsageRecordFilter{},
			want: []store.UsageSummary{
				{TenantID: "tnt_a", Model: "llama3", PromptTokens: 30, CompletionTokens: 15, TotalTokens: 45, RequestCount: 2},
				{TenantID: "tnt_a", Model: "mistral", PromptTokens: 3, TotalTokens: 3, RequestCount: 1},
				{TenantID: "tnt_b", Model: "llama3", PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8, RequestCount: 1},
			},
		},
		{
			name:   "since excludes earlier rows",
			filter: store.UsageRecordFilter{Since: base.Add(90 * time.Second)},
			want: []store.UsageSummary{
				{TenantID: "tnt_a", Model: "mistral", PromptTokens: 3, TotalTokens: 3, RequestCount: 1},
				{TenantID: "tnt_b", Model: "llama3", PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8, RequestCount: 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.SummarizeUsage(ctx, tt.filter)
			if err != nil {
				t.Fatalf("SummarizeUsage(%+v) error = %v, want nil", tt.filter, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("SummarizeUsage(%+v) = %+v, want %+v", tt.filter, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("SummarizeUsage(%+v)[%d] = %+v, want %+v", tt.filter, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDeleteUsageRecordsBeforeRemovesOnlyOlderRows(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	cutoff := time.Now()
	records := []model.UsageRecord{
		{ID: "req_old", TenantID: "tnt_1", Model: "llama3", TotalTokens: 1, CreatedAt: cutoff.Add(-2 * time.Hour)},
		{ID: "req_new", TenantID: "tnt_1", Model: "llama3", TotalTokens: 2, CreatedAt: cutoff.Add(time.Hour)},
	}
	if err := s.CreateUsageRecords(ctx, records); err != nil {
		t.Fatalf("CreateUsageRecords() error = %v, want nil", err)
	}

	removed, err := s.DeleteUsageRecordsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteUsageRecordsBefore() error = %v, want nil", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteUsageRecordsBefore() removed %d rows, want 1", removed)
	}
	summary, err := s.SummarizeUsage(ctx, store.UsageRecordFilter{TenantID: "tnt_1"})
	if err != nil {
		t.Fatalf("SummarizeUsage() error = %v, want nil", err)
	}
	if len(summary) != 1 || summary[0].TotalTokens != 2 {
		t.Fatalf("SummarizeUsage() after cleanup = %+v, want only req_new's usage to remain", summary)
	}
}
