package config_test

import (
	"slices"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
)

func validEzfp() config.EzfpConf {
	return config.EzfpConf{
		Enabled: true, PID: 1001, PrivateKey: "MIIEvQ...", PublicKey: "MIIBIj...",
		NotifyURL: "https://cp.example/billing/v1/ezfp/notify",
		ReturnURL: "https://console.example/console/billing",
	}
}

func TestEzfpOnlyBillingNeedsNoDodo(t *testing.T) {
	cfg := validConfig()
	cfg.Billing = config.BillingConf{Enabled: true, Ezfp: validEzfp()}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Billing.DodoConfigured() {
		t.Fatal("Dodo reported configured")
	}
	if got := cfg.Billing.Ezfp.PayTypes(); !slices.Equal(got, []string{"alipay", "wxpay"}) {
		t.Fatalf("default types = %v", got)
	}
	cfg.Billing.Ezfp.Types = []string{"wxpay"}
	if err := cfg.Validate(); err != nil || !slices.Equal(cfg.Billing.Ezfp.PayTypes(), []string{"wxpay"}) {
		t.Fatalf("wxpay only: %v %v", err, cfg.Billing.Ezfp.PayTypes())
	}
	cfg.Billing.Ezfp.Types = []string{"alipay", "wxpay", "usdt"}
	if err := cfg.Validate(); err != nil || !slices.Equal(cfg.Billing.Ezfp.PayTypes(), []string{"alipay", "wxpay", "usdt"}) {
		t.Fatalf("with usdt: %v %v", err, cfg.Billing.Ezfp.PayTypes())
	}
}

func TestEzfpConfigIsChecked(t *testing.T) {
	for name, mutate := range map[string]func(*config.EzfpConf){
		"no pid":         func(e *config.EzfpConf) { e.PID = 0 },
		"no private key": func(e *config.EzfpConf) { e.PrivateKey = " " },
		"no public key":  func(e *config.EzfpConf) { e.PublicKey = "" },
		"unknown type":   func(e *config.EzfpConf) { e.Types = []string{"qqpay"} },
		"repeated type":  func(e *config.EzfpConf) { e.Types = []string{"alipay", "alipay"} },
		"no notify":      func(e *config.EzfpConf) { e.NotifyURL = "" },
		"http notify":    func(e *config.EzfpConf) { e.NotifyURL = "http://cp.example/billing/v1/ezfp/notify" },
		"notify query":   func(e *config.EzfpConf) { e.NotifyURL = "https://cp.example/billing/v1/ezfp/notify?x=1" },
		"empty query":    func(e *config.EzfpConf) { e.ReturnURL = "https://console.example/console/billing?" },
		"return frag":    func(e *config.EzfpConf) { e.ReturnURL = "https://console.example/console/billing#x" },
		"credentials":    func(e *config.EzfpConf) { e.ReturnURL = "https://u:p@console.example/console/billing" },
		"long return":    func(e *config.EzfpConf) { e.ReturnURL = "https://console.example/" + strings.Repeat("a", 512) },
		"http base":      func(e *config.EzfpConf) { e.BaseURL = "http://www.ezfp.cn/" },
	} {
		cfg := validConfig()
		cfg.Billing = config.BillingConf{Enabled: true, Ezfp: validEzfp()}
		mutate(&cfg.Billing.Ezfp)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "Billing.Ezfp") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDisabledEzfpIsNotChecked(t *testing.T) {
	cfg := validConfig()
	cfg.Billing = config.BillingConf{Enabled: true, Alipay: config.AlipayConf{Enabled: true, CreditsPerUnitMicro: 1},
		Ezfp: config.EzfpConf{Enabled: false, NotifyURL: "not a url"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
