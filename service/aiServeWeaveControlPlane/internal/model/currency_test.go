package model

import (
	"slices"
	"testing"
)

func TestValidCurrency(t *testing.T) {
	if !slices.IsSorted(SupportedCurrencies) {
		t.Fatal("SupportedCurrencies must stay sorted for the binary search")
	}
	for _, code := range []string{"USD", "CNY", "EUR", "JPY", "TWD", "HKD"} {
		if !ValidCurrency(code) {
			t.Errorf("ValidCurrency(%q) = false, want true", code)
		}
	}
	for _, code := range []string{"", "ISO", "CN", "cny", "Usd", "USDT", "XXX"} {
		if ValidCurrency(code) {
			t.Errorf("ValidCurrency(%q) = true, want false", code)
		}
	}
}
