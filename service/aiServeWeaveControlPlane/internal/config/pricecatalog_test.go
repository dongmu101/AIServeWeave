package config_test

import (
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
)

func TestPriceCatalogConfigIsChecked(t *testing.T) {
	base := validConfig()
	base.Billing = config.BillingConf{Enabled: true, Ezfp: validEzfp()}
	if err := base.Validate(); err != nil {
		t.Fatalf("zero price catalog settings must be valid: %v", err)
	}
	good := []config.PriceCatalogConf{
		{URL: "https://api.github.com/repos/BerriAI/litellm/contents/model_prices_and_context_window.json", RefreshInterval: 24 * time.Hour, AutoApplyMaxChangePercent: 30},
		{URL: "http://mirror.internal/prices.json", RefreshInterval: 10 * time.Minute},
		{AutoApplyMaxChangePercent: 1000},
	}
	for _, pc := range good {
		cfg := base
		cfg.Billing.PriceCatalog = pc
		if err := cfg.Validate(); err != nil {
			t.Errorf("%+v: %v", pc, err)
		}
	}
	bad := map[string]config.PriceCatalogConf{
		"URL":                       {URL: "ftp://example.com/x.json"},
		"credentials":               {URL: "https://user:pass@example.com/x.json"},
		"relative":                  {URL: "/x.json"},
		"long":                      {URL: "https://example.com/" + strings.Repeat("x", 512)},
		"RefreshInterval":           {RefreshInterval: time.Minute},
		"negative interval":         {RefreshInterval: -time.Hour},
		"AutoApplyMaxChangePercent": {AutoApplyMaxChangePercent: 1001},
		"negative percent":          {AutoApplyMaxChangePercent: -1},
	}
	for name, pc := range bad {
		cfg := base
		cfg.Billing.PriceCatalog = pc
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "Billing.PriceCatalog") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Billing off: the settings are not checked.
	off := validConfig()
	off.Billing.PriceCatalog = config.PriceCatalogConf{RefreshInterval: time.Second}
	if err := off.Validate(); err != nil {
		t.Errorf("billing off: %v", err)
	}
}
