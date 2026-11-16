package pricingconfig

import (
	"errors"
	"strings"
	"testing"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

func rate(v int64) *int64 { return &v }

func prices(input, output int64) pricing.PriceList {
	return pricing.PriceList{
		{Meter: pricing.MeterOutput, PriceMicroPerM: output},
		{Meter: pricing.MeterInput, PriceMicroPerM: input},
	}
}

func TestNewValidates(t *testing.T) {
	doc := New()
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	if doc.PlanFor("").DefaultRatePPM != pricing.RateOne || doc.ModelRate("any") != pricing.RateOne {
		t.Fatal("a new document prices everything at 1x")
	}
}

func TestFromModelPrices(t *testing.T) {
	doc, skipped := FromModelPrices([]model.ModelPrice{
		{Model: model.DefaultPriceModel, InputMicroPerMTok: 1_000_000, OutputMicroPerMTok: 2_000_000},
		{Model: "gpt-x", InputMicroPerMTok: 3_000_000, OutputMicroPerMTok: 15_000_000},
		{Model: " padded", InputMicroPerMTok: 1, OutputMicroPerMTok: 1},
		{Model: "too-dear", InputMicroPerMTok: pricing.MaxPriceMicroPerM + 1, OutputMicroPerMTok: 1},
	})
	if skipped != 2 {
		t.Fatal("skipped", skipped)
	}
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	want := pricing.PriceList{{Meter: pricing.MeterInput, PriceMicroPerM: 1_000_000}, {Meter: pricing.MeterOutput, PriceMicroPerM: 2_000_000}}
	if len(doc.DefaultPrices) != 2 || doc.DefaultPrices[0] != want[0] || doc.DefaultPrices[1] != want[1] {
		t.Fatal("default prices", doc.DefaultPrices)
	}
	cfg, ok := doc.Models["gpt-x"]
	if !ok || cfg.RatePPM != pricing.RateOne || cfg.CatalogModel != "" || len(cfg.Prices) != 2 || cfg.Prices[0].Meter != pricing.MeterInput || cfg.Prices[0].PriceMicroPerM != 3_000_000 {
		t.Fatal("gpt-x", cfg)
	}
	if len(doc.Models) != 1 || len(doc.Plans) != 1 {
		t.Fatal("unexpected entries", doc.Models, doc.Plans)
	}
	if empty, skipped := FromModelPrices(nil); skipped != 0 || len(empty.DefaultPrices) != 0 || empty.Validate() != nil {
		t.Fatal("no rows", empty, skipped)
	}
}

func TestEncodeParseRoundTripIsCanonical(t *testing.T) {
	doc := New()
	doc, err := doc.SetModel("gpt-x", ModelConfig{CatalogModel: "openai/gpt-x", Prices: prices(3, 15), RatePPM: 1_500_000})
	if err != nil {
		t.Fatal(err)
	}
	doc, err = doc.PutPlan("plan_vip", Plan{Name: "VIP", DefaultRatePPM: 800_000, Models: map[string]PlanModel{
		"gpt-x": {RatePPM: rate(900_000)},
		"gpt-y": {Prices: prices(1, 2)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(back)
	if err != nil || again != raw {
		t.Fatalf("encoding is not canonical:\n%s\n%s", raw, again)
	}
	if back.Models["gpt-x"].Prices[0].Meter != pricing.MeterInput {
		t.Fatal("price lists must be kept in canonical order", back.Models["gpt-x"].Prices)
	}
	if strings.Contains(raw, `"prices":null`) {
		t.Fatal("empty price lists must be omitted", raw)
	}
}

func TestParseRefusesNewerAndInvalid(t *testing.T) {
	if _, err := Parse(`{"v":2,"plans":{"plan_default":{"name":"D","default_rate_ppm":1000000}},"default_plan":"plan_default"}`); err == nil {
		t.Fatal("a newer format must be refused")
	}
	if _, err := Parse(`{"v":1,"plans":{},"default_plan":"plan_default"}`); !errors.Is(err, ErrInvalid) {
		t.Fatal("a document without the default plan must be refused", err)
	}
	if _, err := Parse(`not json`); err == nil {
		t.Fatal("garbage must be refused")
	}
}

func TestValidation(t *testing.T) {
	base := New()
	cases := map[string]func() (Document, error){
		"star alias": func() (Document, error) {
			return base.SetModel(model.DefaultPriceModel, ModelConfig{RatePPM: pricing.RateOne})
		},
		"padded alias": func() (Document, error) { return base.SetModel(" a", ModelConfig{RatePPM: pricing.RateOne}) },
		"long alias": func() (Document, error) {
			return base.SetModel(strings.Repeat("a", MaxAliasLen+1), ModelConfig{RatePPM: pricing.RateOne})
		},
		"control alias": func() (Document, error) { return base.SetModel("a\nb", ModelConfig{RatePPM: pricing.RateOne}) },
		"bad catalog": func() (Document, error) {
			return base.SetModel("a", ModelConfig{CatalogModel: " x", RatePPM: pricing.RateOne})
		},
		"rate too high": func() (Document, error) { return base.SetModel("a", ModelConfig{RatePPM: pricing.MaxRate + 1}) },
		"negative rate": func() (Document, error) { return base.SetModel("a", ModelConfig{RatePPM: -1}) },
		"bad override": func() (Document, error) {
			return base.SetModel("a", ModelConfig{Prices: pricing.PriceList{{Meter: "Bad"}}, RatePPM: pricing.RateOne})
		},
		"bad default price": func() (Document, error) {
			return base.SetDefaultPrices(pricing.PriceList{{Meter: pricing.MeterInput, PriceMicroPerM: -1}})
		},
		"bad plan id":     func() (Document, error) { return base.PutPlan("vip", Plan{Name: "VIP", DefaultRatePPM: 1}) },
		"upper plan id":   func() (Document, error) { return base.PutPlan("plan_VIP", Plan{Name: "VIP", DefaultRatePPM: 1}) },
		"empty plan name": func() (Document, error) { return base.PutPlan("plan_x", Plan{Name: " ", DefaultRatePPM: 1}) },
		"long plan name": func() (Document, error) {
			return base.PutPlan("plan_x", Plan{Name: strings.Repeat("名", MaxPlanNameRunes+1), DefaultRatePPM: 1})
		},
		"plan rate": func() (Document, error) {
			return base.PutPlan("plan_x", Plan{Name: "X", DefaultRatePPM: pricing.MaxRate + 1})
		},
		"both terms": func() (Document, error) {
			return base.PutPlan("plan_x", Plan{Name: "X", DefaultRatePPM: 1, Models: map[string]PlanModel{"a": {RatePPM: rate(1), Prices: prices(1, 1)}}})
		},
		"no terms": func() (Document, error) {
			return base.PutPlan("plan_x", Plan{Name: "X", DefaultRatePPM: 1, Models: map[string]PlanModel{"a": {}}})
		},
		"plan model rate": func() (Document, error) {
			return base.PutPlan("plan_x", Plan{Name: "X", DefaultRatePPM: 1, Models: map[string]PlanModel{"a": {RatePPM: rate(-1)}}})
		},
		"plan model alias": func() (Document, error) {
			return base.PutPlan("plan_x", Plan{Name: "X", DefaultRatePPM: 1, Models: map[string]PlanModel{"*": {RatePPM: rate(1)}}})
		},
	}
	for name, edit := range cases {
		if _, err := edit(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := base.SetModel(strings.Repeat("名", MaxAliasLen/3), ModelConfig{RatePPM: 0}); err != nil {
		t.Errorf("a long multibyte alias within the byte limit and a zero rate are valid: %v", err)
	}
	if _, err := base.PutPlan("plan_x", Plan{Name: strings.Repeat("名", MaxPlanNameRunes), DefaultRatePPM: 0}); err != nil {
		t.Errorf("a name of exactly MaxPlanNameRunes runes is valid: %v", err)
	}
}

func TestEditsDoNotAlias(t *testing.T) {
	base, err := New().SetModel("a", ModelConfig{Prices: prices(1, 2), RatePPM: pricing.RateOne})
	if err != nil {
		t.Fatal(err)
	}
	base, err = base.PutPlan("plan_x", Plan{Name: "X", DefaultRatePPM: 1, Models: map[string]PlanModel{"a": {RatePPM: rate(5)}}})
	if err != nil {
		t.Fatal(err)
	}
	clone := base.Clone()
	clone.Models["a"].Prices[0].PriceMicroPerM = 99
	*clone.Plans["plan_x"].Models["a"].RatePPM = 99
	if base.Models["a"].Prices[0].PriceMicroPerM == 99 || *base.Plans["plan_x"].Models["a"].RatePPM == 99 {
		t.Fatal("Clone must deep-copy")
	}
	next, err := base.SetModel("b", ModelConfig{RatePPM: 2})
	if err != nil || len(base.Models) != 1 || len(next.Models) != 2 {
		t.Fatal("SetModel must not modify its receiver", base.Models, next.Models, err)
	}
}

func TestDeletes(t *testing.T) {
	doc, _ := New().SetModel("a", ModelConfig{RatePPM: 1})
	doc, _ = doc.PutPlan("plan_x", Plan{Name: "X", DefaultRatePPM: 1})
	if _, err := doc.DeleteModel("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if next, err := doc.DeleteModel("a"); err != nil || len(next.Models) != 0 || len(doc.Models) != 1 {
		t.Fatal(next.Models, err)
	}
	if _, err := doc.DeletePlan(DefaultPlanID); !errors.Is(err, ErrDefaultPlan) {
		t.Fatal(err)
	}
	if _, err := doc.DeletePlan("plan_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if next, err := doc.DeletePlan("plan_x"); err != nil || len(next.Plans) != 1 {
		t.Fatal(next.Plans, err)
	}
	if next, err := doc.SetDefaultPrices(prices(1, 2)); err != nil || len(next.DefaultPrices) != 2 {
		t.Fatal(next.DefaultPrices, err)
	} else if cleared, err := next.SetDefaultPrices(nil); err != nil || cleared.DefaultPrices != nil {
		t.Fatal("an empty list clears the default price", cleared.DefaultPrices, err)
	}
}

func TestPlanLimit(t *testing.T) {
	doc := New()
	var err error
	for i := 1; i < MaxPlans; i++ {
		doc, err = doc.PutPlan("plan_"+strings.Repeat("a", i%30+1)+string(rune('a'+i%26))+"_"+itoa(i), Plan{Name: "P", DefaultRatePPM: 1})
		if err != nil {
			t.Fatal(i, err)
		}
	}
	if len(doc.Plans) != MaxPlans {
		t.Fatal(len(doc.Plans))
	}
	if _, err := doc.PutPlan("plan_onemore", Plan{Name: "P", DefaultRatePPM: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("plan limit", err)
	}
	// Replacing an existing plan at the limit is allowed.
	if _, err := doc.PutPlan(DefaultPlanID, Plan{Name: "D", DefaultRatePPM: 2}); err != nil {
		t.Fatal(err)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

func TestEncodeSizeLimit(t *testing.T) {
	doc := New()
	big := make(pricing.PriceList, 0, pricing.MaxPriceItems)
	for i := 0; i < pricing.MaxPriceItems; i++ {
		big = append(big, pricing.PriceItem{Meter: pricing.MeterInput, AboveInputTokens: int64(i), PriceMicroPerM: pricing.MaxPriceMicroPerM})
	}
	// Built directly: each model is valid on its own, only the whole is too big.
	for i := 0; i < 300; i++ {
		doc.Models["model-"+itoa(i)] = ModelConfig{Prices: big, RatePPM: 1}
	}
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(doc); !errors.Is(err, ErrInvalid) {
		t.Fatal("an oversized document must be refused", err)
	}
}

func TestTerms(t *testing.T) {
	doc, _ := New().SetModel("a", ModelConfig{RatePPM: 1_500_000})
	doc, _ = doc.PutPlan(DefaultPlanID, Plan{Name: "D", DefaultRatePPM: 1_100_000})
	doc, _ = doc.PutPlan("plan_vip", Plan{Name: "VIP", DefaultRatePPM: 800_000, Models: map[string]PlanModel{
		"a": {RatePPM: rate(900_000)},
		"b": {Prices: prices(7, 8)},
	}})
	if doc.ModelRate("a") != 1_500_000 || doc.ModelRate("z") != pricing.RateOne {
		t.Fatal("model rate")
	}
	for _, c := range []struct {
		plan, alias string
		rate        int64
		fixed       bool
	}{
		{"plan_vip", "a", 900_000, false},
		{"plan_vip", "b", pricing.RateOne, true},
		{"plan_vip", "c", 800_000, false},
		{"", "a", 1_100_000, false},
		{"plan_deleted", "a", 1_100_000, false},
		{DefaultPlanID, "b", 1_100_000, false},
	} {
		got, fixed := doc.PlanTerms(c.plan, c.alias)
		if got != c.rate || (fixed != nil) != c.fixed {
			t.Errorf("PlanTerms(%q, %q) = %d, %v", c.plan, c.alias, got, fixed)
		}
	}
	if ids := doc.SortedPlanIDs(); len(ids) != 2 || ids[0] != DefaultPlanID || ids[1] != "plan_vip" {
		t.Fatal(ids)
	}
	if aliases := doc.SortedAliases(); len(aliases) != 1 || aliases[0] != "a" {
		t.Fatal(aliases)
	}
}

func TestValidPlanID(t *testing.T) {
	for _, id := range []string{DefaultPlanID, model.NewID(model.PrefixPricePlan), "plan_a1_b"} {
		if !ValidPlanID(id) {
			t.Errorf("ValidPlanID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "plan_", "pkg_x", "plan_A", "plan_a-b", "plan_" + strings.Repeat("a", MaxPlanIDLen)} {
		if ValidPlanID(id) {
			t.Errorf("ValidPlanID(%q) = true", id)
		}
	}
}
