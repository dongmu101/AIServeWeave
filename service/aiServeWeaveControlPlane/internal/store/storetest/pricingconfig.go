package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// PricingConfig verifies the versioned pricing configuration: the first
// revision is written once however many replicas race, appends are guarded
// by the base revision and by plans still in use, effect times never go
// backwards, tenant plans keep their history, and every write bumps the
// version exactly once. st must be empty of pricing data.
//
// PricingConfig 验证带版本的定价配置：无论多少副本同时初始化，第一个版本只写一次；追加
// 受基础版本与「方案仍在使用」守护；生效时间绝不倒退；租户方案保留历史；每次写入让版本号
// 恰好加一。st 中不能已有定价数据。
func PricingConfig(t *testing.T, st store.PricingConfig) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)

	if _, err := st.LatestPricingRevision(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("latest revision of an empty store", err)
	}
	if _, err := st.PricingRevisionAt(ctx, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("revision at of an empty store", err)
	}
	if v, err := st.PricingConfigVersion(ctx); err != nil || v != 0 {
		t.Fatal("initial version", v, err)
	}
	if _, err := st.AppendPricingRevision(ctx, store.AppendPricingRevision{BaseRevisionID: 1, Document: "{}", Now: t0}); !errors.Is(err, store.ErrPricingRevisionChanged) {
		t.Fatal("append before the first revision", err)
	}

	// Replicas racing to bootstrap agree on one first revision.
	var wg sync.WaitGroup
	results := make([]model.PricingRevision, 8)
	created := make([]bool, len(results))
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], created[i], errs[i] = st.CreateInitialPricingRevision(ctx, model.PricingRevision{
				EffectiveAt: t0, Document: `{"v":1}`, CreatedBy: model.PricingRevisionSystemActor,
				Summary: "initial", CreatedAt: t0,
			})
		}(i)
	}
	wg.Wait()
	first := results[0]
	creations := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatal("bootstrap", errs[i])
		}
		if results[i].ID != first.ID {
			t.Fatal("bootstrap replicas disagree", results[i].ID, first.ID)
		}
		if created[i] {
			creations++
		}
	}
	if creations != 1 || first.ID <= 0 || first.Document != `{"v":1}` {
		t.Fatal("bootstrap must create exactly one revision", creations, first)
	}
	if v, _ := st.PricingConfigVersion(ctx); v != 1 {
		t.Fatal("version after bootstrap", v)
	}

	// Appends need the latest revision as their base.
	t1 := t0.Add(time.Hour)
	second, err := st.AppendPricingRevision(ctx, store.AppendPricingRevision{
		BaseRevisionID: first.ID, Document: `{"v":1,"n":2}`, CreatedBy: "operator", Summary: "second", Now: t1,
	})
	if err != nil || second.ID <= first.ID || !second.EffectiveAt.Equal(t1) || second.CreatedBy != "operator" {
		t.Fatal("append", second, err)
	}
	if _, err := st.AppendPricingRevision(ctx, store.AppendPricingRevision{BaseRevisionID: first.ID, Document: "{}", Now: t1}); !errors.Is(err, store.ErrPricingRevisionChanged) || !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale base must conflict", err)
	}
	// A replica whose clock lags keeps effect times in order.
	third, err := st.AppendPricingRevision(ctx, store.AppendPricingRevision{
		BaseRevisionID: second.ID, Document: `{"v":1,"n":3}`, CreatedBy: "operator", Summary: "third", Now: t1.Add(-time.Minute),
	})
	if err != nil || !third.EffectiveAt.Equal(t1) {
		t.Fatal("lagging clock must not move effect time back", third, err)
	}
	if v, _ := st.PricingConfigVersion(ctx); v != 3 {
		t.Fatal("version after two appends", v)
	}

	// Revision at a moment: before the first is the first; ties take the newest id.
	for _, c := range []struct {
		at   time.Time
		want int64
	}{
		{t0.Add(-time.Hour), first.ID},
		{t0, first.ID},
		{t0.Add(30 * time.Minute), first.ID},
		{t1, third.ID},
		{t1.Add(time.Hour), third.ID},
	} {
		rev, err := st.PricingRevisionAt(ctx, c.at)
		if err != nil || rev.ID != c.want {
			t.Fatalf("revision at %s = %d, %v; want %d", c.at, rev.ID, err, c.want)
		}
	}
	if latest, err := st.LatestPricingRevision(ctx); err != nil || latest.ID != third.ID || latest.Document != `{"v":1,"n":3}` {
		t.Fatal("latest", latest, err)
	}
	if got, err := st.GetPricingRevision(ctx, second.ID); err != nil || got.Document != `{"v":1,"n":2}` || got.Summary != "second" {
		t.Fatal("get", got, err)
	}
	if _, err := st.GetPricingRevision(ctx, third.ID+100); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("get missing", err)
	}

	// The list is newest first, without documents, and pages by id.
	page, err := st.ListPricingRevisions(ctx, store.ListQuery{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != third.ID || page.Items[1].ID != second.ID || page.NextCursor == "" {
		t.Fatal("first page", page, err)
	}
	if page.Items[0].Document != "" || page.Items[0].Summary != "third" {
		t.Fatal("list must leave documents out", page.Items[0])
	}
	rest, err := st.ListPricingRevisions(ctx, store.ListQuery{Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(rest.Items) != 1 || rest.Items[0].ID != first.ID || rest.NextCursor != "" {
		t.Fatal("second page", rest, err)
	}
	if _, err := st.ListPricingRevisions(ctx, store.ListQuery{Cursor: store.EncodeCursor(t0, "led_x")}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatal("foreign cursor", err)
	}

	// Tenant plans: a move is guarded by the revision logic checked against.
	latest := third
	if _, err := st.SetTenantPricePlan(ctx, store.SetTenantPricePlan{TenantID: "tnt_a", PlanID: "plan_vip", ExpectRevisionID: second.ID, Now: t1}); !errors.Is(err, store.ErrPricingRevisionChanged) {
		t.Fatal("stale revision must conflict", err)
	}
	if changed, err := st.SetTenantPricePlan(ctx, store.SetTenantPricePlan{TenantID: "tnt_a", PlanID: "", ExpectRevisionID: latest.ID, Now: t1}); err != nil || changed {
		t.Fatal("default to default is no change", changed, err)
	}
	t2 := t1.Add(time.Hour)
	if changed, err := st.SetTenantPricePlan(ctx, store.SetTenantPricePlan{TenantID: "tnt_a", PlanID: "plan_vip", ExpectRevisionID: latest.ID, CreatedBy: "operator", Now: t2}); err != nil || !changed {
		t.Fatal("assign", changed, err)
	}
	if changed, err := st.SetTenantPricePlan(ctx, store.SetTenantPricePlan{TenantID: "tnt_a", PlanID: "plan_vip", ExpectRevisionID: latest.ID, Now: t2.Add(time.Minute)}); err != nil || changed {
		t.Fatal("same plan is no change", changed, err)
	}
	t3 := t2.Add(time.Hour)
	if changed, err := st.SetTenantPricePlan(ctx, store.SetTenantPricePlan{TenantID: "tnt_a", PlanID: "plan_gold", ExpectRevisionID: latest.ID, Now: t3}); err != nil || !changed {
		t.Fatal("move", changed, err)
	}
	if _, err := st.SetTenantPricePlan(ctx, store.SetTenantPricePlan{TenantID: "tnt_b", PlanID: "plan_vip", ExpectRevisionID: latest.ID, Now: t3}); err != nil {
		t.Fatal(err)
	}
	// A lagging clock does not end an assignment before it began.
	t4 := t3.Add(time.Hour)
	if _, err := st.SetTenantPricePlan(ctx, store.SetTenantPricePlan{TenantID: "tnt_b", PlanID: "", ExpectRevisionID: latest.ID, Now: t3.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.PricingConfigVersion(ctx); v != 7 {
		t.Fatal("version after four plan changes", v)
	}
	for _, c := range []struct {
		at       time.Time
		a, b     string
		aIn, bIn bool
		label    string
	}{
		{t1, "", "", false, false, "before any assignment"},
		{t2, "plan_vip", "", true, false, "at assignment start"},
		{t3.Add(-time.Second), "plan_vip", "", true, false, "just before the move"},
		{t3, "plan_gold", "", true, false, "at the move; b's assignment ended as it began"},
		{t4, "plan_gold", "", true, false, "later"},
	} {
		got, err := st.TenantPricePlansAt(ctx, []string{"tnt_a", "tnt_b", "tnt_none"}, c.at)
		if err != nil {
			t.Fatal(err)
		}
		a, aIn := got["tnt_a"]
		b, bIn := got["tnt_b"]
		if a != c.a || aIn != c.aIn || b != c.b || bIn != c.bIn {
			t.Fatalf("%s: plans = %v", c.label, got)
		}
		if _, ok := got["tnt_none"]; ok {
			t.Fatalf("%s: a tenant never assigned must be absent", c.label)
		}
	}
	if got, err := st.TenantPricePlansAt(ctx, nil, t4); err != nil || len(got) != 0 {
		t.Fatal("no tenants", got, err)
	}
	counts, err := st.CountTenantPricePlans(ctx)
	if err != nil || counts["plan_gold"] != 1 || counts["plan_vip"] != 0 || len(counts) != 1 {
		t.Fatal("counts", counts, err)
	}

	// A plan in use cannot be removed by an append; an unused one can.
	if _, err := st.AppendPricingRevision(ctx, store.AppendPricingRevision{
		BaseRevisionID: latest.ID, Document: "{}", Now: t4, UnusedPlans: []string{"plan_vip", "plan_gold"},
	}); !errors.Is(err, store.ErrPricePlanInUse) || !errors.Is(err, store.ErrConflict) {
		t.Fatal("plan in use", err)
	}
	if _, err := st.AppendPricingRevision(ctx, store.AppendPricingRevision{
		BaseRevisionID: latest.ID, Document: `{"v":1,"n":4}`, Now: t4, UnusedPlans: []string{"plan_vip"},
	}); err != nil {
		t.Fatal("unused plan", err)
	}
	if v, _ := st.PricingConfigVersion(ctx); v != 8 {
		t.Fatal("refused appends must not bump the version", v)
	}
}
