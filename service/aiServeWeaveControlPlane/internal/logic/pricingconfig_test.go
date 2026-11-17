package logic_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricingconfig"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func pricingFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.svc = logic.New(f.store, f.clock, logic.WithBilling(logic.BillingConfig{Provider: &accountTestProvider{}}))
	return f
}

func testPrices(input, output int64) pricing.PriceList {
	return pricing.PriceList{
		{Meter: pricing.MeterInput, PriceMicroPerM: input},
		{Meter: pricing.MeterOutput, PriceMicroPerM: output},
	}
}

func rateOf(v int64) *int64 { return &v }

// seedCatalog puts chat entries with the given names into the price catalog.
// seedCatalog 把给定名称的对话条目写入价目表。
func seedCatalog(t *testing.T, f *fixture, names ...string) {
	t.Helper()
	doc := map[string]any{}
	for _, name := range names {
		doc[name] = map[string]any{"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 3e-6, "output_cost_per_token": 1.5e-5}
	}
	data, _ := json.Marshal(doc)
	snap, err := pricecatalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyCatalogSnapshot(context.Background(), snap, store.CatalogApplyOptions{Source: "test", Embedded: true}, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
}

func auditActions(t *testing.T, f *fixture) map[string]int {
	t.Helper()
	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{Limit: 200}, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, entry := range page.Items {
		out[entry.Action]++
	}
	return out
}

func TestPricingConfigBootstrapsFromModelPrices(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	now := f.clock.Now()
	for _, p := range []model.ModelPrice{
		{Model: model.DefaultPriceModel, InputMicroPerMTok: 1_000_000, OutputMicroPerMTok: 2_000_000, UpdatedBy: "ops", UpdatedAt: now},
		{Model: "gpt-x", InputMicroPerMTok: 3_000_000, OutputMicroPerMTok: 15_000_000, UpdatedBy: "ops", UpdatedAt: now},
	} {
		if err := f.store.PutModelPrice(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	view, err := f.svc.GetPricingConfig(ctx, accountOperator)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision.ID != 1 || view.Revision.CreatedBy != model.PricingRevisionSystemActor || !strings.Contains(view.Revision.Summary, "1 models") {
		t.Fatal("first revision", view.Revision)
	}
	if len(view.Document.DefaultPrices) != 2 || view.Document.Models["gpt-x"].RatePPM != pricing.RateOne {
		t.Fatal("converted document", view.Document)
	}
	if view.Document.PlanFor("").DefaultRatePPM != pricing.RateOne {
		t.Fatal("default plan at 1x")
	}
	// Later model_prices edits do not create another first revision.
	if err := f.store.PutModelPrice(ctx, model.ModelPrice{Model: "late", InputMicroPerMTok: 1, OutputMicroPerMTok: 1, UpdatedBy: "ops", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.EnsurePricingConfig(ctx)
	if err != nil || again.Revision.ID != 1 || len(again.Document.Models) != 1 {
		t.Fatal("bootstrap must happen once", again.Revision, err)
	}
}

func TestPricingConfigRequiresBillingOperator(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	if _, err := f.svc.GetPricingConfig(ctx, f.ownerAt); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("tenant owner read the pricing configuration", err)
	}
	if err := f.svc.SetTenantPricePlan(ctx, f.ownerAt, f.tenant.ID, ""); !errors.Is(err, logic.ErrForbidden) {
		t.Fatal("tenant owner set a plan", err)
	}
	off := logic.New(f.store, f.clock)
	if _, err := off.GetPricingConfig(ctx, accountOperator); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("billing off must hide the configuration", err)
	}
	if _, err := off.ListPricePlans(ctx, accountOperator); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("billing off must hide plans", err)
	}
}

func TestPutPricingModel(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	seedCatalog(t, f, "openai/gpt-x")
	first, err := f.svc.GetPricingConfig(ctx, accountOperator)
	if err != nil {
		t.Fatal(err)
	}
	in := logic.PricingModelInput{Model: " gpt-x ", CatalogModel: " openai/gpt-x ", BaseRevision: first.Revision.ID}
	if _, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "gpt-x"}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("a missing base revision must be refused", err)
	}
	if _, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "gpt-x", BaseRevision: first.Revision.ID + 5}); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("a stale base revision must conflict", err)
	}
	for name, bad := range map[string]logic.PricingModelInput{
		"star":     {Model: "*", BaseRevision: first.Revision.ID},
		"empty":    {Model: " ", BaseRevision: first.Revision.ID},
		"rate":     {Model: "a", RatePPM: rateOf(pricing.MaxRate + 1), BaseRevision: first.Revision.ID},
		"prices":   {Model: "a", Prices: pricing.PriceList{{Meter: "Bad"}}, BaseRevision: first.Revision.ID},
		"catalog":  {Model: "a", CatalogModel: strings.Repeat("c", pricingconfig.MaxCatalogModelLen+1), BaseRevision: first.Revision.ID},
		"negative": {Model: "a", Prices: pricing.PriceList{{Meter: pricing.MeterInput, PriceMicroPerM: -1}}, BaseRevision: first.Revision.ID},
	} {
		if _, err := f.svc.PutPricingModel(ctx, accountOperator, bad); !errors.Is(err, logic.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	view, err := f.svc.PutPricingModel(ctx, accountOperator, in)
	if err != nil {
		t.Fatal(err)
	}
	cfg := view.Document.Models["gpt-x"]
	if view.Revision.ID != first.Revision.ID+1 || cfg.CatalogModel != "openai/gpt-x" || cfg.RatePPM != pricing.RateOne || len(cfg.Prices) != 0 {
		t.Fatal("set model", view.Revision, cfg)
	}
	if view.Revision.CreatedBy != accountOperator.UserID || view.Revision.Summary != "set model gpt-x" {
		t.Fatal("revision attribution", view.Revision)
	}
	view, err = f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{
		Model: "gpt-x", Prices: testPrices(3, 15), RatePPM: rateOf(1_500_000), BaseRevision: view.Revision.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg = view.Document.Models["gpt-x"]
	if cfg.CatalogModel != "" || len(cfg.Prices) != 2 || cfg.RatePPM != 1_500_000 {
		t.Fatal("a put replaces the alias's settings wholesale", cfg)
	}
	if _, err := f.svc.DeletePricingModel(ctx, accountOperator, "missing", view.Revision.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("delete missing", err)
	}
	deleted, err := f.svc.DeletePricingModel(ctx, accountOperator, "gpt-x", view.Revision.ID)
	if err != nil || len(deleted.Document.Models) != 0 {
		t.Fatal("delete", deleted.Document.Models, err)
	}
	actions := auditActions(t, f)
	if actions[model.ActionBillingPricingModelSet] != 2 || actions[model.ActionBillingPricingModelDelete] != 1 {
		t.Fatal("audit", actions)
	}
}

func TestPutDefaultPrices(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	first, _ := f.svc.GetPricingConfig(ctx, accountOperator)
	view, err := f.svc.PutDefaultPrices(ctx, accountOperator, testPrices(1, 2), first.Revision.ID)
	if err != nil || len(view.Document.DefaultPrices) != 2 || view.Revision.Summary != "set default prices" {
		t.Fatal("set", view, err)
	}
	if _, err := f.svc.PutDefaultPrices(ctx, accountOperator, pricing.PriceList{{Meter: pricing.MeterInput, PriceMicroPerM: 1}, {Meter: pricing.MeterInput, PriceMicroPerM: 2}}, view.Revision.ID); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("duplicate keys", err)
	}
	cleared, err := f.svc.PutDefaultPrices(ctx, accountOperator, nil, view.Revision.ID)
	if err != nil || cleared.Document.DefaultPrices != nil || cleared.Revision.Summary != "clear default prices" {
		t.Fatal("clear", cleared, err)
	}
	if auditActions(t, f)[model.ActionBillingPricingDefaultSet] != 2 {
		t.Fatal("audit")
	}
}

func TestPricePlansAndTenantAssignment(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	first, _ := f.svc.GetPricingConfig(ctx, accountOperator)

	created, err := f.svc.SavePricePlan(ctx, accountOperator, "", logic.PricePlanInput{
		Name: " VIP ", DefaultRatePPM: rateOf(800_000), BaseRevision: first.Revision.ID,
		Models: map[string]pricingconfig.PlanModel{
			"gpt-x": {RatePPM: rateOf(900_000)},
			"gpt-y": {Prices: testPrices(1, 2)},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	vip := created.Plan.ID
	if !strings.HasPrefix(vip, model.PrefixPricePlan) || created.Plan.Plan.Name != "VIP" || created.Plan.Default || len(created.Plan.Plan.Models) != 2 {
		t.Fatal("create", created.Plan)
	}
	if _, err := f.svc.SavePricePlan(ctx, accountOperator, "", logic.PricePlanInput{Name: "X", BaseRevision: first.Revision.ID}, true); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("stale base on create", err)
	}
	if _, err := f.svc.SavePricePlan(ctx, accountOperator, "", logic.PricePlanInput{Name: "X", BaseRevision: created.Revision.ID,
		Models: map[string]pricingconfig.PlanModel{"a": {}}}, true); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("terms without a rate or prices", err)
	}
	if _, err := f.svc.SavePricePlan(ctx, accountOperator, "plan_missing", logic.PricePlanInput{Name: "X", BaseRevision: created.Revision.ID}, false); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("update missing", err)
	}
	// An update with nil rate and models keeps them.
	updated, err := f.svc.SavePricePlan(ctx, accountOperator, vip, logic.PricePlanInput{Name: "Gold", BaseRevision: created.Revision.ID}, false)
	if err != nil || updated.Plan.Plan.Name != "Gold" || updated.Plan.Plan.DefaultRatePPM != 800_000 || len(updated.Plan.Plan.Models) != 2 {
		t.Fatal("update keeps", updated.Plan, err)
	}
	updated, err = f.svc.SavePricePlan(ctx, accountOperator, vip, logic.PricePlanInput{Name: "Gold", DefaultRatePPM: rateOf(0),
		Models: map[string]pricingconfig.PlanModel{}, BaseRevision: updated.Revision.ID}, false)
	if err != nil || updated.Plan.Plan.DefaultRatePPM != 0 || len(updated.Plan.Plan.Models) != 0 {
		t.Fatal("update replaces", updated.Plan, err)
	}
	// The default plan's factor is the global rate and can be changed.
	global, err := f.svc.SavePricePlan(ctx, accountOperator, pricingconfig.DefaultPlanID, logic.PricePlanInput{Name: "Default", DefaultRatePPM: rateOf(1_200_000), BaseRevision: updated.Revision.ID}, false)
	if err != nil || !global.Plan.Default || global.Plan.Plan.DefaultRatePPM != 1_200_000 {
		t.Fatal("global rate", global.Plan, err)
	}
	base := global.Revision.ID
	if _, err := f.svc.DeletePricePlan(ctx, accountOperator, pricingconfig.DefaultPlanID, base); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("the default plan cannot be deleted", err)
	}

	// Assignment.
	if err := f.svc.SetTenantPricePlan(ctx, accountOperator, "tnt_missing", vip); !errors.Is(err, logic.ErrNotFound) {
		t.Fatal("unknown tenant", err)
	}
	if err := f.svc.SetTenantPricePlan(ctx, accountOperator, f.tenant.ID, "plan_missing"); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("unknown plan", err)
	}
	if err := f.svc.SetTenantPricePlan(ctx, accountOperator, f.tenant.ID, vip); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.ListBillingTenants(ctx, accountOperator, store.ListQuery{}, store.TenantFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].PricePlanID != vip {
		t.Fatal("tenant list plan", page.Items, err)
	}
	plans, err := f.svc.ListPricePlans(ctx, accountOperator)
	if err != nil || len(plans.Plans) != 2 || plans.Plans[0].ID != pricingconfig.DefaultPlanID || plans.Plans[0].TenantCount != nil ||
		plans.Plans[1].ID != vip || plans.Plans[1].TenantCount == nil || *plans.Plans[1].TenantCount != 1 || plans.Revision.ID != base {
		t.Fatal("plan list", plans, err)
	}
	if _, err := f.svc.DeletePricePlan(ctx, accountOperator, vip, base); !errors.Is(err, logic.ErrConflict) {
		t.Fatal("a plan in use cannot be deleted", err)
	}
	// Back to the default by its id; then the plan can go.
	if err := f.svc.SetTenantPricePlan(ctx, accountOperator, f.tenant.ID, pricingconfig.DefaultPlanID); err != nil {
		t.Fatal(err)
	}
	page, _ = f.svc.ListBillingTenants(ctx, accountOperator, store.ListQuery{}, store.TenantFilter{})
	if page.Items[0].PricePlanID != pricingconfig.DefaultPlanID {
		t.Fatal("back to default", page.Items[0].PricePlanID)
	}
	rev, err := f.svc.DeletePricePlan(ctx, accountOperator, vip, base)
	if err != nil || rev.ID != base+1 {
		t.Fatal("delete unused plan", rev, err)
	}
	if err := f.svc.SetTenantPricePlan(ctx, accountOperator, f.tenant.ID, vip); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("a deleted plan cannot be assigned", err)
	}
	actions := auditActions(t, f)
	if actions[model.ActionBillingPlanCreate] != 1 || actions[model.ActionBillingPlanUpdate] != 3 || actions[model.ActionBillingPlanDelete] != 1 || actions[model.ActionBillingTenantPlan] != 2 {
		t.Fatal("audit", actions)
	}
}

func TestPricingConfigAtFollowsTime(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	start := f.clock.Now()
	first, _ := f.svc.GetPricingConfig(ctx, accountOperator)
	f.clock.Advance(time.Hour)
	second, err := f.svc.PutDefaultPrices(ctx, accountOperator, testPrices(1, 2), first.Revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at   time.Time
		want int64
	}{
		{start.Add(-time.Hour), first.Revision.ID},
		{start.Add(30 * time.Minute), first.Revision.ID},
		{start.Add(time.Hour), second.Revision.ID},
		{start.Add(2 * time.Hour), second.Revision.ID},
	} {
		view, err := f.svc.PricingConfigAt(ctx, c.at)
		if err != nil || view.Revision.ID != c.want {
			t.Fatalf("at %s: %d, %v; want %d", c.at, view.Revision.ID, err, c.want)
		}
	}
	page, err := f.svc.ListPricingRevisions(ctx, accountOperator, store.ListQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.Revision.ID || page.NextCursor == "" {
		t.Fatal("revision list", page, err)
	}
	if _, err := f.svc.ListPricingRevisions(ctx, accountOperator, store.ListQuery{Cursor: "garbage!"}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatal("bad cursor", err)
	}
}

func TestConcurrentPricingEditsCannotOverwriteEachOther(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	first, _ := f.svc.GetPricingConfig(ctx, accountOperator)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{
				Model: "m" + string(rune('a'+i)), BaseRevision: first.Revision.ID,
			})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, logic.ErrConflict):
			t.Fatal(err)
		}
	}
	latest, _ := f.svc.EnsurePricingConfig(ctx)
	if ok != 1 || len(latest.Document.Models) != 1 || latest.Revision.ID != first.Revision.ID+1 {
		t.Fatal("exactly one edit from the same base may land", ok, latest.Document.Models)
	}
}

// TestPutPricingModelChecksTheCatalogReference checks that a reference model
// must be a catalog entry the catalog still prices.
// TestPutPricingModelChecksTheCatalogReference 检查参照模型必须是价目表仍在定价的条目。
func TestPutPricingModelChecksTheCatalogReference(t *testing.T) {
	f := pricingFixture(t)
	ctx := context.Background()
	seedCatalog(t, f, "claude-x")
	view, err := f.svc.GetPricingConfig(ctx, accountOperator)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "my-claude", CatalogModel: "no-such-model", BaseRevision: view.Revision.ID})
	if !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("unknown reference: err = %v, want ErrInvalidInput", err)
	}
	next, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "my-claude", CatalogModel: "claude-x", BaseRevision: view.Revision.ID})
	if err != nil {
		t.Fatalf("known reference: %v", err)
	}
	if next.Revision.ID == view.Revision.ID {
		t.Fatal("setting a valid reference must append a revision")
	}
}
