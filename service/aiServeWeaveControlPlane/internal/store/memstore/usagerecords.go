package memstore

import (
	"context"
	"sort"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateUsageRecords implements store.UsageRecords, mirroring
// CreateRequestLogs's duplicate-id handling exactly: a duplicate is a
// silent skip (idempotent batch push), never store.ErrConflict.
func (s *Store) CreateUsageRecords(_ context.Context, records []model.UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		if _, exists := s.usageRecords[r.ID]; exists {
			continue
		}
		s.usageRecords[r.ID] = r
	}
	return nil
}

// SummarizeUsage implements store.UsageRecords, aggregating in Go over the
// in-memory map — the equivalent of gormstore's SQL GROUP BY, since this
// store has no database to push the aggregation into.
func (s *Store) SummarizeUsage(_ context.Context, filter store.UsageRecordFilter) ([]store.UsageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type key struct{ tenantID, model string }
	totals := map[key]*store.UsageSummary{}
	for _, r := range s.usageRecords {
		if filter.TenantID != "" && r.TenantID != filter.TenantID {
			continue
		}
		if !filter.Since.IsZero() && r.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !r.CreatedAt.Before(filter.Until) {
			continue
		}
		k := key{r.TenantID, r.Model}
		agg, ok := totals[k]
		if !ok {
			agg = &store.UsageSummary{TenantID: r.TenantID, Model: r.Model}
			totals[k] = agg
		}
		agg.PromptTokens += r.PromptTokens
		agg.CompletionTokens += r.CompletionTokens
		agg.TotalTokens += r.TotalTokens
		agg.RequestCount++
	}
	out := make([]store.UsageSummary, 0, len(totals))
	for _, agg := range totals {
		out = append(out, *agg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

// DeleteUsageRecordsBefore implements store.UsageRecords.
func (s *Store) DeleteUsageRecordsBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	for id, r := range s.usageRecords {
		if r.CreatedAt.Before(before) {
			delete(s.usageRecords, id)
			removed++
		}
	}
	return removed, nil
}
