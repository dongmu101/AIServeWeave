package pricing

import (
	"maps"
	"math"
	"math/big"
	"slices"
)

// Bill is the outcome of pricing one usage record. See
// .scratch/token-pricing/spec.md「单笔计价」.
//
// Bill 是为一条用量记录计价的结果。见 .scratch/token-pricing/spec.md「单笔计价」。
type Bill struct {
	// Tier is the tier the request fell in: the highest AboveInputTokens
	// of the list that the input total exceeds, 0 for the base tier.
	// Tier 是请求所落入的档位：单价表中输入总数超过的最高 AboveInputTokens，基础档
	// 为 0。
	Tier int64
	// Lines holds every meter of the request in name order.
	// Lines 按名称顺序列出请求的每个计量项。
	Lines []BillLine
	// BaseCostMicro is the cost before rates, rounded up.
	// BaseCostMicro 是乘以倍率之前的费用，向上取整。
	BaseCostMicro int64
	// CostMicro is the cost at the rates, rounded up: what is charged.
	// CostMicro 是乘以倍率之后的费用，向上取整：即实际扣费。
	CostMicro int64
	// Adjusted reports a parent meter raised to the sum of its children.
	// Adjusted 表示有父项被提高到其子项之和。
	Adjusted bool
	// Unpriced reports a billed quantity that found no price at all.
	// Unpriced 表示有计费数量完全找不到单价。
	Unpriced bool
}

// BillLine is one meter of a Bill.
//
// BillLine 是 Bill 中的一个计量项。
type BillLine struct {
	Meter string
	// Quantity is the meter's quantity after the parent-not-below-children
	// correction; Billed is that less its direct children's, the part this
	// meter's own price applies to.
	// Quantity 是经过「父项不小于子项之和」校正后的数量；Billed 是它减去直接子项数量
	// 之后的部分，即适用本计量项自身单价的部分。
	Quantity int64
	Billed   int64
	// Price is the base unit price applied to Billed, in micro-credits per
	// million units; Priced is false when none was found.
	// Price 是用于 Billed 的基础单价（micro 点 / 百万单位）；找不到单价时 Priced 为
	// false。
	Price  int64
	Priced bool
	// Cost is Billed at Price and the rates, rounded up on its own. It is
	// for display: the lines' costs may add up to a few micro-credits more
	// than the Bill's CostMicro.
	// Cost 是 Billed 按 Price 与倍率计算、单独向上取整的费用。它只用于展示：各行费用之和
	// 可能比 Bill 的 CostMicro 多几个 micro 点。
	Cost int64
}

// Compute prices one usage record: prompt and completion totals plus their
// meter breakdown, against prices under the service-tier variant ("" for
// the default), at the product of rates (each in parts per million; none
// means 1x). A negative rate counts as zero. The arithmetic is exact; a cost
// beyond int64 saturates at math.MaxInt64, so no reported count can make a
// request free, and a non-zero quantity at a non-zero price and rate never
// costs nothing.
//
// Compute 为一条用量记录计价：prompt 与 completion 总数及其计量项细分，按服务等级变体
// variant（"" 为默认）在 prices 中取价，倍率为 rates 的乘积（每个都以百万分之一为单位；
// 不传表示 1 倍）。负倍率按零计。运算是精确的；超出 int64 的费用饱和为 math.MaxInt64，
// 因此上报的数量无论多大都不会让请求变成免费，非零数量在非零单价与倍率下也绝不会免费。
func Compute(prompt, completion int64, details map[string]int64, prices PriceList, variant string, rates ...int64) Bill {
	quantities, adjusted := Quantities(prompt, completion, details)
	bill := Bill{Adjusted: adjusted, Tier: SelectTier(prices, variant, quantities[MeterInput])}

	childSums := make(map[string]int64, len(quantities))
	for name, n := range quantities {
		if parent, ok := Parent(name); ok {
			childSums[parent] = saturatingAdd(childSums[parent], n)
		}
	}
	rateNum, rateDen := rateFraction(rates)
	total := new(big.Int)
	for _, name := range slices.Sorted(maps.Keys(quantities)) {
		line := BillLine{Meter: name, Quantity: quantities[name]}
		line.Billed = line.Quantity - childSums[name]
		line.Price, line.Priced = Lookup(prices, name, variant, bill.Tier)
		if line.Billed > 0 && !line.Priced {
			bill.Unpriced = true
		}
		amount := new(big.Int).Mul(big.NewInt(line.Billed), big.NewInt(line.Price))
		total.Add(total, amount)
		line.Cost = ceilDiv(new(big.Int).Mul(amount, rateNum), new(big.Int).Mul(rateDen, perMillion))
		bill.Lines = append(bill.Lines, line)
	}
	bill.BaseCostMicro = ceilDiv(total, perMillion)
	bill.CostMicro = ceilDiv(new(big.Int).Mul(total, rateNum), new(big.Int).Mul(rateDen, perMillion))
	return bill
}

// CombinedRate returns the product of rates in parts per million, rounded
// to the nearest and saturating, for display. Compute keeps the exact
// product.
//
// CombinedRate 返回 rates 的乘积（以百万分之一为单位），四舍五入并饱和，用于展示。
// Compute 使用的是精确的乘积。
func CombinedRate(rates ...int64) int64 {
	num, den := rateFraction(rates)
	scaled := new(big.Int).Mul(num, big.NewInt(RateOne))
	half := new(big.Int).Rsh(den, 1)
	return saturate(new(big.Int).Quo(scaled.Add(scaled, half), den))
}

// SelectTier returns the tier a request with inputTotal input tokens falls
// in under variant: the highest AboveInputTokens among the list's prices
// for that variant (or the default variant) that inputTotal exceeds, 0 when
// it exceeds none.
//
// SelectTier 返回输入总数为 inputTotal 的请求在 variant 下所落入的档位：单价表中该
// 变体（或默认变体）的单价里，inputTotal 超过的最高 AboveInputTokens；一个都没超过
// 时为 0。
func SelectTier(prices PriceList, variant string, inputTotal int64) int64 {
	tier := int64(0)
	for _, item := range prices {
		if (item.Variant == variant || item.Variant == "") && inputTotal > item.AboveInputTokens && item.AboveInputTokens > tier {
			tier = item.AboveInputTokens
		}
	}
	return tier
}

// Lookup returns the unit price for meter in tier under variant: the
// meter's own price at the highest tier not above tier, first for variant
// and then for the default variant, falling back meter by meter to its
// ancestors. It reports false when no ancestor has a price either.
//
// Lookup 返回计量项 meter 在档位 tier、变体 variant 下的单价：先在该变体、再在默认变体
// 中，取该计量项不高于 tier 的最高一档的单价；找不到时逐级回落到祖先计量项。所有祖先都
// 没有单价时报告 false。
func Lookup(prices PriceList, meter, variant string, tier int64) (int64, bool) {
	variants := []string{variant}
	if variant != "" {
		variants = append(variants, "")
	}
	for name, more := meter, true; more; name, more = Parent(name) {
		for _, v := range variants {
			best, found := int64(0), false
			bestTier := int64(-1)
			for _, item := range prices {
				if item.Meter == name && item.Variant == v && item.AboveInputTokens <= tier && item.AboveInputTokens > bestTier {
					best, bestTier, found = item.PriceMicroPerM, item.AboveInputTokens, true
				}
			}
			if found {
				return best, true
			}
		}
	}
	return 0, false
}

var perMillion = big.NewInt(1_000_000)

// rateFraction returns the product of rates as a fraction num/den, each
// rate in parts per million; no rates is 1x and a negative one is zero.
//
// rateFraction 以分数 num/den 返回 rates 的乘积，每个倍率以百万分之一为单位；不传
// 表示 1 倍，负倍率按零计。
func rateFraction(rates []int64) (num, den *big.Int) {
	num, den = big.NewInt(1), big.NewInt(1)
	for _, rate := range rates {
		num.Mul(num, big.NewInt(max(rate, 0)))
		den.Mul(den, perMillion)
	}
	return num, den
}

// ceilDiv returns ceil(a/b) for a >= 0 and b > 0, saturating at
// math.MaxInt64.
//
// ceilDiv 对 a >= 0、b > 0 返回 ceil(a/b)，到 math.MaxInt64 饱和。
func ceilDiv(a, b *big.Int) int64 {
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return saturate(q)
}

// saturate converts a non-negative big integer to int64, capping it at
// math.MaxInt64.
//
// saturate 把非负大整数转换为 int64，上限为 math.MaxInt64。
func saturate(n *big.Int) int64 {
	if !n.IsInt64() {
		return math.MaxInt64
	}
	return n.Int64()
}
