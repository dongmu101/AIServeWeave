package pricing

import (
	"math"
	"testing"
)

// sonnetLike is a catalog-style price list: $3 / $15 per million tokens,
// cache read $0.30, 5-minute write $3.75, one-hour write $6, and a
// long-context tier above 200k input tokens.
var sonnetLike = PriceList{
	{Meter: MeterInput, PriceMicroPerM: 3_000_000},
	{Meter: MeterOutput, PriceMicroPerM: 15_000_000},
	{Meter: MeterCacheRead, PriceMicroPerM: 300_000},
	{Meter: MeterCacheWrite, PriceMicroPerM: 3_750_000},
	{Meter: MeterCacheWrite1h, PriceMicroPerM: 6_000_000},
	{Meter: MeterInput, AboveInputTokens: 200_000, PriceMicroPerM: 6_000_000},
	{Meter: MeterOutput, AboveInputTokens: 200_000, PriceMicroPerM: 22_500_000},
	{Meter: MeterCacheRead, AboveInputTokens: 200_000, PriceMicroPerM: 600_000},
}

func lineOf(t *testing.T, bill Bill, meter string) BillLine {
	t.Helper()
	for _, line := range bill.Lines {
		if line.Meter == meter {
			return line
		}
	}
	t.Fatalf("bill has no line for %s: %+v", meter, bill.Lines)
	return BillLine{}
}

// TestComputeSpecExample reproduces the worked example in the spec: a
// long-context request with cache reads, at a combined rate of 1.2x.
func TestComputeSpecExample(t *testing.T) {
	bill := Compute(250_000, 3000, map[string]int64{MeterCacheRead: 200_000}, sonnetLike, "", 1_500_000, 800_000)
	if bill.Tier != 200_000 {
		t.Fatalf("tier = %d, want 200000", bill.Tier)
	}
	input := lineOf(t, bill, MeterInput)
	if input.Quantity != 250_000 || input.Billed != 50_000 || input.Price != 6_000_000 || input.Cost != 360_000 {
		t.Errorf("input line = %+v", input)
	}
	cache := lineOf(t, bill, MeterCacheRead)
	if cache.Billed != 200_000 || cache.Price != 600_000 || cache.Cost != 144_000 {
		t.Errorf("cache line = %+v", cache)
	}
	output := lineOf(t, bill, MeterOutput)
	if output.Billed != 3000 || output.Price != 22_500_000 || output.Cost != 81_000 {
		t.Errorf("output line = %+v", output)
	}
	// base = 50000*6 + 200000*0.6 + 3000*22.5 = 487500 micro; at 1.2x 585000.
	if bill.BaseCostMicro != 487_500 || bill.CostMicro != 585_000 || bill.Unpriced || bill.Adjusted {
		t.Errorf("bill = %+v, want base 487500 and cost 585000", bill)
	}
	if got := CombinedRate(1_500_000, 800_000); got != 1_200_000 {
		t.Errorf("CombinedRate = %d, want 1200000", got)
	}
}

// TestComputeSubtractsChildrenAndPricesTTLs checks that each meter is billed
// for its quantity less its children's, so 5-minute and one-hour cache
// writes get their own prices, and that the base tier applies below the
// threshold.
func TestComputeSubtractsChildrenAndPricesTTLs(t *testing.T) {
	bill := Compute(1000, 10, map[string]int64{MeterCacheRead: 600, MeterCacheWrite: 300, MeterCacheWrite1h: 100}, sonnetLike, "")
	want := map[string]struct{ billed, price int64 }{
		MeterInput:        {100, 3_000_000},
		MeterCacheRead:    {600, 300_000},
		MeterCacheWrite:   {200, 3_750_000},
		MeterCacheWrite1h: {100, 6_000_000},
		MeterOutput:       {10, 15_000_000},
	}
	for meter, w := range want {
		line := lineOf(t, bill, meter)
		if line.Billed != w.billed || line.Price != w.price {
			t.Errorf("%s = %+v, want billed %d at %d", meter, line, w.billed, w.price)
		}
	}
	// 100*3 + 600*0.3 + 200*3.75 + 100*6 + 10*15 = 300+180+750+600+150 = 1980.
	if bill.Tier != 0 || bill.BaseCostMicro != 1980 || bill.CostMicro != 1980 {
		t.Errorf("bill = %+v, want tier 0 and 1980 micro", bill)
	}
}

// TestComputeFallsBackToTheParentPrice checks that a meter without its own
// price is charged at its parent's, so an unknown meter never lowers the
// bill, and that a missing cache price means the input price.
func TestComputeFallsBackToTheParentPrice(t *testing.T) {
	prices := PriceList{{Meter: MeterInput, PriceMicroPerM: 2_000_000}, {Meter: MeterOutput, PriceMicroPerM: 8_000_000}}
	bill := Compute(1000, 100, map[string]int64{MeterCacheRead: 400, "input.someday_new": 100, MeterCacheWrite12h: 50, MeterReasoning: 30}, prices, "")
	if base := Compute(1000, 100, nil, prices, ""); bill.CostMicro != base.CostMicro {
		t.Errorf("cost with unknown details = %d, without = %d; they must match", bill.CostMicro, base.CostMicro)
	}
	if line := lineOf(t, bill, MeterCacheWrite12h); line.Price != 2_000_000 || !line.Priced {
		t.Errorf("12h write = %+v, want the input price via input.cache_write", line)
	}
	if bill.Unpriced {
		t.Error("every meter has an ancestor price, so nothing is unpriced")
	}
}

// TestComputeTierLookupOrder checks the lookup order: the meter's own
// highest tier not above the selected one, before falling back to the
// parent; and that a meter with only a base price keeps it in a higher tier.
func TestComputeTierLookupOrder(t *testing.T) {
	prices := PriceList{
		{Meter: MeterInput, PriceMicroPerM: 1_000_000},
		{Meter: MeterInput, AboveInputTokens: 128_000, PriceMicroPerM: 2_000_000},
		{Meter: MeterInput, AboveInputTokens: 256_000, PriceMicroPerM: 4_000_000},
		{Meter: MeterCacheRead, PriceMicroPerM: 100_000},
		{Meter: MeterOutput, PriceMicroPerM: 5_000_000},
	}
	cases := []struct {
		input, tier, inputPrice int64
	}{
		{128_000, 0, 1_000_000}, // not above the threshold / 没有超过阈值
		{128_001, 128_000, 2_000_000},
		{300_000, 256_000, 4_000_000},
	}
	for _, c := range cases {
		bill := Compute(c.input, 1, map[string]int64{MeterCacheRead: 1}, prices, "")
		if bill.Tier != c.tier || lineOf(t, bill, MeterInput).Price != c.inputPrice {
			t.Errorf("input %d: tier %d, input price %d; want %d, %d", c.input, bill.Tier, lineOf(t, bill, MeterInput).Price, c.tier, c.inputPrice)
		}
		if line := lineOf(t, bill, MeterCacheRead); line.Price != 100_000 {
			t.Errorf("input %d: cache read price %d, want its own base price 100000, not the tiered input price", c.input, line.Price)
		}
	}
	if price, ok := Lookup(prices, MeterOutput, "", 256_000); !ok || price != 5_000_000 {
		t.Errorf("output in a high tier = %d, %t; want its base price", price, ok)
	}
}

// TestComputeVariantFallsBackToTheDefault checks that a variant price wins
// and that a missing one falls back to the default variant.
func TestComputeVariantFallsBackToTheDefault(t *testing.T) {
	prices := PriceList{
		{Meter: MeterInput, PriceMicroPerM: 1_000_000},
		{Meter: MeterInput, Variant: "priority", PriceMicroPerM: 2_000_000},
		{Meter: MeterOutput, PriceMicroPerM: 5_000_000},
	}
	bill := Compute(10, 10, nil, prices, "priority")
	if lineOf(t, bill, MeterInput).Price != 2_000_000 || lineOf(t, bill, MeterOutput).Price != 5_000_000 {
		t.Errorf("lines = %+v", bill.Lines)
	}
}

// TestComputeRoundingRatesAndSaturation covers rounding up, a zero rate,
// unpriced meters and saturation.
func TestComputeRoundingRatesAndSaturation(t *testing.T) {
	prices := PriceList{{Meter: MeterInput, PriceMicroPerM: 1}, {Meter: MeterOutput, PriceMicroPerM: 1}}
	if bill := Compute(1, 0, nil, prices, ""); bill.BaseCostMicro != 1 || bill.CostMicro != 1 {
		t.Errorf("a tiny usage at a tiny price = %+v, want 1 micro, never 0", bill)
	}
	if bill := Compute(1_000_000, 0, nil, prices, "", 0); bill.CostMicro != 0 || bill.BaseCostMicro != 1 {
		t.Errorf("rate 0 = %+v, want cost 0 and base 1", bill)
	}
	if bill := Compute(1_000_000, 0, nil, prices, "", -5); bill.CostMicro != 0 {
		t.Errorf("a negative rate = %+v, want it read as zero", bill)
	}

	free := Compute(100, 100, nil, PriceList{{Meter: MeterInput, PriceMicroPerM: 1_000_000}}, "")
	if !free.Unpriced || lineOf(t, free, MeterOutput).Priced || free.CostMicro != 100 {
		t.Errorf("output without a price = %+v, want unpriced output at 0 and the input charged", free)
	}
	if empty := Compute(100, 100, nil, nil, ""); !empty.Unpriced || empty.CostMicro != 0 {
		t.Errorf("no prices = %+v, want unpriced and free", empty)
	}

	huge := Compute(math.MaxInt64, math.MaxInt64, nil, PriceList{{Meter: MeterInput, PriceMicroPerM: MaxPriceMicroPerM}, {Meter: MeterOutput, PriceMicroPerM: MaxPriceMicroPerM}}, "", MaxRate, MaxRate)
	if huge.BaseCostMicro != math.MaxInt64 || huge.CostMicro != math.MaxInt64 {
		t.Errorf("huge = %+v, want both costs saturated", huge)
	}
	// 100x eight times is 1e16x, beyond int64 in parts per million.
	if got := CombinedRate(MaxRate, MaxRate, MaxRate, MaxRate, MaxRate, MaxRate, MaxRate, MaxRate); got != math.MaxInt64 {
		t.Errorf("CombinedRate overflow = %d, want saturation", got)
	}
	if got := CombinedRate(); got != RateOne {
		t.Errorf("CombinedRate() = %d, want 1x", got)
	}
	if got := CombinedRate(333_333, 3_000_000); got != 999_999 {
		t.Errorf("CombinedRate(0.333333, 3) = %d, want 999999", got)
	}
}

func TestApplyRates(t *testing.T) {
	cases := []struct {
		price int64
		rates []int64
		want  int64
	}{
		{3_000_000, nil, 3_000_000},
		{3_000_000, []int64{1_500_000, 800_000}, 3_600_000},
		{1, []int64{500_000}, 1}, // 0.5 rounds up / 0.5 向上舍入
		{1, []int64{499_999}, 0},
		{-5, []int64{RateOne}, 0},
		{MaxPriceMicroPerM, []int64{MaxRate, MaxRate, MaxRate, MaxRate}, math.MaxInt64},
	}
	for _, c := range cases {
		if got := ApplyRates(c.price, c.rates...); got != c.want {
			t.Errorf("ApplyRates(%d, %v) = %d, want %d", c.price, c.rates, got, c.want)
		}
	}
}

// TestComputeAdjustsInconsistentCounts checks that a child larger than its
// parent raises the parent, so the request is charged for at least what was
// reported.
func TestComputeAdjustsInconsistentCounts(t *testing.T) {
	bill := Compute(100, 0, map[string]int64{MeterCacheRead: 500}, sonnetLike, "")
	if !bill.Adjusted || lineOf(t, bill, MeterInput).Quantity != 500 || lineOf(t, bill, MeterInput).Billed != 0 {
		t.Errorf("bill = %+v, want input raised to 500 and nothing billed at the full input price", bill)
	}
	if bill.CostMicro != 150 {
		t.Errorf("cost = %d, want 500 cache reads at 0.30 = 150", bill.CostMicro)
	}
}
