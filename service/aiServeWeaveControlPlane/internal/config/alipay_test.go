package config_test

import (
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
)

func TestBillingNeedsOnePaymentMethod(t *testing.T) {
	cfg := validConfig()
	cfg.Billing.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "Billing.Alipay.Enabled") {
		t.Fatalf("billing without a provider: %v", err)
	}
}

func TestAlipayOnlyBillingNeedsNoDodo(t *testing.T) {
	cfg := validConfig()
	cfg.Billing = config.BillingConf{Enabled: true, Alipay: config.AlipayConf{
		Enabled: true, CreditsPerUnitMicro: 1_000_000, ReturnURL: "https://console.example/console/billing?tab=alipay",
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Billing.DodoConfigured() {
		t.Fatal("Dodo reported configured")
	}
	for name, mutate := range map[string]func(*config.AlipayConf){
		"zero rate":        func(a *config.AlipayConf) { a.CreditsPerUnitMicro = 0 },
		"huge rate":        func(a *config.AlipayConf) { a.CreditsPerUnitMicro = config.MaxAlipayCreditsPerUnitMicro + 1 },
		"http return":      func(a *config.AlipayConf) { a.ReturnURL = "http://console.example/billing" },
		"credentials":      func(a *config.AlipayConf) { a.ReturnURL = "https://u:p@console.example/billing" },
		"http base":        func(a *config.AlipayConf) { a.BaseURL = "http://127.0.0.1:9000" },
		"oversized return": func(a *config.AlipayConf) { a.ReturnURL = "https://console.example/" + strings.Repeat("a", 512) },
	} {
		bad := cfg
		mutate(&bad.Billing.Alipay)
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "Billing.Alipay") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDodoStillNeedsBothSecretsAndReturnURL(t *testing.T) {
	cfg := validConfig()
	cfg.Billing = config.BillingConf{Enabled: true, Dodo: config.DodoConf{APIKey: "dodo_test_x", Environment: "test"},
		Alipay: config.AlipayConf{Enabled: true, CreditsPerUnitMicro: 1}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "WebhookSecret") {
		t.Fatalf("half a Dodo account: %v", err)
	}
	cfg.Billing.Dodo.WebhookSecret = "whsec_x"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ReturnURL") {
		t.Fatalf("Dodo without a return URL: %v", err)
	}
	cfg.Billing.ReturnURL = "https://console.example/console/billing"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
