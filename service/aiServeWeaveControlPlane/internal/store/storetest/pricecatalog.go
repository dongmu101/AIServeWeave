package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// catalogDoc builds a LiteLLM document from model → {field: USD per token}.
//
// catalogDoc 由「模型 → {字段: 美元/token}」构造一份 LiteLLM 文档。
func catalogDoc(t *testing.T, entries map[string]map[string]float64) pricecatalog.Snapshot {
	t.Helper()
	doc := map[string]map[string]any{}
	for name, fields := range entries {
		entry := map[string]any{"mode": "chat", "litellm_provider": "test"}
		for field, usd := range fields {
			entry[field] = usd
		}
		doc[name] = entry
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := pricecatalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// priceIn returns the base-tier price of meter in rows, or -1.
//
// priceIn 返回 rows 中 meter 基础档的单价，没有则返回 -1。
func priceIn(rows []model.CatalogPrice, meter string, above int64) int64 {
	for _, row := range rows {
		if row.Meter == meter && row.Variant == "" && row.AboveInputTokens == above {
			return row.PriceMicroPerM
		}
	}
	return -1
}

func names(changes []store.CatalogChange) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.Model
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PriceCatalog checks the price catalog: first import, automatic and
// reviewed changes, price history read back by time, rejection remembered,
// removal kept until approved, listing and pagination, and concurrent syncs
// of the same content applying once.
//
// PriceCatalog 检查价目表：首次导入、自动生效与需要审核的变动、按时间读回的单价历史、
// 被记住的拒绝、批准前保留的删除、列表与分页，以及相同内容的并发同步只生效一次。
func PriceCatalog(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	opts := store.CatalogApplyOptions{Source: "test", MaxChangePercent: 30}

	state, err := st.CatalogSyncState(ctx)
	if err != nil || state.ID != model.CatalogSyncStateID || state.ContentHash != "" || state.Version != 0 {
		t.Fatalf("initial sync state = %+v, %v", state, err)
	}

	a := catalogDoc(t, map[string]map[string]float64{
		"model-a": {"input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07},
		"model-b": {"input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06,
			"input_cost_per_token_above_200k_tokens": 2e-06, "output_cost_per_token_above_200k_tokens": 4e-06},
		"model-c": {"input_cost_per_token": 5e-07, "output_cost_per_token": 1e-06},
	})
	embedded := opts
	embedded.Embedded, embedded.Source = true, pricecatalog.EmbeddedLabel
	result, err := st.ApplyCatalogSnapshot(ctx, a, embedded, t0)
	if err != nil || result.Skipped || !sameStrings(result.Added, []string{"model-a", "model-b", "model-c"}) || result.Version != 1 {
		t.Fatalf("first import = %+v, %v", result, err)
	}
	state, _ = st.CatalogSyncState(ctx)
	if state.ContentHash != a.ContentHash || state.EmbeddedHash != a.ContentHash || state.EntryCount != 3 || state.Version != 1 || state.Source != pricecatalog.EmbeddedLabel {
		t.Fatalf("sync state after import = %+v", state)
	}
	if again, err := st.ApplyCatalogSnapshot(ctx, a, opts, t0.Add(time.Minute)); err != nil || !again.Skipped || again.Version != 1 {
		t.Fatalf("the same content must be skipped: %+v, %v", again, err)
	}
	set, err := st.CurrentCatalogPrices(ctx)
	if err != nil || set.Version != 1 || len(set.Prices) != 3 || priceIn(set.Prices["model-a"], pricing.MeterCacheRead, 0) != 300_000 {
		t.Fatalf("current prices = %+v, %v", set, err)
	}
	// model-b lists no sub-meters, so no tier was derived; its tier prices are kept.
	if priceIn(set.Prices["model-b"], pricing.MeterInput, 200_000) != 2_000_000 {
		t.Fatalf("model-b tier = %+v", set.Prices["model-b"])
	}

	// t1: model-a +10% (automatic), model-b output doubled (review), model-d new.
	t1 := t0.Add(time.Hour)
	b := catalogDoc(t, map[string]map[string]float64{
		"model-a": {"input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07},
		"model-b": {"input_cost_per_token": 1e-06, "output_cost_per_token": 4e-06,
			"input_cost_per_token_above_200k_tokens": 2e-06, "output_cost_per_token_above_200k_tokens": 4e-06},
		"model-c": {"input_cost_per_token": 5e-07, "output_cost_per_token": 1e-06},
		"model-d": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
	})
	result, err = st.ApplyCatalogSnapshot(ctx, b, opts, t1)
	if err != nil || !sameStrings(result.Added, []string{"model-d"}) || !sameStrings(names(result.Applied), []string{"model-a"}) ||
		!sameStrings(names(result.Reviewed), []string{"model-b"}) || result.Version != 2 {
		t.Fatalf("second sync = %+v, %v", result, err)
	}
	if result.Applied[0].MaxChangeBP != 1000 || result.Reviewed[0].MaxChangeBP != 10_000 {
		t.Fatalf("change sizes = %+v %+v", result.Applied, result.Reviewed)
	}
	before, err := st.CatalogPricesAt(ctx, "model-a", t1.Add(-time.Second))
	if err != nil || priceIn(before, pricing.MeterInput, 0) != 3_000_000 {
		t.Fatalf("history before the change = %+v, %v", before, err)
	}
	after, err := st.CatalogPricesAt(ctx, "model-a", t1)
	if err != nil || priceIn(after, pricing.MeterInput, 0) != 3_300_000 || priceIn(after, pricing.MeterCacheRead, 0) != 300_000 {
		t.Fatalf("history after the change = %+v, %v", after, err)
	}
	if _, err := st.CatalogPricesAt(ctx, "model-d", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("model-d did not exist at t0: %v", err)
	}
	if current, _ := st.CatalogPricesAt(ctx, "model-b", t1); priceIn(current, pricing.MeterOutput, 0) != 2_000_000 {
		t.Fatalf("a change under review must not take effect: %+v", current)
	}
	pending, err := st.ListCatalogPending(ctx, store.ListQuery{})
	if err != nil || len(pending.Items) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	p := pending.Items[0]
	if p.Pending.Model != "model-b" || p.Pending.Kind != pricecatalog.PendingChange || p.Status != pricecatalog.StatusActive ||
		priceIn(p.Current, pricing.MeterOutput, 0) != 2_000_000 || p.Proposed.Model != "model-b" {
		t.Fatalf("pending entry = %+v", p)
	}
	entry, err := st.GetCatalogEntry(ctx, "model-b")
	if err != nil || entry.Pending != pricecatalog.PendingChange || entry.Model.RawJSON == "" {
		t.Fatalf("model-b entry = %+v, %v", entry, err)
	}

	// Reject: the change is remembered and not raised again for the same prices.
	decision, err := st.RejectCatalogPending(ctx, "model-b", t1.Add(time.Minute))
	if err != nil || decision.Kind != pricecatalog.PendingChange || decision.Version != 2 {
		t.Fatalf("reject = %+v, %v", decision, err)
	}
	if _, err := st.RejectCatalogPending(ctx, "model-b", t1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("nothing waits any more: %v", err)
	}
	b2 := catalogDoc(t, map[string]map[string]float64{
		"model-a": {"input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07},
		"model-b": {"input_cost_per_token": 1e-06, "output_cost_per_token": 4e-06,
			"input_cost_per_token_above_200k_tokens": 2e-06, "output_cost_per_token_above_200k_tokens": 4e-06},
		"model-c": {"input_cost_per_token": 5e-07, "output_cost_per_token": 1e-06},
		"model-d": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
		"model-e": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
	})
	result, err = st.ApplyCatalogSnapshot(ctx, b2, opts, t1.Add(2*time.Minute))
	if err != nil || result.Suppressed != 1 || len(result.Reviewed) != 0 || !sameStrings(result.Added, []string{"model-e"}) || result.Version != 3 {
		t.Fatalf("a rejected change must stay quiet: %+v, %v", result, err)
	}

	// A different change to model-b is raised again; approving it applies it,
	// including the tier price it drops.
	t2 := t1.Add(time.Hour)
	c := catalogDoc(t, map[string]map[string]float64{
		"model-a": {"input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07},
		"model-b": {"input_cost_per_token": 1e-06, "output_cost_per_token": 6e-06, "input_cost_per_token_above_200k_tokens": 2e-06},
		"model-c": {"input_cost_per_token": 5e-07, "output_cost_per_token": 1e-06},
		"model-d": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
		"model-e": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
	})
	result, err = st.ApplyCatalogSnapshot(ctx, c, opts, t2)
	if err != nil || !sameStrings(names(result.Reviewed), []string{"model-b"}) {
		t.Fatalf("a new change must be raised: %+v, %v", result, err)
	}
	decision, err = st.ApproveCatalogPending(ctx, "model-b", t2.Add(time.Minute))
	if err != nil || decision.Kind != pricecatalog.PendingChange || decision.Version != 4 ||
		priceIn(decision.Before, pricing.MeterOutput, 0) != 2_000_000 || priceIn(decision.After, pricing.MeterOutput, 0) != 6_000_000 {
		t.Fatalf("approve = %+v, %v", decision, err)
	}
	approved, _ := st.CatalogPricesAt(ctx, "model-b", t2.Add(time.Minute))
	if priceIn(approved, pricing.MeterOutput, 0) != 6_000_000 || priceIn(approved, pricing.MeterOutput, 200_000) != -1 ||
		priceIn(approved, pricing.MeterInput, 200_000) != 2_000_000 {
		t.Fatalf("approved prices = %+v", approved)
	}
	if _, err := st.ApproveCatalogPending(ctx, "model-b", t2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approving twice: %v", err)
	}

	// model-c disappears upstream: kept with its prices until the removal is approved.
	t3 := t2.Add(time.Hour)
	d := catalogDoc(t, map[string]map[string]float64{
		"model-a": {"input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07},
		"model-b": {"input_cost_per_token": 1e-06, "output_cost_per_token": 6e-06, "input_cost_per_token_above_200k_tokens": 2e-06},
		"model-d": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
		"model-e": {"input_cost_per_token": 1e-07, "output_cost_per_token": 0},
	})
	result, err = st.ApplyCatalogSnapshot(ctx, d, opts, t3)
	if err != nil || !sameStrings(result.Removed, []string{"model-c"}) || !sameStrings(names(result.Reviewed), []string{"model-e"}) {
		t.Fatalf("removal and fall to zero = %+v, %v", result, err)
	}
	entry, _ = st.GetCatalogEntry(ctx, "model-c")
	if entry.Model.Status != pricecatalog.StatusRemovedUpstream || entry.Pending != pricecatalog.PendingRemove || priceIn(entry.Prices, pricing.MeterInput, 0) != 500_000 {
		t.Fatalf("model-c while its removal waits = %+v", entry)
	}
	decision, err = st.ApproveCatalogPending(ctx, "model-c", t3.Add(time.Minute))
	if err != nil || decision.Kind != pricecatalog.PendingRemove || len(decision.After) != 0 {
		t.Fatalf("approve removal = %+v, %v", decision, err)
	}
	set, _ = st.CurrentCatalogPrices(ctx)
	if _, ok := set.Prices["model-c"]; ok || set.Version != decision.Version {
		t.Fatalf("a removed entry has no current prices: %+v", set)
	}
	if old, err := st.CatalogPricesAt(ctx, "model-c", t3); err != nil || priceIn(old, pricing.MeterInput, 0) != 500_000 {
		t.Fatalf("history survives the removal: %+v, %v", old, err)
	}

	// Rejecting the zero price keeps the old one.
	if _, err := st.RejectCatalogPending(ctx, "model-e", t3.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if kept, _ := st.CatalogPricesAt(ctx, "model-e", t3.Add(time.Hour)); priceIn(kept, pricing.MeterOutput, 0) != 100_000 {
		t.Fatalf("a rejected zero price must not apply: %+v", kept)
	}

	// Listing in name order, with a prefix, a status and a cursor.
	page, err := st.ListCatalogEntries(ctx, store.ListQuery{Limit: 2}, store.CatalogFilter{Prefix: "model-"})
	if err != nil || len(page.Items) != 2 || page.Items[0].Model.Model != "model-a" || page.NextCursor == "" || page.Items[0].Model.RawJSON != "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page2, err := st.ListCatalogEntries(ctx, store.ListQuery{Limit: 2, Cursor: page.NextCursor}, store.CatalogFilter{Prefix: "model-"})
	if err != nil || len(page2.Items) != 2 || page2.Items[0].Model.Model != "model-c" {
		t.Fatalf("second page = %+v, %v", page2, err)
	}
	removed, err := st.ListCatalogEntries(ctx, store.ListQuery{}, store.CatalogFilter{Status: pricecatalog.StatusRemoved})
	if err != nil || len(removed.Items) != 1 || removed.Items[0].Model.Model != "model-c" {
		t.Fatalf("status filter = %+v, %v", removed, err)
	}
	if none, err := st.ListCatalogEntries(ctx, store.ListQuery{}, store.CatalogFilter{Prefix: "model_%"}); err != nil || len(none.Items) != 0 {
		t.Fatalf("LIKE wildcards in a prefix must be literal: %+v, %v", none, err)
	}
	if _, err := st.ListCatalogEntries(ctx, store.ListQuery{Cursor: "not-a-cursor"}, store.CatalogFilter{}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("invalid cursor: %v", err)
	}

	// A sync failure is recorded without touching the catalog.
	if err := st.RecordCatalogSyncError(ctx, "HTTP 503", t3.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	state, _ = st.CatalogSyncState(ctx)
	if state.LastError != "HTTP 503" || state.LastErrorAt == nil || state.ContentHash != d.ContentHash || state.Source != "test" {
		t.Fatalf("sync error = %+v", state)
	}

	// Concurrent syncs of the same new content apply it exactly once.
	e := catalogDoc(t, map[string]map[string]float64{
		"model-a": {"input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07},
		"model-b": {"input_cost_per_token": 1e-06, "output_cost_per_token": 6e-06, "input_cost_per_token_above_200k_tokens": 2e-06},
		"model-d": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
		"model-e": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
		"model-f": {"input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07},
	})
	versionBefore := state.Version
	var wg sync.WaitGroup
	applied := make(chan bool, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := st.ApplyCatalogSnapshot(ctx, e, opts, t3.Add(3*time.Hour))
			if err != nil {
				failures <- err
				return
			}
			applied <- !r.Skipped
		}()
	}
	wg.Wait()
	close(applied)
	close(failures)
	for err := range failures {
		t.Fatal("concurrent sync:", err)
	}
	count := 0
	for ok := range applied {
		if ok {
			count++
		}
	}
	state, _ = st.CatalogSyncState(ctx)
	if count != 1 || state.Version != versionBefore+1 || state.LastError != "" {
		t.Fatalf("concurrent syncs: %d applied, version %d → %d, state %+v", count, versionBefore, state.Version, state)
	}
	if rows, _ := st.CatalogPricesAt(ctx, "model-f", t3.Add(3*time.Hour)); len(rows) != 2 {
		t.Fatalf("model-f inserted %d price rows, want 2", len(rows))
	}
}
