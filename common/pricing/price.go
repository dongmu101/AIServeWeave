package pricing

import (
	"errors"
	"fmt"
	"sort"
)

// Units. One credit is one US dollar and is MicroPerCredit micro-credits;
// a unit price is micro-credits per million units of its meter (per million
// tokens for a token meter). A rate is parts per million: RateOne is 1x.
//
// 单位。1 点 = 1 美元 = MicroPerCredit micro 点；单价是「micro 点 / 该计量项的百万
// 单位」（token 计量项就是每百万 token）。倍率以百万分之一为单位：RateOne 表示 1 倍。
const (
	MicroPerCredit = 1_000_000
	RateOne        = 1_000_000
	// MaxRate bounds a model rate or a plan factor: 100x.
	// MaxRate 限制模型倍率或方案系数：100 倍。
	MaxRate = 100 * RateOne
	// MaxPriceMicroPerM bounds one unit price: a million credits per
	// million units. It is a sanity check on operator input and imported
	// data, not a business rule.
	// MaxPriceMicroPerM 限制单条单价：每百万单位一百万点。它只是对运维输入与导入
	// 数据的合理性校验，不是业务规则。
	MaxPriceMicroPerM = 1_000_000 * MicroPerCredit
	// MaxVariantLen bounds a price variant (a service tier name), in bytes.
	// MaxVariantLen 限制单价变体（服务等级名称）的长度（字节）。
	MaxVariantLen = 32
	// MaxPriceItems bounds one price list.
	// MaxPriceItems 限制一张单价表的条数。
	MaxPriceItems = 256
)

// PriceItem is one unit price: for Meter, under Variant ("" is the default
// service tier), applying when a request's input total is above
// AboveInputTokens (0 is the base tier).
//
// PriceItem 是一条单价：适用于计量项 Meter、变体 Variant（"" 为默认服务等级），在一次
// 请求的输入总数大于 AboveInputTokens 时生效（0 为基础档）。
type PriceItem struct {
	Meter            string `json:"meter"`
	Variant          string `json:"variant,omitempty"`
	AboveInputTokens int64  `json:"above_input_tokens,omitempty"`
	PriceMicroPerM   int64  `json:"price_micro_per_m"`
}

// PriceKey identifies a PriceItem within a list.
//
// PriceKey 在一张单价表内标识一条 PriceItem。
type PriceKey struct {
	Meter            string
	Variant          string
	AboveInputTokens int64
}

// Key returns the item's key.
//
// Key 返回该条单价的键。
func (p PriceItem) Key() PriceKey {
	return PriceKey{Meter: p.Meter, Variant: p.Variant, AboveInputTokens: p.AboveInputTokens}
}

// PriceList is a set of unit prices with distinct keys: an alias's override,
// a plan's fixed price, the default price, or one catalog entry.
//
// PriceList 是一组键互不相同的单价：某个别名的覆盖价、某个方案的固定售价、默认价，
// 或价目表中的一个条目。
type PriceList []PriceItem

// ErrEmptyPriceList is returned by Validate for a list without items.
//
// ErrEmptyPriceList 是 Validate 对没有任何条目的单价表返回的错误。
var ErrEmptyPriceList = errors.New("a price list needs at least one price")

// Validate returns an error unless the list is non-empty, has at most
// MaxPriceItems items with distinct keys, every meter name and variant is
// well formed, every tier is non-negative and every price is within
// [0, MaxPriceMicroPerM].
//
// Validate 在单价表非空、条目不超过 MaxPriceItems 且键互不相同、每个计量项名称与变体
// 格式正确、档位非负、单价在 [0, MaxPriceMicroPerM] 内时返回 nil，否则返回错误。
func (l PriceList) Validate() error {
	if len(l) == 0 {
		return ErrEmptyPriceList
	}
	if len(l) > MaxPriceItems {
		return fmt.Errorf("at most %d prices", MaxPriceItems)
	}
	seen := make(map[PriceKey]bool, len(l))
	for _, item := range l {
		if !ValidMeterName(item.Meter) {
			return fmt.Errorf("invalid meter name %q", item.Meter)
		}
		if item.Variant != "" && (len(item.Variant) > MaxVariantLen || !validSegment(item.Variant, true)) {
			return fmt.Errorf("invalid variant %q", item.Variant)
		}
		if item.AboveInputTokens < 0 {
			return fmt.Errorf("meter %q has a negative tier", item.Meter)
		}
		if item.PriceMicroPerM < 0 || item.PriceMicroPerM > MaxPriceMicroPerM {
			return fmt.Errorf("meter %q has a price outside [0, %d]", item.Meter, int64(MaxPriceMicroPerM))
		}
		key := item.Key()
		if seen[key] {
			return fmt.Errorf("duplicate price for meter %q, variant %q, tier %d", item.Meter, item.Variant, item.AboveInputTokens)
		}
		seen[key] = true
	}
	return nil
}

// Sorted returns a copy of the list in a canonical order (meter, variant,
// tier), so equal lists compare and serialize identically.
//
// Sorted 返回按规范顺序（计量项、变体、档位）排列的副本，使相等的单价表比较与序列化
// 的结果完全相同。
func (l PriceList) Sorted() PriceList {
	out := append(PriceList(nil), l...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Meter != b.Meter {
			return a.Meter < b.Meter
		}
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		return a.AboveInputTokens < b.AboveInputTokens
	})
	return out
}

// ValidRate reports whether rate is a valid model rate or plan factor.
//
// ValidRate 报告 rate 是否是合法的模型倍率或方案系数。
func ValidRate(rate int64) bool {
	return rate >= 0 && rate <= MaxRate
}
