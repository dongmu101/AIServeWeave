package pricecatalog

import (
	"bytes"
	"sort"

	"AIServeWeave/common/pricing"
)

// Entry statuses, as stored. An entry upstream stopped listing keeps its
// prices as removed_upstream until an operator approves the removal, which
// makes it removed.
//
// 条目状态（与存储一致）。上游不再列出的条目以 removed_upstream 保留原价，直到运维批准
// 删除，此后变为 removed。
const (
	StatusActive          = "active"
	StatusRemovedUpstream = "removed_upstream"
	StatusRemoved         = "removed"
)

// Pending review kinds.
//
// 待审核的类型。
const (
	PendingChange = "change"
	PendingRemove = "remove"
)

// UnboundedChange is the change, in basis points, reported for a price that
// rises from zero or has no previous price to compare with.
//
// UnboundedChange 是单价从 0 上涨、或者没有可比较的原价时报告的变动（基点）。
const UnboundedChange = -1

// Current is what the store holds for one entry when a snapshot is compared
// with it: its status, its prices in effect, the hash of an upstream change
// an operator rejected, and the kind of review waiting on it, if any.
//
// Current 是比较快照时 store 中某个条目的现状：状态、生效中的单价、运维拒绝过的上游变动
// 的哈希，以及等待中的审核类型（如有）。
type Current struct {
	Model        string
	Provider     string
	Mode         string
	Raw          []byte
	Status       string
	Prices       []Price
	RejectedHash string
	Pending      string
	PendingHash  string
}

// Options tunes Diff.
//
// Options 调整 Diff 的行为。
type Options struct {
	// MaxChangePercent is the largest relative change of an existing price
	// that takes effect on its own; 0 sends every price change to review.
	//
	// MaxChangePercent 是已有单价可以自动生效的最大相对变动；0 表示所有改价都要审核。
	MaxChangePercent int
}

// Change is an entry whose new prices replace the ones in effect, with the
// largest relative change among them in basis points (UnboundedChange when
// a price rises from zero or has nothing to compare with).
//
// Change 是新单价将取代生效中单价的条目，附带其中最大的相对变动（基点；单价从 0 上涨或
// 没有可比较的原价时为 UnboundedChange）。
type Change struct {
	Entry       Entry
	MaxChangeBP int64
}

// Plan is what Diff decided. Each model appears in at most one of Add,
// Apply, Metadata, Review, Remove and Reactivate; Unpend may accompany any
// of them but Review and Remove.
//
// Plan 是 Diff 的判断结果。每个模型最多出现在 Add、Apply、Metadata、Review、Remove、
// Reactivate 之一中；Unpend 可以与除 Review、Remove 以外的任何一类同时出现。
type Plan struct {
	// Add are entries the store has no live copy of; they take effect now.
	// Add 是 store 中没有有效副本的条目，立即生效。
	Add []Entry
	// Apply are existing entries whose prices changed within the limits.
	// Apply 是单价变动在限度之内的已有条目。
	Apply []Change
	// Metadata are existing entries whose prices did not change but whose
	// provider, mode, raw entry or status did.
	// Metadata 是单价未变、但服务商、模式、原始条目或状态有变化的已有条目。
	Metadata []Entry
	// Review are existing entries whose change waits for an operator.
	// Review 是变动需要等待运维审核的已有条目。
	Review []Change
	// Remove are active entries upstream no longer lists.
	// Remove 是上游不再列出的生效条目。
	Remove []string
	// Unpend are models whose waiting review no longer applies.
	// Unpend 是等待中的审核已不再适用的模型。
	Unpend []string
	// Reactivate are entries upstream lists again whose prices stay as they
	// are (the upstream prices were rejected before); only their status
	// returns to active.
	// Reactivate 是上游重新列出、但单价保持不变（上游单价此前被拒绝过）的条目；只把它们
	// 的状态恢复为 active。
	Reactivate []string
	// Suppressed counts changes skipped because an operator rejected the
	// same upstream prices before.
	// Suppressed 统计因运维此前拒绝过相同上游单价而跳过的变动数。
	Suppressed int
}

// Diff compares a snapshot with the store's current entries. New entries
// take effect; a change to an existing entry takes effect unless a price is
// removed, falls to zero, rises from zero or moves by more than
// MaxChangePercent, in which case it waits for review — unless an operator
// already rejected exactly these prices. Active entries missing from the
// snapshot wait for review as removals and keep their prices meanwhile.
//
// Diff 比较一份快照与 store 中的现有条目。新条目立即生效；已有条目的变动会生效，除非有
// 单价被删除、降为 0、从 0 上涨或变动超过 MaxChangePercent，此时要等待审核——除非运维已经
// 拒绝过完全相同的单价。快照中缺少的生效条目作为删除等待审核，期间保留原价。
func Diff(current map[string]Current, snap Snapshot, opts Options) Plan {
	var plan Plan
	listed := make(map[string]bool, len(snap.Entries))
	for _, entry := range snap.Entries {
		listed[entry.Model] = true
		cur, known := current[entry.Model]
		if !known || cur.Status == StatusRemoved {
			plan.Add = append(plan.Add, entry)
			if known && cur.Pending != "" {
				plan.Unpend = append(plan.Unpend, entry.Model)
			}
			continue
		}
		if samePrices(cur.Prices, entry.Prices) {
			if cur.Status != StatusActive || cur.Provider != entry.Provider || cur.Mode != entry.Mode || !bytes.Equal(cur.Raw, entry.Raw) {
				plan.Metadata = append(plan.Metadata, entry)
			}
			if cur.Pending != "" {
				plan.Unpend = append(plan.Unpend, entry.Model)
			}
			continue
		}
		review, bp := Assess(cur.Prices, entry.Prices, opts.MaxChangePercent)
		hash := entry.PricesHash()
		switch {
		case !review:
			plan.Apply = append(plan.Apply, Change{Entry: entry, MaxChangeBP: bp})
			if cur.Pending != "" {
				plan.Unpend = append(plan.Unpend, entry.Model)
			}
		case hash == cur.RejectedHash:
			plan.Suppressed++
			if cur.Pending != "" {
				plan.Unpend = append(plan.Unpend, entry.Model)
			}
			if cur.Status != StatusActive {
				plan.Reactivate = append(plan.Reactivate, entry.Model)
			}
		case cur.Pending == PendingChange && cur.PendingHash == hash:
			// Already waiting for exactly this change.
			// 已经在等待完全相同的变动。
		default:
			plan.Review = append(plan.Review, Change{Entry: entry, MaxChangeBP: bp})
		}
	}
	missing := make([]string, 0)
	for name, cur := range current {
		if !listed[name] && cur.Status == StatusActive {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	plan.Remove = missing
	return plan
}

// Assess reports whether replacing old with next needs an operator, and the
// largest relative change among the prices in basis points
// (UnboundedChange when one has nothing finite to compare with). A price
// new to the entry is compared with the price it used to fall back to.
//
// Assess 报告用 next 取代 old 是否需要运维审核，以及各单价中最大的相对变动（基点；有单价
// 无法与有限值比较时为 UnboundedChange）。条目中新出现的单价，与它原先回落到的单价比较。
func Assess(old, next []Price, maxChangePercent int) (bool, int64) {
	oldIndex := index(old)
	nextIndex := index(next)
	review := false
	var maxBP int64
	unbounded := false
	note := func(before, after int64) {
		if before == after {
			return
		}
		switch {
		case after == 0:
			review = true
			maxBP = max(maxBP, 10_000)
		case before == 0:
			review, unbounded = true, true
		default:
			delta := after - before
			if delta < 0 {
				delta = -delta
			}
			// delta × 100 > limit × before, in exact integers; both sides
			// are bounded by pricing.MaxPriceMicroPerM × 1000.
			// 以精确整数比较 delta × 100 > limit × before；两边都不超过
			// pricing.MaxPriceMicroPerM × 1000。
			if delta*100 > int64(maxChangePercent)*before {
				review = true
			}
			maxBP = max(maxBP, (delta*10_000+before-1)/before)
		}
	}
	for key, before := range oldIndex {
		after, kept := nextIndex[key]
		if !kept {
			review = true
			maxBP = max(maxBP, 10_000)
			continue
		}
		note(before.PriceMicroPerM, after.PriceMicroPerM)
	}
	for key, after := range nextIndex {
		if _, existed := oldIndex[key]; existed {
			continue
		}
		before, ok := resolve(oldIndex, key, true)
		if !ok {
			review, unbounded = true, true
			continue
		}
		note(before, after.PriceMicroPerM)
	}
	if unbounded {
		return review, UnboundedChange
	}
	return review, maxBP
}

// resolve looks up the price that applies at key in idx the way the pricing
// engine does: the same meter at the highest tier not above key's (strictly
// below when excludeExact), then the parent meter, up to the root.
//
// resolve 按计价引擎的方式在 idx 中查找 key 处适用的单价：同一计量项在不高于 key 的
// 最高一档（excludeExact 时严格低于），然后是父项，直到根。
func resolve(idx map[pricing.PriceKey]Price, key pricing.PriceKey, excludeExact bool) (int64, bool) {
	meter := key.Meter
	for {
		best, found := int64(-1), false
		var price int64
		for candidate, p := range idx {
			if candidate.Meter != meter || candidate.Variant != key.Variant {
				continue
			}
			if candidate.AboveInputTokens > key.AboveInputTokens ||
				(excludeExact && meter == key.Meter && candidate.AboveInputTokens == key.AboveInputTokens) {
				continue
			}
			if candidate.AboveInputTokens > best {
				best, price, found = candidate.AboveInputTokens, p.PriceMicroPerM, true
			}
		}
		if found {
			return price, true
		}
		parent, ok := pricing.Parent(meter)
		if !ok {
			return 0, false
		}
		meter = parent
	}
}

// samePrices reports whether a and b hold the same prices and marks.
//
// samePrices 报告 a 与 b 的单价与推算标记是否完全相同。
func samePrices(a, b []Price) bool {
	if len(a) != len(b) {
		return false
	}
	ai, bi := index(a), index(b)
	for key, p := range ai {
		q, ok := bi[key]
		if !ok || p.PriceMicroPerM != q.PriceMicroPerM || p.Derived != q.Derived {
			return false
		}
	}
	return true
}

// index maps prices by key.
//
// index 按键映射单价。
func index(prices []Price) map[pricing.PriceKey]Price {
	out := make(map[pricing.PriceKey]Price, len(prices))
	for _, p := range prices {
		out[p.Key()] = p
	}
	return out
}

// Index is index for callers outside the package.
//
// Index 是供包外调用方使用的 index。
func Index(prices []Price) map[pricing.PriceKey]Price { return index(prices) }
