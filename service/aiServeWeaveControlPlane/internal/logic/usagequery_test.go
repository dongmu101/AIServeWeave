package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/common/modelroute"
	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// publishRoutes publishes one route per alias, each to a runtime model of
// the same name.
// publishRoutes 为每个别名发布一条路由，各自指向同名的运行时模型。
func publishRoutes(t *testing.T, f *fixture, aliases ...string) {
	t.Helper()
	routes := make([]modelroute.Route, len(aliases))
	for i, alias := range aliases {
		routes[i] = modelroute.Route{Model: alias, Targets: []modelroute.Target{{RuntimeModel: alias}}}
	}
	current, err := f.svc.CurrentRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PublishRoutes(context.Background(), accountOperator, current.Revision, routes); err != nil {
		t.Fatal(err)
	}
}

func priceOf(t *testing.T, items []logic.EffectivePrice, alias string) logic.EffectivePrice {
	t.Helper()
	for _, item := range items {
		if item.Model == alias {
			return item
		}
	}
	t.Fatalf("no price for %s in %+v", alias, items)
	return logic.EffectivePrice{}
}

func saleOf(t *testing.T, p logic.EffectivePrice, meter string) logic.EffectivePriceItem {
	t.Helper()
	for _, item := range p.Items {
		if item.Meter == meter && item.AboveInputTokens == 0 {
			return item
		}
	}
	t.Fatalf("%s has no base-tier %s price: %+v", p.Model, meter, p.Items)
	return logic.EffectivePriceItem{}
}

func TestOperatorEffectivePrices(t *testing.T) {
	f, capture := chargingFixture(t)
	ctx := context.Background()
	applyClaudeCatalog(t, f, 3e-6, f.clock.Now())
	publishRoutes(t, f, "claude-x", "local-llama")
	view, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "my-claude", CatalogModel: "claude-x", RatePPM: rateOf(1_500_000), BaseRevision: revisionOf(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	vip, err := f.svc.SavePricePlan(ctx, accountOperator, "", logic.PricePlanInput{Name: "VIP", DefaultRatePPM: rateOf(800_000), BaseRevision: view.Revision.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	pushOne(t, f, capture, logic.CreateUsageRecordParams{RequestID: "req_free", Model: "local-llama", PromptTokens: 5, CreatedAt: f.clock.Now()})

	prices, err := f.svc.OperatorEffectivePrices(ctx, accountOperator, "")
	if err != nil {
		t.Fatal(err)
	}
	if prices.PlanID != "plan_default" || len(prices.Items) != 3 {
		t.Fatalf("prices = %+v, want the default plan and three aliases", prices)
	}
	claude := priceOf(t, prices.Items, "claude-x")
	if !claude.Routed || claude.Source != model.PriceSourceCatalog || claude.Ref != "claude-x" || saleOf(t, claude, pricing.MeterInput).SaleMicroPerM != 3_000_000 {
		t.Errorf("claude-x = %+v", claude)
	}
	mine := priceOf(t, prices.Items, "my-claude")
	if mine.Routed || mine.ModelRatePPM != 1_500_000 || mine.RatePPM != 1_500_000 || saleOf(t, mine, pricing.MeterCacheRead).SaleMicroPerM != 450_000 {
		t.Errorf("my-claude = %+v, want an unrouted alias at 1.5x (cache read 0.30 -> 0.45)", mine)
	}
	if local := priceOf(t, prices.Items, "local-llama"); local.Source != model.PriceSourceNone || len(local.Items) != 0 {
		t.Errorf("local-llama = %+v, want unpriced", local)
	}
	if len(prices.UnpricedAliases) != 1 || prices.UnpricedAliases[0] != "local-llama" || prices.UnpricedRequests24h != 1 {
		t.Errorf("unpriced = %v / %d, want local-llama and one request", prices.UnpricedAliases, prices.UnpricedRequests24h)
	}

	onVIP, err := f.svc.OperatorEffectivePrices(ctx, accountOperator, vip.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := priceOf(t, onVIP.Items, "my-claude"); got.PlanRatePPM != 800_000 || got.RatePPM != 1_200_000 || saleOf(t, got, pricing.MeterInput).SaleMicroPerM != 3_600_000 {
		t.Errorf("my-claude on VIP = %+v, want 1.2x and $3.60 input", got)
	}
	if _, err := f.svc.OperatorEffectivePrices(ctx, accountOperator, "plan_missing"); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("unknown plan: %v, want ErrNotFound", err)
	}
	if _, err := f.svc.OperatorEffectivePrices(ctx, f.ownerAt, ""); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("tenant owner: %v, want ErrForbidden", err)
	}
	off := newFixture(t)
	if _, err := off.svc.OperatorEffectivePrices(ctx, accountOperator, ""); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("billing off: %v, want ErrNotFound", err)
	}
}

func TestTenantPricesFollowThePlanAndTheModelPolicy(t *testing.T) {
	f, _ := chargingFixture(t)
	ctx := context.Background()
	applyClaudeCatalog(t, f, 3e-6, f.clock.Now())
	publishRoutes(t, f, "claude-x", "other")
	vip, err := f.svc.SavePricePlan(ctx, accountOperator, "", logic.PricePlanInput{Name: "VIP", DefaultRatePPM: rateOf(500_000), BaseRevision: revisionOf(t, f)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetTenantPricePlan(ctx, accountOperator, f.tenant.ID, vip.Plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetModelAccess(ctx, accountOperator, f.tenant.ID, []string{"claude-x"}); err != nil {
		t.Fatal(err)
	}

	prices, err := f.svc.TenantPrices(ctx, f.ownerAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 1 || prices[0].Model != "claude-x" {
		t.Fatalf("prices = %+v, want only the allowed claude-x", prices)
	}
	if p := prices[0]; p.Ref != "" || p.RatePPM != 500_000 || saleOf(t, p, pricing.MeterOutput).SaleMicroPerM != 7_500_000 {
		t.Errorf("claude-x = %+v, want no ref and half price", p)
	}
	if _, err := f.svc.TenantPrices(ctx, accountOperator, nil); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("operator: %v, want ErrForbidden", err)
	}
	if _, err := newFixture(t).svc.TenantPrices(ctx, f.ownerAt, nil); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("billing off: %v, want ErrNotFound", err)
	}
}

func TestUsageRecordDetailsFallsBackToTheTotals(t *testing.T) {
	doc := logic.UsageRecordDetails(model.UsageRecord{PromptTokens: 7, CompletionTokens: 3})
	if len(doc.Meters) != 2 || doc.Meters[0].K != pricing.MeterInput || doc.Meters[0].Q != 7 || doc.Meters[1].Q != 3 {
		t.Fatalf("details = %+v, want input 7 and output 3", doc)
	}
	bad := "{not json"
	if doc := logic.UsageRecordDetails(model.UsageRecord{PromptTokens: 1, Details: &bad}); len(doc.Meters) != 1 {
		t.Fatalf("undecodable details = %+v, want the totals", doc)
	}
}
