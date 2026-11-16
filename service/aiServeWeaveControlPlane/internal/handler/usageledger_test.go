package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func TestCreateUsageRecordsAcceptsAValidEntryAndSkipsAnInvalidOne(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	body := `{"records":[
		{"request_id":"req_ok","tenant_id":"tnt_1","model":"llama3","endpoint":"chat","prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"created_at":"2026-09-11T08:00:00Z"},
		{"endpoint":"chat"}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/usagerecords", strings.NewReader(body))
	w := httptest.NewRecorder()

	createUsageRecords(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.CreateUsageRecordsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Accepted != 1 {
		t.Fatalf("resp.Accepted = %d, want 1 (only the well-formed record)", resp.Accepted)
	}
}

// capturingUsageStore records what CreateUsageRecords was asked to write.
// capturingUsageStore 记录 CreateUsageRecords 被要求写入的内容。
type capturingUsageStore struct {
	*memstore.Store
	written []model.UsageRecord
}

func (s *capturingUsageStore) CreateUsageRecords(ctx context.Context, records []model.UsageRecord) error {
	s.written = append(s.written, records...)
	return s.Store.CreateUsageRecords(ctx, records)
}

func TestCreateUsageRecordsAcceptsDetailsAndAttributes(t *testing.T) {
	st := &capturingUsageStore{Store: memstore.New()}
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	body := `{"records":[
		{"request_id":"req_1","tenant_id":"tnt_1","model":"claude","endpoint":"anthropic_messages","prompt_tokens":1000,"completion_tokens":5,"total_tokens":1005,"created_at":"2026-09-11T08:00:00Z",
		 "details":{"input.cache_read":600,"input.cache_write":300},"attributes":{"service_tier":"flex"}},
		{"request_id":"req_2","tenant_id":"tnt_1","model":"claude","endpoint":"anthropic_messages","prompt_tokens":10,"total_tokens":10,"created_at":"2026-09-11T08:00:00Z",
		 "details":{"input.cache_read":-1}}
	]}`
	w := httptest.NewRecorder()
	createUsageRecords(ctx)(w, httptest.NewRequest(http.MethodPost, "/internal/v1/usagerecords", strings.NewReader(body)))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.CreateUsageRecordsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Accepted != 2 {
		t.Fatalf("accepted = %d (%v), want 2: invalid details drop only the details", resp.Accepted, err)
	}
	if len(st.written) != 2 {
		t.Fatalf("wrote %d records, want 2", len(st.written))
	}
	first, second := st.written[0], st.written[1]
	if first.CacheReadTokens != 600 || first.CacheWriteTokens != 300 || first.Details == nil || !strings.Contains(*first.Details, `"service_tier":"flex"`) {
		t.Errorf("first record = %+v, want its cache columns and details", first)
	}
	if second.Details != nil || second.PromptTokens != 10 {
		t.Errorf("second record = %+v, want its totals without details", second)
	}
}

func TestSummarizeUsageTenantScopesToTheCallersTenant(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	now := time.Now()
	if err := st.CreateUsageRecords(t.Context(), []model.UsageRecord{
		{ID: "req_mine", TenantID: "tnt_1", Model: "llama3", Endpoint: "chat", TotalTokens: 10, CreatedAt: now},
		{ID: "req_other", TenantID: "tnt_2", Model: "llama3", Endpoint: "chat", TotalTokens: 99, CreatedAt: now},
	}); err != nil {
		t.Fatalf("seeding CreateUsageRecords() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/usage/summary", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	summarizeUsageTenant(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.UsageSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].TotalTokens != 10 {
		t.Fatalf("resp.Items = %+v, want exactly tnt_1's 10 tokens (tnt_2's row must not leak in)", resp.Items)
	}
	if resp.Items[0].TenantID != "" {
		t.Errorf("resp.Items[0].TenantID = %q, want empty on the tenant-scoped endpoint", resp.Items[0].TenantID)
	}
}

func TestSummarizeUsageTenantRejectsAMalformedSince(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/usage/summary?since=not-a-time", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	summarizeUsageTenant(ctx)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed since", w.Code)
	}
}

func TestSummarizeUsageOperatorAggregatesAcrossTenantsWhenNoneIsGiven(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	now := time.Now()
	if err := st.CreateUsageRecords(t.Context(), []model.UsageRecord{
		{ID: "req_a", TenantID: "tnt_a", Model: "llama3", Endpoint: "chat", TotalTokens: 10, CreatedAt: now},
		{ID: "req_b", TenantID: "tnt_b", Model: "llama3", Endpoint: "chat", TotalTokens: 20, CreatedAt: now},
	}); err != nil {
		t.Fatalf("seeding CreateUsageRecords() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/operator/v1/usage/summary", nil)
	w := httptest.NewRecorder()
	summarizeUsageOperator(ctx)(w, req)

	var resp types.UsageSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("resp.Items = %+v, want both tenants' groups with no tenant_id filter", resp.Items)
	}
	for _, item := range resp.Items {
		if item.TenantID == "" {
			t.Errorf("item %+v has an empty TenantID, want it populated on the operator endpoint", item)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/operator/v1/usage/summary?tenant_id=tnt_a", nil)
	w = httptest.NewRecorder()
	summarizeUsageOperator(ctx)(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].TotalTokens != 10 {
		t.Fatalf("resp.Items = %+v, want exactly tnt_a's 10 tokens when tenant_id=tnt_a", resp.Items)
	}
}

func TestSummarizeUsageOperatorRejectsAMalformedUntil(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	req := httptest.NewRequest(http.MethodGet, "/operator/v1/usage/summary?until=not-a-time", nil)
	w := httptest.NewRecorder()
	summarizeUsageOperator(ctx)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed until", w.Code)
	}
}

func TestSummarizeUsageTenantGroupsByKeyOnRequest(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	now := time.Now()
	if err := st.CreateUsageRecords(t.Context(), []model.UsageRecord{
		{ID: "req_a", TenantID: "tnt_1", KeyID: "key_a", UserID: "usr_a", Model: "llama3", Endpoint: "chat", PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10, CreatedAt: now},
		{ID: "req_b", TenantID: "tnt_1", KeyID: "key_b", UserID: "usr_b", Model: "llama3", Endpoint: "chat", TotalTokens: 20, CreatedAt: now},
	}); err != nil {
		t.Fatalf("seeding CreateUsageRecords() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/usage/summary?group_by=key", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	summarizeUsageTenant(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.UsageSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Items) != 2 || resp.Items[0].KeyID != "key_a" || resp.Items[0].UserID != "usr_a" ||
		resp.Items[0].PromptTokens != 4 || resp.Items[0].CompletionTokens != 6 || resp.Items[1].KeyID != "key_b" {
		t.Fatalf("resp.Items = %+v, want key_a (usr_a, 4/6) then key_b", resp.Items)
	}
}

func TestSummarizeUsageTenantRejectsAnUnknownGroupBy(t *testing.T) {
	st := memstore.New()
	ctx := &svc.ServiceContext{Logic: logic.New(st, runtime.NewSystemClock())}
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/usage/summary?group_by=tenant", nil)
	req = req.WithContext(withActor(req.Context(), logic.Actor{TenantID: "tnt_1"}))
	w := httptest.NewRecorder()
	summarizeUsageTenant(ctx)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown group_by", w.Code)
	}
}
