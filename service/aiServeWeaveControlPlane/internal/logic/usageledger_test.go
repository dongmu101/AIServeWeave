package logic_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestCreateUsageRecordsRejectsAMissingRequiredField(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name   string
		params logic.CreateUsageRecordParams
	}{
		{name: "missing request id", params: logic.CreateUsageRecordParams{TenantID: f.tenant.ID, Model: "llama3", Endpoint: "chat", CreatedAt: time.Now()}},
		{name: "missing tenant id", params: logic.CreateUsageRecordParams{RequestID: "req_1", Model: "llama3", Endpoint: "chat", CreatedAt: time.Now()}},
		{name: "missing model", params: logic.CreateUsageRecordParams{RequestID: "req_1", TenantID: f.tenant.ID, Endpoint: "chat", CreatedAt: time.Now()}},
		{name: "missing endpoint", params: logic.CreateUsageRecordParams{RequestID: "req_1", TenantID: f.tenant.ID, Model: "llama3", CreatedAt: time.Now()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accepted, err := f.svc.CreateUsageRecords(context.Background(), []logic.CreateUsageRecordParams{tt.params})
			if err != nil {
				t.Fatalf("CreateUsageRecords(%+v) error = %v, want nil (an invalid entry is skipped, not an error)", tt.params, err)
			}
			if accepted != 0 {
				t.Fatalf("CreateUsageRecords(%+v) accepted = %d, want 0 (the entry is invalid)", tt.params, accepted)
			}
		})
	}
}

func TestCreateUsageRecordsAcceptsAValidBatchAndSkipsInvalidEntries(t *testing.T) {
	f := newFixture(t)
	valid := logic.CreateUsageRecordParams{
		RequestID: "req_ok", TenantID: f.tenant.ID, Model: "llama3", Endpoint: "chat",
		PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CreatedAt: time.Now(),
	}
	invalid := logic.CreateUsageRecordParams{Endpoint: "chat"} // missing RequestID/TenantID/Model

	accepted, err := f.svc.CreateUsageRecords(context.Background(), []logic.CreateUsageRecordParams{valid, invalid})
	if err != nil {
		t.Fatalf("CreateUsageRecords() error = %v, want nil (a batch with one bad entry still persists the good ones)", err)
	}
	if accepted != 1 {
		t.Fatalf("CreateUsageRecords() accepted = %d, want 1 (only the valid entry)", accepted)
	}

	summary, err := f.svc.SummarizeUsage(context.Background(), store.UsageRecordFilter{TenantID: f.tenant.ID})
	if err != nil {
		t.Fatalf("SummarizeUsage() error = %v, want nil", err)
	}
	if len(summary) != 1 || summary[0].Model != "llama3" || summary[0].TotalTokens != 15 {
		t.Fatalf("SummarizeUsage() = %+v, want exactly one llama3 group with TotalTokens=15", summary)
	}
}

func TestCreateUsageRecordsSkipsADuplicateRequestID(t *testing.T) {
	f := newFixture(t)
	rec := logic.CreateUsageRecordParams{RequestID: "req_dup", TenantID: f.tenant.ID, Model: "llama3", Endpoint: "chat", TotalTokens: 10, CreatedAt: time.Now()}

	if _, err := f.svc.CreateUsageRecords(context.Background(), []logic.CreateUsageRecordParams{rec}); err != nil {
		t.Fatalf("first CreateUsageRecords() error = %v, want nil", err)
	}
	retry := rec
	retry.TotalTokens = 9999 // a retried push must not double-count
	if _, err := f.svc.CreateUsageRecords(context.Background(), []logic.CreateUsageRecordParams{retry}); err != nil {
		t.Fatalf("duplicate CreateUsageRecords() error = %v, want nil (silent dedup, not an error)", err)
	}

	summary, err := f.svc.SummarizeUsage(context.Background(), store.UsageRecordFilter{TenantID: f.tenant.ID})
	if err != nil {
		t.Fatalf("SummarizeUsage() error = %v, want nil", err)
	}
	if len(summary) != 1 || summary[0].TotalTokens != 10 || summary[0].RequestCount != 1 {
		t.Fatalf("SummarizeUsage() = %+v, want TotalTokens=10, RequestCount=1 (the retried push must not double-count)", summary)
	}
}
