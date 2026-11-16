package config_test

import (
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
)

func TestLedgerTimezoneIsChecked(t *testing.T) {
	base := validConfig()
	base.Billing = config.BillingConf{Enabled: true, Ezfp: validEzfp()}
	for _, tz := range []string{"", "UTC", "Asia/Taipei", "Asia/Shanghai", "America/New_York"} {
		cfg := base
		cfg.Billing.LedgerTimezone = tz
		if err := cfg.Validate(); err != nil {
			t.Errorf("%q: %v", tz, err)
		}
		if loc, err := cfg.Billing.LedgerLocation(); err != nil || loc == nil {
			t.Errorf("%q: LedgerLocation = %v, %v", tz, loc, err)
		}
	}
	for _, tz := range []string{"Asia/Nowhere", "+08:00", "taipei"} {
		cfg := base
		cfg.Billing.LedgerTimezone = tz
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "LedgerTimezone") {
			t.Errorf("%q: Validate() = %v, want a LedgerTimezone error", tz, err)
		}
	}
}
