package httpapi

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

type fakeUsageLedgerSink struct {
	records []UsageRecord
}

func (s *fakeUsageLedgerSink) enqueue(r UsageRecord) bool {
	s.records = append(s.records, r)
	return true
}

func TestRecordUsageLedgersOnlyAuthenticatedNonZeroUsage(t *testing.T) {
	tests := []struct {
		name         string
		usage        runtime.Usage
		withIdentity bool
		wantLedgered bool
	}{
		{name: "authenticated non-zero usage is ledgered", usage: runtime.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, withIdentity: true, wantLedgered: true},
		{name: "no identity in context means no tenant, so nothing is ledgered", usage: runtime.Usage{PromptTokens: 10, TotalTokens: 10}, withIdentity: false, wantLedgered: false},
		{name: "all-zero usage is skipped even with identity", usage: runtime.Usage{}, withIdentity: true, wantLedgered: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeUsageLedgerSink{}
			h := &handlers{usageLedger: sink, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
			ctx := context.Background()
			if tt.withIdentity {
				ctx = context.WithValue(ctx, identityKey{}, Identity{TenantID: "tnt_1", KeyID: "key_1"})
			}

			h.recordUsage(ctx, tt.usage, time.Millisecond, UsageEndpointChat, "llama3")

			if got := len(sink.records) == 1; got != tt.wantLedgered {
				t.Fatalf("ledgered a record = %v (records=%v), want %v", got, sink.records, tt.wantLedgered)
			}
		})
	}
}

func TestRecordUsageLedgerFieldsMatchTheCall(t *testing.T) {
	sink := &fakeUsageLedgerSink{}
	h := &handlers{usageLedger: sink, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
	ctx := context.WithValue(context.Background(), identityKey{}, Identity{TenantID: "tnt_9", KeyID: "key_9"})

	h.recordUsage(ctx, runtime.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}, time.Millisecond, UsageEndpointEmbeddings, "mistral")

	if len(sink.records) != 1 {
		t.Fatalf("got %d records, want 1", len(sink.records))
	}
	got := sink.records[0]
	if got.TenantID != "tnt_9" {
		t.Errorf("TenantID = %q, want tnt_9", got.TenantID)
	}
	if got.Model != "mistral" {
		t.Errorf("Model = %q, want mistral", got.Model)
	}
	if got.Endpoint != UsageEndpointEmbeddings {
		t.Errorf("Endpoint = %q, want %q", got.Endpoint, UsageEndpointEmbeddings)
	}
	if got.PromptTokens != 20 || got.CompletionTokens != 10 || got.TotalTokens != 30 {
		t.Errorf("token fields = %+v, want PromptTokens=20 CompletionTokens=10 TotalTokens=30", got)
	}
}

func TestRecordUsageIsANoOpWhenNoLedgerIsConfigured(t *testing.T) {
	h := &handlers{usageLedger: nil, metrics: newRecorder(nil), clock: runtime.NewSystemClock()}
	ctx := context.WithValue(context.Background(), identityKey{}, Identity{TenantID: "tnt_1"})
	// Must not panic with a nil ledger — this is the "no control plane
	// configured" degrade path every other background feature in this
	// package already follows.
	h.recordUsage(ctx, runtime.Usage{TotalTokens: 5}, time.Millisecond, UsageEndpointChat, "llama3")
}
