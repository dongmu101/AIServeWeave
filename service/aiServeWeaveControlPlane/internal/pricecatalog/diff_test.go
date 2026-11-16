package pricecatalog

import (
	"encoding/json"
	"testing"

	"AIServeWeave/common/pricing"
)

func p(meter string, above, micro int64) Price {
	return Price{PriceItem: pricing.PriceItem{Meter: meter, AboveInputTokens: above, PriceMicroPerM: micro}}
}

func entry(model string, prices ...Price) Entry {
	return Entry{Model: model, Provider: "prov", Mode: "chat", Prices: sortPrices(prices), Raw: json.RawMessage(`{"mode":"chat"}`)}
}

func current(e Entry) Current {
	return Current{Model: e.Model, Provider: e.Provider, Mode: e.Mode, Raw: e.Raw, Status: StatusActive, Prices: e.Prices}
}

func TestAssess(t *testing.T) {
	base := []Price{p(pricing.MeterInput, 0, 1000), p(pricing.MeterOutput, 0, 2000), p(pricing.MeterCacheRead, 0, 100)}
	cases := []struct {
		name   string
		next   []Price
		limit  int
		review bool
		bp     int64
	}{
		{"within limit", []Price{p(pricing.MeterInput, 0, 1300), p(pricing.MeterOutput, 0, 2000), p(pricing.MeterCacheRead, 0, 100)}, 30, false, 3000},
		{"exactly the limit", []Price{p(pricing.MeterInput, 0, 700), p(pricing.MeterOutput, 0, 2000), p(pricing.MeterCacheRead, 0, 100)}, 30, false, 3000},
		{"over the limit", []Price{p(pricing.MeterInput, 0, 1301), p(pricing.MeterOutput, 0, 2000), p(pricing.MeterCacheRead, 0, 100)}, 30, true, 3010},
		{"limit zero", []Price{p(pricing.MeterInput, 0, 1001), p(pricing.MeterOutput, 0, 2000), p(pricing.MeterCacheRead, 0, 100)}, 0, true, 10},
		{"falls to zero", []Price{p(pricing.MeterInput, 0, 0), p(pricing.MeterOutput, 0, 2000), p(pricing.MeterCacheRead, 0, 100)}, 100, true, 10_000},
		{"price removed", []Price{p(pricing.MeterInput, 0, 1000), p(pricing.MeterOutput, 0, 2000)}, 100, true, 10_000},
		// A new 1h price is compared with the price it used to fall back to
		// (cache_write is absent, so input: 1000 → 1200 is 20%).
		{"new price within limit", append(append([]Price{}, base...), p(pricing.MeterCacheWrite1h, 0, 1200)), 30, false, 2000},
		{"new price over limit", append(append([]Price{}, base...), p(pricing.MeterCacheWrite1h, 0, 2000)), 30, true, 10_000},
		// A new tier is compared with the same meter's base price.
		{"new tier", append(append([]Price{}, base...), p(pricing.MeterInput, 200_000, 2000)), 30, true, 10_000},
		{"new tier equal to fallback", append(append([]Price{}, base...), p(pricing.MeterCacheRead, 200_000, 100)), 30, false, 0},
		{"new count meter", append(append([]Price{}, base...), p("image", 0, 5)), 30, true, UnboundedChange},
	}
	for _, c := range cases {
		review, bp := Assess(base, c.next, c.limit)
		if review != c.review || bp != c.bp {
			t.Errorf("%s: Assess = %t, %d; want %t, %d", c.name, review, bp, c.review, c.bp)
		}
	}
	if review, bp := Assess([]Price{p(pricing.MeterInput, 0, 0)}, []Price{p(pricing.MeterInput, 0, 5)}, 1000); !review || bp != UnboundedChange {
		t.Errorf("rising from zero = %t, %d", review, bp)
	}
}

func TestDiff(t *testing.T) {
	same := entry("same", p(pricing.MeterInput, 0, 1000), p(pricing.MeterOutput, 0, 2000))
	small := entry("small", p(pricing.MeterInput, 0, 1000), p(pricing.MeterOutput, 0, 2000))
	big := entry("big", p(pricing.MeterInput, 0, 1000), p(pricing.MeterOutput, 0, 2000))
	rejected := entry("rejected", p(pricing.MeterInput, 0, 1000))
	gone := entry("gone", p(pricing.MeterInput, 0, 1000))
	goneBefore := entry("gone-before", p(pricing.MeterInput, 0, 1000))
	back := entry("back", p(pricing.MeterInput, 0, 1000))
	retired := entry("retired", p(pricing.MeterInput, 0, 1000))
	meta := entry("meta", p(pricing.MeterInput, 0, 1000))
	waiting := entry("waiting", p(pricing.MeterInput, 0, 1000))

	rejectedNext := entry("rejected", p(pricing.MeterInput, 0, 5000))
	waitingNext := entry("waiting", p(pricing.MeterInput, 0, 9000))
	cur := map[string]Current{}
	for _, e := range []Entry{same, small, big, rejected, gone, goneBefore, back, retired, meta, waiting} {
		cur[e.Model] = current(e)
	}
	c := cur["rejected"]
	c.RejectedHash = rejectedNext.PricesHash()
	c.Pending, c.PendingHash = PendingChange, "older"
	cur["rejected"] = c
	c = cur["gone-before"]
	c.Status = StatusRemovedUpstream
	cur["gone-before"] = c
	c = cur["back"]
	c.Status, c.Pending = StatusRemovedUpstream, PendingRemove
	cur["back"] = c
	c = cur["retired"]
	c.Status = StatusRemoved
	cur["retired"] = c
	c = cur["waiting"]
	c.Pending, c.PendingHash = PendingChange, waitingNext.PricesHash()
	cur["waiting"] = c

	metaNext := meta
	metaNext.Raw = json.RawMessage(`{"mode":"chat","max_tokens":10}`)
	snap := Snapshot{Entries: []Entry{
		same,
		entry("small", p(pricing.MeterInput, 0, 1100), p(pricing.MeterOutput, 0, 2000)),
		entry("big", p(pricing.MeterInput, 0, 1000), p(pricing.MeterOutput, 0, 4000)),
		rejectedNext,
		back,
		retired,
		metaNext,
		waitingNext,
		entry("new", p(pricing.MeterInput, 0, 7)),
	}}
	plan := Diff(cur, snap, Options{MaxChangePercent: 30})

	names := func(entries []Entry) []string {
		var out []string
		for _, e := range entries {
			out = append(out, e.Model)
		}
		return out
	}
	changes := func(cs []Change) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Entry.Model)
		}
		return out
	}
	expect := func(what string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s = %v, want %v", what, got, want)
			return
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s = %v, want %v", what, got, want)
				return
			}
		}
	}
	expect("Add", names(plan.Add), "retired", "new")
	expect("Apply", changes(plan.Apply), "small")
	expect("Metadata", names(plan.Metadata), "back", "meta")
	expect("Review", changes(plan.Review), "big")
	expect("Remove", plan.Remove, "gone")
	expect("Unpend", plan.Unpend, "rejected", "back")
	if plan.Suppressed != 1 {
		t.Errorf("Suppressed = %d, want 1", plan.Suppressed)
	}
	if plan.Apply[0].MaxChangeBP != 1000 || plan.Review[0].MaxChangeBP != 10_000 {
		t.Errorf("change sizes = %d, %d", plan.Apply[0].MaxChangeBP, plan.Review[0].MaxChangeBP)
	}
}

func TestDiffReactivatesASuppressedReturn(t *testing.T) {
	old := entry("m", p(pricing.MeterInput, 0, 1000))
	next := entry("m", p(pricing.MeterInput, 0, 9000))
	cur := current(old)
	cur.Status, cur.Pending, cur.RejectedHash = StatusRemovedUpstream, PendingRemove, next.PricesHash()
	plan := Diff(map[string]Current{"m": cur}, Snapshot{Entries: []Entry{next}}, Options{MaxChangePercent: 30})
	if plan.Suppressed != 1 || len(plan.Reactivate) != 1 || len(plan.Unpend) != 1 || len(plan.Review) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestDiffFirstImportAddsEverything(t *testing.T) {
	snap := Snapshot{Entries: []Entry{entry("a", p(pricing.MeterInput, 0, 1)), entry("b", p(pricing.MeterOutput, 0, 2))}}
	plan := Diff(nil, snap, Options{})
	if len(plan.Add) != 2 || len(plan.Apply)+len(plan.Review)+len(plan.Remove)+len(plan.Metadata) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}
