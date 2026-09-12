package logic_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestCreateRequestLogsRejectsAMissingRequiredField(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name   string
		params logic.CreateRequestLogParams
	}{
		{name: "missing request id", params: logic.CreateRequestLogParams{TenantID: f.tenant.ID, Endpoint: "chat", Outcome: "ok", CreatedAt: time.Now()}},
		{name: "missing tenant id", params: logic.CreateRequestLogParams{RequestID: "req_1", Endpoint: "chat", Outcome: "ok", CreatedAt: time.Now()}},
		{name: "missing endpoint", params: logic.CreateRequestLogParams{RequestID: "req_1", TenantID: f.tenant.ID, Outcome: "ok", CreatedAt: time.Now()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accepted, err := f.svc.CreateRequestLogs(context.Background(), []logic.CreateRequestLogParams{tt.params})
			if err != nil {
				t.Fatalf("CreateRequestLogs(%+v) error = %v, want nil (an invalid entry is skipped, not an error)", tt.params, err)
			}
			if accepted != 0 {
				t.Fatalf("CreateRequestLogs(%+v) accepted = %d, want 0 (the entry is invalid)", tt.params, accepted)
			}
		})
	}
}

func TestCreateRequestLogsAcceptsAValidBatchAndSkipsInvalidEntries(t *testing.T) {
	f := newFixture(t)
	valid := logic.CreateRequestLogParams{RequestID: "req_ok", TenantID: f.tenant.ID, Endpoint: "chat", StatusCode: 200, Outcome: "ok", DurationMS: 10, CreatedAt: time.Now()}
	invalid := logic.CreateRequestLogParams{Endpoint: "chat"} // missing RequestID/TenantID

	accepted, err := f.svc.CreateRequestLogs(context.Background(), []logic.CreateRequestLogParams{valid, invalid})
	if err != nil {
		t.Fatalf("CreateRequestLogs() error = %v, want nil (a batch with one bad entry still persists the good ones)", err)
	}
	if accepted != 1 {
		t.Fatalf("CreateRequestLogs() accepted = %d, want 1 (only the valid entry)", accepted)
	}

	page, err := f.svc.ListRequestLogs(context.Background(), store.ListQuery{}, store.RequestLogFilter{TenantID: f.tenant.ID})
	if err != nil {
		t.Fatalf("ListRequestLogs() error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "req_ok" {
		t.Fatalf("ListRequestLogs() = %+v, want exactly req_ok", page.Items)
	}
}
