package pricecatalog

import (
	"encoding/json"
	"errors"
	"testing"

	"AIServeWeave/common/pricing"
)

func priceOf(t *testing.T, e Entry, meter string, above int64) (Price, bool) {
	t.Helper()
	for _, p := range e.Prices {
		if p.Meter == meter && p.Variant == "" && p.AboveInputTokens == above {
			return p, true
		}
	}
	return Price{}, false
}

func mustEntry(t *testing.T, snap Snapshot, model string) Entry {
	t.Helper()
	for _, e := range snap.Entries {
		if e.Model == model {
			return e
		}
	}
	t.Fatalf("entry %q missing", model)
	return Entry{}
}

func TestMapField(t *testing.T) {
	cases := []struct {
		field string
		meter string
		above int64
		ok    bool
	}{
		{"input_cost_per_token", pricing.MeterInput, 0, true},
		{"output_cost_per_token", pricing.MeterOutput, 0, true},
		{"cache_read_input_token_cost", pricing.MeterCacheRead, 0, true},
		{"cache_creation_input_token_cost", pricing.MeterCacheWrite, 0, true},
		{"cache_creation_input_token_cost_above_1hr", pricing.MeterCacheWrite1h, 0, true},
		{"cache_creation_input_token_cost_above_12hr", pricing.MeterCacheWrite12h, 0, true},
		{"output_cost_per_reasoning_token", pricing.MeterReasoning, 0, true},
		{"input_cost_per_token_above_200k_tokens", pricing.MeterInput, 200_000, true},
		{"cache_read_input_token_cost_above_272k_tokens", pricing.MeterCacheRead, 272_000, true},
		{"cache_creation_input_token_cost_above_1hr_above_200k_tokens", pricing.MeterCacheWrite1h, 200_000, true},
		{"cache_creation_input_token_cost_above_12hr_above_272k_tokens", pricing.MeterCacheWrite12h, 272_000, true},
		// Service tiers, unknown suffixes and other units are not mapped.
		{"input_cost_per_token_batches", "", 0, false},
		{"input_cost_per_token_priority", "", 0, false},
		{"input_cost_per_token_above_200k_tokens_priority", "", 0, false},
		{"cache_creation_input_token_cost_above_12hr_flex", "", 0, false},
		{"input_cost_per_audio_token", "", 0, false},
		{"cache_read_input_token_cost_per_audio_token", "", 0, false},
		{"explicit_cache_read_input_token_cost", "", 0, false},
		{"input_cost_per_token_above_0k_tokens", "", 0, false},
		{"input_cost_per_token_above_k_tokens", "", 0, false},
		{"input_cost_per_token_above_1hr", "", 0, false},
		{"search_context_cost_per_query", "", 0, false},
	}
	for _, c := range cases {
		key, ok := mapField(c.field)
		if ok != c.ok || (ok && (key.Meter != c.meter || key.AboveInputTokens != c.above || key.Variant != "")) {
			t.Errorf("mapField(%q) = %+v, %t; want %s/%d, %t", c.field, key, ok, c.meter, c.above, c.ok)
		}
	}
}

func TestParseMapsAndFilters(t *testing.T) {
	doc := `{
	  "sample_spec": {"mode": "chat", "input_cost_per_token": 1},
	  "claude-x": {
	    "litellm_provider": "anthropic", "mode": "chat",
	    "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05,
	    "cache_read_input_token_cost": 3e-07,
	    "cache_creation_input_token_cost": 3.75e-06,
	    "cache_creation_input_token_cost_above_1hr": 6e-06,
	    "input_cost_per_token_above_200k_tokens": 6e-06,
	    "output_cost_per_token_above_200k_tokens": 2.25e-05,
	    "cache_read_input_token_cost_above_200k_tokens": 6e-07,
	    "input_cost_per_token_batches": 1.5e-06,
	    "search_context_cost_per_query": {"search_context_size_low": 0.01},
	    "supports_vision": true
	  },
	  "embed-x": {"mode": "embedding", "input_cost_per_token": 2e-08, "output_cost_per_token": 0},
	  "image-x": {"mode": "image_generation", "input_cost_per_token": 1e-06},
	  "free-x": {"mode": "chat", "input_cost_per_token": 0, "output_cost_per_token": 0},
	  "no-mode": {"input_cost_per_token": 1e-06},
	  "bad-value": {"mode": "chat", "input_cost_per_token": "cheap", "output_cost_per_token": 1e-06},
	  "zero-sub": {"mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06, "cache_read_input_token_cost": 0},
	  " spaced": {"mode": "chat", "input_cost_per_token": 1e-06}
	}`
	snap, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range snap.Entries {
		names = append(names, e.Model)
	}
	want := []string{"bad-value", "claude-x", "embed-x", "zero-sub"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v (sorted)", names, want)
		}
	}
	if snap.ContentHash == "" {
		t.Fatal("missing content hash")
	}

	claude := mustEntry(t, snap, "claude-x")
	if claude.Provider != "anthropic" || claude.Mode != "chat" {
		t.Fatalf("metadata = %q %q", claude.Provider, claude.Mode)
	}
	checks := []struct {
		meter   string
		above   int64
		micro   int64
		derived bool
	}{
		{pricing.MeterInput, 0, 3_000_000, false},
		{pricing.MeterOutput, 0, 15_000_000, false},
		{pricing.MeterCacheRead, 0, 300_000, false},
		{pricing.MeterCacheWrite, 0, 3_750_000, false},
		{pricing.MeterCacheWrite1h, 0, 6_000_000, false},
		{pricing.MeterInput, 200_000, 6_000_000, false},
		{pricing.MeterOutput, 200_000, 22_500_000, false},
		{pricing.MeterCacheRead, 200_000, 600_000, false},
		// Not listed upstream: derived from the input root's 2x ratio.
		{pricing.MeterCacheWrite, 200_000, 7_500_000, true},
		{pricing.MeterCacheWrite1h, 200_000, 12_000_000, true},
	}
	for _, c := range checks {
		p, ok := priceOf(t, claude, c.meter, c.above)
		if !ok || p.PriceMicroPerM != c.micro || p.Derived != c.derived {
			t.Errorf("%s@%d = %+v, %t; want %d derived=%t", c.meter, c.above, p, ok, c.micro, c.derived)
		}
	}
	if len(claude.Prices) != len(checks) {
		t.Errorf("claude-x has %d prices, want %d: %+v", len(claude.Prices), len(checks), claude.Prices)
	}
	if err := claude.PriceList().Validate(); err != nil {
		t.Errorf("parsed prices must validate: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(claude.Raw, &raw); err != nil || raw["supports_vision"] != true {
		t.Errorf("raw entry must be kept whole: %v %v", raw, err)
	}

	embed := mustEntry(t, snap, "embed-x")
	if p, ok := priceOf(t, embed, pricing.MeterOutput, 0); !ok || p.PriceMicroPerM != 0 {
		t.Errorf("a zero root price is kept: %+v %t", p, ok)
	}
	if p, ok := priceOf(t, embed, pricing.MeterInput, 0); !ok || p.PriceMicroPerM != 20_000 {
		t.Errorf("embedding input = %+v", p)
	}
	if _, ok := priceOf(t, mustEntry(t, snap, "zero-sub"), pricing.MeterCacheRead, 0); ok {
		t.Error("a zero sub-meter price must be dropped so it falls back to its parent")
	}
	bad := mustEntry(t, snap, "bad-value")
	if _, ok := priceOf(t, bad, pricing.MeterInput, 0); ok {
		t.Error("a non-numeric field must be skipped")
	}
}

func TestParseRejectsUnusableDocuments(t *testing.T) {
	if _, err := Parse([]byte(`[1, 2]`)); err == nil {
		t.Error("a non-object must be rejected")
	}
	if _, err := Parse([]byte(`<html>rate limited</html>`)); err == nil {
		t.Error("an HTML page must be rejected")
	}
	if _, err := Parse([]byte(`{"x": {"mode": "image_generation"}}`)); !errors.Is(err, ErrNoEntries) {
		t.Errorf("a document without usable entries: %v", err)
	}
}

func TestToMicroPerMRounding(t *testing.T) {
	cases := map[string]int64{
		"3e-06":     3_000_000,
		"1.5e-07":   150_000,
		"2.475e-05": 24_750_000,
		"1e-12":     1,
		"4e-13":     0,
		"0":         0,
	}
	for in, want := range cases {
		got, ok := toMicroPerM(json.RawMessage(in))
		if !ok || got != want {
			t.Errorf("toMicroPerM(%s) = %d, %t; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"-1e-06", `"1e-06"`, "null", "{}", "1e10"} {
		if _, ok := toMicroPerM(json.RawMessage(in)); ok {
			t.Errorf("toMicroPerM(%s) must be rejected", in)
		}
	}
}

func TestDeriveTiersSkipsWithoutABase(t *testing.T) {
	prices := map[pricing.PriceKey]int64{
		{Meter: pricing.MeterOutput}:                            0,
		{Meter: pricing.MeterOutput, AboveInputTokens: 200_000}: 5,
		{Meter: pricing.MeterReasoning}:                         7,
	}
	if derived := deriveTiers(prices); len(derived) != 0 {
		t.Fatalf("a zero base root must not derive tiers: %v", derived)
	}
	prices[pricing.PriceKey{Meter: pricing.MeterOutput}] = 3
	derived := deriveTiers(prices)
	key := pricing.PriceKey{Meter: pricing.MeterReasoning, AboveInputTokens: 200_000}
	if !derived[key] || prices[key] != 12 { // round(7 × 5 / 3) = round(11.67)
		t.Fatalf("derived = %v, price = %d", derived, prices[key])
	}
}

func TestPricesHashIgnoresOrderAndRaw(t *testing.T) {
	a := Entry{Model: "m", Raw: json.RawMessage(`{"a":1}`), Prices: []Price{
		{PriceItem: pricing.PriceItem{Meter: pricing.MeterOutput, PriceMicroPerM: 2}},
		{PriceItem: pricing.PriceItem{Meter: pricing.MeterInput, PriceMicroPerM: 1}},
	}}
	b := Entry{Model: "m", Raw: json.RawMessage(`{"b":2}`), Prices: []Price{
		{PriceItem: pricing.PriceItem{Meter: pricing.MeterInput, PriceMicroPerM: 1}},
		{PriceItem: pricing.PriceItem{Meter: pricing.MeterOutput, PriceMicroPerM: 2}},
	}}
	if a.PricesHash() != b.PricesHash() {
		t.Fatal("the hash must not depend on order or raw fields")
	}
	b.Prices[1].Derived = true
	if a.PricesHash() == b.PricesHash() {
		t.Fatal("the derived mark is part of the hash")
	}
}

// TestEmbeddedSnapshot parses the built-in snapshot: it must parse, keep
// every entry Trim kept, and be stable under Trim again.
func TestEmbeddedSnapshot(t *testing.T) {
	snap, err := Parse(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) < 1000 {
		t.Fatalf("the built-in snapshot has only %d entries", len(snap.Entries))
	}
	for _, e := range snap.Entries {
		if err := e.PriceList().Validate(); err != nil {
			t.Fatalf("%s: %v", e.Model, err)
		}
	}
	again, err := Trim(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(Embedded()) {
		t.Fatal("Trim of the built-in snapshot must reproduce it; regenerate with scripts/update-price-catalog.sh")
	}
}
