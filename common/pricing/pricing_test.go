package pricing

import (
	"math"
	"strings"
	"testing"
)

func TestValidMeterName(t *testing.T) {
	valid := []string{"input", "output", "input.cache_read", "input.cache_write.1h", "request", "a", "x1.y_2"}
	for _, name := range valid {
		if !ValidMeterName(name) {
			t.Errorf("ValidMeterName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", ".", "input.", ".input", "input..x", "Input", "1input", "_input", "input-x", "input x", "输入",
		strings.Repeat("a", MaxMeterNameLen+1)}
	for _, name := range invalid {
		if ValidMeterName(name) {
			t.Errorf("ValidMeterName(%q) = true, want false", name)
		}
	}
	if !ValidMeterName(strings.Repeat("a", MaxMeterNameLen)) {
		t.Error("a name of exactly MaxMeterNameLen bytes must be valid")
	}
}

func TestParentAndRoot(t *testing.T) {
	cases := []struct {
		name, parent, root string
		hasParent          bool
	}{
		{"input", "", "input", false},
		{"input.cache_write", "input", "input", true},
		{"input.cache_write.1h", "input.cache_write", "input", true},
		{"image", "", "image", false},
	}
	for _, c := range cases {
		parent, ok := Parent(c.name)
		if parent != c.parent || ok != c.hasParent {
			t.Errorf("Parent(%q) = %q, %t; want %q, %t", c.name, parent, ok, c.parent, c.hasParent)
		}
		if got := Root(c.name); got != c.root {
			t.Errorf("Root(%q) = %q, want %q", c.name, got, c.root)
		}
	}
	if !IsTokenMeter(MeterCacheWrite1h) || !IsTokenMeter(MeterReasoning) || IsTokenMeter("image") {
		t.Error("IsTokenMeter misclassifies a meter")
	}
}

func TestValidateDetails(t *testing.T) {
	if err := ValidateDetails(nil); err != nil {
		t.Fatalf("nil details: %v", err)
	}
	if err := ValidateDetails(map[string]int64{MeterCacheRead: 10, MeterCacheWrite1h: 0}); err != nil {
		t.Fatalf("valid details: %v", err)
	}
	bad := []map[string]int64{
		{"Bad": 1},
		{MeterCacheRead: -1},
		{MeterInput: 5},
		{MeterOutput: 5},
	}
	for _, details := range bad {
		if ValidateDetails(details) == nil {
			t.Errorf("ValidateDetails(%v) = nil, want an error", details)
		}
	}
	tooMany := map[string]int64{}
	for i := 0; i <= MaxDetails; i++ {
		tooMany["input.m"+strings.Repeat("x", i)] = 1
	}
	if ValidateDetails(tooMany) == nil {
		t.Error("more than MaxDetails entries must be rejected")
	}
}

func TestSanitizeDetails(t *testing.T) {
	in := map[string]int64{
		MeterCacheRead:    600,
		MeterCacheWrite:   0, // zero: dropped, not counted as invalid
		"Bad":             3,
		MeterReasoning:    -1,
		MeterInput:        1000,
		MeterCacheWrite1h: 100,
	}
	out, dropped := SanitizeDetails(in)
	if dropped != 3 {
		t.Errorf("dropped = %d, want 3", dropped)
	}
	if len(out) != 2 || out[MeterCacheRead] != 600 || out[MeterCacheWrite1h] != 100 {
		t.Errorf("out = %v", out)
	}
	if err := ValidateDetails(out); err != nil {
		t.Errorf("sanitized details must validate: %v", err)
	}
	if out, dropped := SanitizeDetails(map[string]int64{MeterCacheRead: 0}); out != nil || dropped != 0 {
		t.Errorf("all-zero details: out = %v, dropped = %d; want nil, 0", out, dropped)
	}

	many := map[string]int64{}
	for i := 0; i < MaxDetails+3; i++ {
		many["input.m"+string(rune('a'+i%26))+strings.Repeat("z", i/26)] = 1
	}
	out, dropped = SanitizeDetails(many)
	if len(out) != MaxDetails || dropped != 3 {
		t.Fatalf("len(out) = %d, dropped = %d; want %d, 3", len(out), dropped, MaxDetails)
	}
	if _, ok := out["input.ma"]; !ok {
		t.Error("trimming must keep the lexically smallest names")
	}
}

func TestSanitizeAndValidateAttributes(t *testing.T) {
	out, dropped := SanitizeAttributes(map[string]string{
		"service_tier": "priority",
		"empty":        "",
		"Bad":          "x",
		"long":         strings.Repeat("v", MaxAttributeValLen+1),
	})
	if dropped != 2 || len(out) != 1 || out["service_tier"] != "priority" {
		t.Errorf("out = %v, dropped = %d", out, dropped)
	}
	if err := ValidateAttributes(out); err != nil {
		t.Errorf("sanitized attributes must validate: %v", err)
	}
	if ValidateAttributes(map[string]string{"1x": "v"}) == nil {
		t.Error("a key starting with a digit must be rejected")
	}
	if ValidateAttributes(map[string]string{"k": strings.Repeat("v", MaxAttributeValLen+1)}) == nil {
		t.Error("an over-long value must be rejected")
	}
}

func TestPriceListValidate(t *testing.T) {
	good := PriceList{
		{Meter: MeterInput, PriceMicroPerM: 3_000_000},
		{Meter: MeterInput, AboveInputTokens: 200_000, PriceMicroPerM: 6_000_000},
		{Meter: MeterOutput, Variant: "priority", PriceMicroPerM: 15_000_000},
		{Meter: MeterCacheRead, PriceMicroPerM: 0},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid list: %v", err)
	}
	bad := []PriceList{
		nil,
		{{Meter: "Bad", PriceMicroPerM: 1}},
		{{Meter: MeterInput, Variant: "Priority", PriceMicroPerM: 1}},
		{{Meter: MeterInput, AboveInputTokens: -1, PriceMicroPerM: 1}},
		{{Meter: MeterInput, PriceMicroPerM: -1}},
		{{Meter: MeterInput, PriceMicroPerM: MaxPriceMicroPerM + 1}},
		{{Meter: MeterInput, PriceMicroPerM: 1}, {Meter: MeterInput, PriceMicroPerM: 2}},
	}
	for i, list := range bad {
		if list.Validate() == nil {
			t.Errorf("bad[%d] = %v: Validate() = nil, want an error", i, list)
		}
	}
}

func TestPriceListSorted(t *testing.T) {
	list := PriceList{
		{Meter: MeterOutput, PriceMicroPerM: 1},
		{Meter: MeterInput, AboveInputTokens: 200_000, PriceMicroPerM: 2},
		{Meter: MeterInput, Variant: "flex", PriceMicroPerM: 3},
		{Meter: MeterInput, PriceMicroPerM: 4},
	}
	sorted := list.Sorted()
	want := []int64{4, 2, 3, 1}
	for i, item := range sorted {
		if item.PriceMicroPerM != want[i] {
			t.Fatalf("sorted = %v", sorted)
		}
	}
	if list[0].PriceMicroPerM != 1 {
		t.Error("Sorted must not modify the receiver")
	}
}

func TestValidRate(t *testing.T) {
	for _, rate := range []int64{0, RateOne, MaxRate} {
		if !ValidRate(rate) {
			t.Errorf("ValidRate(%d) = false", rate)
		}
	}
	for _, rate := range []int64{-1, MaxRate + 1} {
		if ValidRate(rate) {
			t.Errorf("ValidRate(%d) = true", rate)
		}
	}
}

func TestQuantities(t *testing.T) {
	got, adjusted := Quantities(1000, 50, map[string]int64{
		MeterCacheRead:    600,
		MeterCacheWrite:   300,
		MeterCacheWrite1h: 100,
		MeterReasoning:    20,
	})
	want := map[string]int64{MeterInput: 1000, MeterOutput: 50, MeterCacheRead: 600, MeterCacheWrite: 300, MeterCacheWrite1h: 100, MeterReasoning: 20}
	if adjusted || len(got) != len(want) {
		t.Fatalf("got %v (adjusted %t), want %v", got, adjusted, want)
	}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("%s = %d, want %d", name, got[name], n)
		}
	}

	// A child reported without its parent raises the parent, and the root.
	got, adjusted = Quantities(50, 0, map[string]int64{MeterCacheWrite1h: 100, MeterCacheRead: 30})
	if !adjusted || got[MeterCacheWrite] != 100 || got[MeterInput] != 130 || got[MeterCacheRead] != 30 {
		t.Errorf("got %v (adjusted %t)", got, adjusted)
	}
	if _, ok := got[MeterOutput]; ok {
		t.Error("a zero total must be left out")
	}

	// Zero and negative entries are ignored; a count meter is its own root.
	got, adjusted = Quantities(10, 5, map[string]int64{MeterCacheRead: 0, MeterReasoning: -3, "image": 2})
	if adjusted || got["image"] != 2 || len(got) != 3 {
		t.Errorf("got %v (adjusted %t)", got, adjusted)
	}

	// Sums saturate instead of wrapping.
	got, _ = Quantities(0, 0, map[string]int64{"input.a": math.MaxInt64, "input.b": 5})
	if got[MeterInput] != math.MaxInt64 {
		t.Errorf("input = %d, want MaxInt64", got[MeterInput])
	}
}
