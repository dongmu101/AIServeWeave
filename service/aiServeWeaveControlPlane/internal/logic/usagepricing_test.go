package logic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricingconfig"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

// capturingChargeStore records what ChargeUsage was asked to write.
// capturingChargeStore 记录 ChargeUsage 被要求写入的内容。
type capturingChargeStore struct {
	*memstore.Store
	mu      sync.Mutex
	charged []model.UsageRecord
}

func (s *capturingChargeStore) ChargeUsage(ctx context.Context, records []model.UsageRecord, now time.Time) (bool, error) {
	s.mu.Lock()
	s.charged = append(s.charged, records...)
	s.mu.Unlock()
	return s.Store.ChargeUsage(ctx, records, now)
}

// chargingFixture is a billing-enabled fixture whose tenant is prepaid with
// a large balance, and the capturing store under it.
// chargingFixture 是开启计费、租户为 prepaid 且余额充足的 fixture，以及它底下的捕获
// store。
func chargingFixture(t *testing.T) (*fixture, *capturingChargeStore) {
	t.Helper()
	f := newFixture(t)
	capture := &capturingChargeStore{Store: f.store}
	f.svc = logic.New(capture, f.clock, logic.WithBilling(logic.BillingConfig{Provider: &accountTestProvider{}}))
	now := f.clock.Now()
	if err := f.store.InitTenantBilling(context.Background(), model.TenantBilling{
		TenantID: f.tenant.ID, Mode: model.BillingModePrepaid, BalanceMicro: 1_000_000_000, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
	return f, capture
}

// claudeCatalog prices claude-x like the catalog: $3 / $15, cache read
// $0.30, 5-minute cache write $3.75. inputScale scales the input price.
// claudeCatalog 像价目表那样为 claude-x 定价：$3 / $15，缓存读取 $0.30，5 分钟缓存写入
// $3.75。inputScale 缩放输入价。
func applyClaudeCatalog(t *testing.T, f *fixture, input float64, at time.Time) {
	t.Helper()
	doc := fmt.Sprintf(`{"claude-x":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":%g,"output_cost_per_token":1.5e-5,
		"cache_read_input_token_cost":3e-7,"cache_creation_input_token_cost":3.75e-6}}`, input)
	snap, err := pricecatalog.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyCatalogSnapshot(context.Background(), snap, store.CatalogApplyOptions{Source: "test", Embedded: true, MaxChangePercent: 30}, at); err != nil {
		t.Fatal(err)
	}
}

// pushOne pushes one record and returns it as ChargeUsage received it.
// pushOne 推送一条记录，并返回 ChargeUsage 收到的样子。
func pushOne(t *testing.T, f *fixture, capture *capturingChargeStore, p logic.CreateUsageRecordParams) model.UsageRecord {
	t.Helper()
	if p.Endpoint == "" {
		p.Endpoint = "anthropic_messages"
	}
	if p.TenantID == "" {
		p.TenantID = f.tenant.ID
	}
	before := len(capture.charged)
	accepted, err := f.svc.CreateUsageRecords(context.Background(), []logic.CreateUsageRecordParams{p})
	if err != nil || accepted != 1 || len(capture.charged) != before+1 {
		t.Fatalf("push %s: accepted = %d, err = %v", p.RequestID, accepted, err)
	}
	return capture.charged[before]
}

func usageDetailsOf(t *testing.T, r model.UsageRecord) model.UsageDetails {
	t.Helper()
	if r.Details == nil {
		t.Fatalf("record %s has no details", r.ID)
	}
	var doc model.UsageDetails
	if err := json.Unmarshal([]byte(*r.Details), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func revisionOf(t *testing.T, f *fixture) int64 {
	t.Helper()
	view, err := f.svc.GetPricingConfig(context.Background(), accountOperator)
	if err != nil {
		t.Fatal(err)
	}
	return view.Revision.ID
}

// cachedRequest is 1000 prompt tokens, 600 of them cache reads and 300
// cache writes, and 100 completion tokens. At claude-x's catalog prices it
// costs 100*3 + 600*0.3 + 300*3.75 + 100*15 = 3105 micro-credits.
// cachedRequest 是 1000 个 prompt token（其中 600 个缓存读取、300 个缓存写入）与 100 个
// completion token。按 claude-x 的价目表单价，它的费用是 3105 micro 点。
func cachedRequest(id, alias string, at time.Time) logic.CreateUsageRecordParams {
	return logic.CreateUsageRecordParams{
		RequestID: id, Model: alias, PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100, CreatedAt: at,
		Details: map[string]int64{pricing.MeterCacheRead: 600, pricing.MeterCacheWrite: 300},
	}
}

func TestChargeUsagePricesFromTheCatalogAndChargesTheBalance(t *testing.T) {
	f, capture := chargingFixture(t)
	ctx := context.Background()
	now := f.clock.Now()
	applyClaudeCatalog(t, f, 3e-6, now)

	r := pushOne(t, f, capture, cachedRequest("req_cat", "claude-x", now))
	if r.CostMicro != 3105 || r.BaseCostMicro != 3105 || r.RatePPM != pricing.RateOne || r.PriceSource != model.PriceSourceCatalog {
		t.Fatalf("record = cost %d base %d rate %d source %q; want 3105 at 1x from the catalog", r.CostMicro, r.BaseCostMicro, r.RatePPM, r.PriceSource)
	}
	doc := usageDetailsOf(t, r)
	if doc.Ref != "claude-x" || doc.Source != model.PriceSourceCatalog || doc.Plan != "plan_default" || doc.Revision == 0 || doc.Unpriced {
		t.Fatalf("details = %+v", doc)
	}
	lines := map[string]model.UsageMeterLine{}
	for _, line := range doc.Meters {
		lines[line.K] = line
	}
	if l := lines[pricing.MeterInput]; l.Q != 1000 || l.X != 100 || l.P != 3_000_000 || l.C != 300 {
		t.Errorf("input line = %+v", l)
	}
	if l := lines[pricing.MeterCacheWrite]; l.X != 300 || l.P != 3_750_000 || l.C != 1125 {
		t.Errorf("cache write line = %+v", l)
	}
	billing, err := f.store.GetTenantBilling(ctx, f.tenant.ID)
	if err != nil || billing.BalanceMicro != 1_000_000_000-3105 {
		t.Fatalf("balance = %d (%v), want it charged 3105", billing.BalanceMicro, err)
	}

	// The same request pushed again is not charged again.
	// 同一请求再次推送不会再次扣费。
	if _, err := f.svc.CreateUsageRecords(ctx, []logic.CreateUsageRecordParams{func() logic.CreateUsageRecordParams {
		p := cachedRequest("req_cat", "claude-x", now.AddDate(0, 1, 0))
		p.TenantID, p.Endpoint = f.tenant.ID, "anthropic_messages"
		return p
	}()}); err != nil {
		t.Fatal(err)
	}
	if billing, _ := f.store.GetTenantBilling(ctx, f.tenant.ID); billing.BalanceMicro != 1_000_000_000-3105 {
		t.Fatalf("balance = %d after a resend, want it unchanged", billing.BalanceMicro)
	}
}

func TestChargeUsagePriceSourcesAndModelRate(t *testing.T) {
	f, capture := chargingFixture(t)
	ctx := context.Background()
	now := f.clock.Now()
	applyClaudeCatalog(t, f, 3e-6, now)

	// Nothing prices an unknown alias yet: free, and flagged.
	// 还没有任何东西为未知别名定价：免费，并被标记。
	r := pushOne(t, f, capture, logic.CreateUsageRecordParams{RequestID: "req_none", Model: "local-llama", PromptTokens: 1000, CompletionTokens: 100, CreatedAt: now})
	if r.CostMicro != 0 || r.PriceSource != model.PriceSourceNone || !usageDetailsOf(t, r).Unpriced {
		t.Fatalf("unpriced record = cost %d source %q", r.CostMicro, r.PriceSource)
	}

	rev := revisionOf(t, f)
	view, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "my-claude", CatalogModel: "claude-x", RatePPM: rateOf(1_500_000), BaseRevision: rev})
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "ov", Prices: testPrices(1_000_000, 2_000_000), BaseRevision: view.Revision.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PutDefaultPrices(ctx, accountOperator, testPrices(500_000, 500_000), view.Revision.ID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, alias, source string
		details             bool
		cost, base, rate    int64
	}{
		// A reference model at a 1.5x model rate: 3105 * 1.5 = 4657.5, rounded up.
		{"reference", "my-claude", model.PriceSourceCatalog, true, 4658, 3105, 1_500_000},
		// An override replaces the catalog wholesale: 1000*1 + 100*2.
		{"override", "ov", model.PriceSourceOverride, false, 1200, 1200, pricing.RateOne},
		// The default price for an alias nothing else prices: 1100*0.5.
		{"default", "local-llama", model.PriceSourceDefault, false, 550, 550, pricing.RateOne},
	}
	for i, c := range cases {
		p := logic.CreateUsageRecordParams{RequestID: fmt.Sprintf("req_src_%d", i), Model: c.alias, PromptTokens: 1000, CompletionTokens: 100, CreatedAt: f.clock.Now()}
		if c.details {
			p = cachedRequest(p.RequestID, c.alias, p.CreatedAt)
		}
		r := pushOne(t, f, capture, p)
		if r.CostMicro != c.cost || r.BaseCostMicro != c.base || r.RatePPM != c.rate || r.PriceSource != c.source {
			t.Errorf("%s: cost %d base %d rate %d source %q; want %d %d %d %q", c.name, r.CostMicro, r.BaseCostMicro, r.RatePPM, r.PriceSource, c.cost, c.base, c.rate, c.source)
		}
	}
}

func TestChargeUsageAppliesThePlanAsOfEachRequest(t *testing.T) {
	f, capture := chargingFixture(t)
	ctx := context.Background()
	applyClaudeCatalog(t, f, 3e-6, f.clock.Now())
	rev := revisionOf(t, f)
	view, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "my-claude", CatalogModel: "claude-x", RatePPM: rateOf(1_500_000), BaseRevision: rev})
	if err != nil {
		t.Fatal(err)
	}
	vip, err := f.svc.SavePricePlan(ctx, accountOperator, "", logic.PricePlanInput{
		Name: "VIP", DefaultRatePPM: rateOf(800_000), BaseRevision: view.Revision.ID,
		Models: map[string]pricingconfig.PlanModel{"fixed-model": {Prices: testPrices(100_000, 100_000)}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := f.clock.Now()
	f.clock.Advance(time.Minute)
	if err := f.svc.SetTenantPricePlan(ctx, accountOperator, f.tenant.ID, vip.Plan.ID); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	after := f.clock.Now()

	// One batch straddling the plan change: the earlier request is on the
	// default plan, the later one on VIP.
	// 跨越方案变更的一批：较早的请求按默认方案，较晚的按 VIP。
	batch := []logic.CreateUsageRecordParams{cachedRequest("req_plan_before", "claude-x", before), cachedRequest("req_plan_after", "claude-x", after)}
	for i := range batch {
		batch[i].TenantID, batch[i].Endpoint = f.tenant.ID, "anthropic_messages"
	}
	start := len(capture.charged)
	if _, err := f.svc.CreateUsageRecords(ctx, batch); err != nil {
		t.Fatal(err)
	}
	got := capture.charged[start:]
	if len(got) != 2 || got[0].CostMicro != 3105 || got[1].CostMicro != 2484 || usageDetailsOf(t, got[1]).Plan != vip.Plan.ID {
		t.Fatalf("costs = %d, %d; want 3105 on the default plan and 2484 (3105*0.8) on VIP", got[0].CostMicro, got[1].CostMicro)
	}

	// The model rate and the plan factor multiply: 3105 * 1.5 * 0.8 = 3726.
	// 模型倍率与方案系数相乘：3105 * 1.5 * 0.8 = 3726。
	if r := pushOne(t, f, capture, cachedRequest("req_plan_rate", "my-claude", after)); r.CostMicro != 3726 || r.RatePPM != 1_200_000 {
		t.Errorf("my-claude on VIP = cost %d rate %d, want 3726 at 1.2x", r.CostMicro, r.RatePPM)
	}
	// A fixed price ignores every rate: 1000*0.1 + 100*0.1 = 110.
	// 固定售价不受任何倍率影响：1000*0.1 + 100*0.1 = 110。
	r := pushOne(t, f, capture, logic.CreateUsageRecordParams{RequestID: "req_plan_fixed", Model: "fixed-model", PromptTokens: 1000, CompletionTokens: 100, CreatedAt: after})
	if r.CostMicro != 110 || r.RatePPM != pricing.RateOne || r.PriceSource != model.PriceSourcePlanFixed {
		t.Errorf("fixed price = cost %d rate %d source %q, want 110 at 1x from the plan", r.CostMicro, r.RatePPM, r.PriceSource)
	}
}

func TestChargeUsagePricesAsOfTheRequestNotTheIngest(t *testing.T) {
	f, capture := chargingFixture(t)
	ctx := context.Background()
	t0 := f.clock.Now()
	applyClaudeCatalog(t, f, 3e-6, t0)
	view, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "tm", Prices: testPrices(1_000_000, 0), BaseRevision: revisionOf(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	// Warm the cache on the first prices, then change them an hour later.
	// 先用第一组单价把缓存加载起来，一小时后再改价。
	pushOne(t, f, capture, logic.CreateUsageRecordParams{RequestID: "req_tm_warm", Model: "tm", PromptTokens: 1000, CreatedAt: t0})
	f.clock.Advance(time.Hour)
	if _, err := f.svc.PutPricingModel(ctx, accountOperator, logic.PricingModelInput{Model: "tm", Prices: testPrices(2_000_000, 0), BaseRevision: view.Revision.ID}); err != nil {
		t.Fatal(err)
	}
	// A 10% catalog change applies on its own.
	// 价目表 10% 的变动会自动生效。
	applyClaudeCatalog(t, f, 3.3e-6, f.clock.Now())
	f.clock.Advance(time.Hour)

	if r := pushOne(t, f, capture, logic.CreateUsageRecordParams{RequestID: "req_tm_old", Model: "tm", PromptTokens: 1000, CreatedAt: t0.Add(30 * time.Minute)}); r.CostMicro != 1000 {
		t.Errorf("a request made before the change = %d, want the old 1000", r.CostMicro)
	}
	if r := pushOne(t, f, capture, logic.CreateUsageRecordParams{RequestID: "req_tm_new", Model: "tm", PromptTokens: 1000, CreatedAt: f.clock.Now()}); r.CostMicro != 2000 {
		t.Errorf("a request made after the change = %d, want the new 2000", r.CostMicro)
	}
	if r := pushOne(t, f, capture, logic.CreateUsageRecordParams{RequestID: "req_tm_future", Model: "tm", PromptTokens: 1000, CreatedAt: f.clock.Now().Add(24 * time.Hour)}); r.CostMicro != 2000 {
		t.Errorf("a request stamped in the future = %d, want today's 2000", r.CostMicro)
	}
	// The catalog, too: 100*3.3 + 600*0.3 + 300*3.75 + 100*15 = 3135 now, 3105 before.
	// 价目表同理：现在是 3135，变动之前是 3105。
	if r := pushOne(t, f, capture, cachedRequest("req_cat_old", "claude-x", t0.Add(30*time.Minute))); r.CostMicro != 3105 {
		t.Errorf("catalog price before the change = %d, want 3105", r.CostMicro)
	}
	if r := pushOne(t, f, capture, cachedRequest("req_cat_new", "claude-x", f.clock.Now())); r.CostMicro != 3135 {
		t.Errorf("catalog price after the change = %d, want 3135", r.CostMicro)
	}
}

func TestChargeUsageLeavesTheTenantsOwnNodeUnbilled(t *testing.T) {
	f, capture := chargingFixture(t)
	ctx := context.Background()
	applyClaudeCatalog(t, f, 3e-6, f.clock.Now())
	p := cachedRequest("req_own", "claude-x", f.clock.Now())
	p.NodeID = fmt.Sprintf("tnode.%s.%032x", f.tenant.ID, 7)
	r := pushOne(t, f, capture, p)
	if r.CostMicro != 0 || r.BaseCostMicro != 3105 || !usageDetailsOf(t, r).OwnNode {
		t.Fatalf("own-node record = cost %d base %d, want 0 charged and 3105 recorded", r.CostMicro, r.BaseCostMicro)
	}
	if billing, _ := f.store.GetTenantBilling(ctx, f.tenant.ID); billing.BalanceMicro != 1_000_000_000 {
		t.Fatalf("balance = %d, want it untouched", billing.BalanceMicro)
	}
}
