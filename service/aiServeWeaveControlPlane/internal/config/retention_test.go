package config_test

import (
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
)

func TestRetentionDefaults(t *testing.T) {
	if config.DefaultResponseTurnRetention != 30*24*time.Hour {
		t.Errorf("DefaultResponseTurnRetention = %v, want 30 days", config.DefaultResponseTurnRetention)
	}
	if config.DefaultAuditLogRetention != 400*24*time.Hour {
		t.Errorf("DefaultAuditLogRetention = %v, want 400 days", config.DefaultAuditLogRetention)
	}
	if err := validConfig().Validate(); err != nil {
		t.Errorf("zero retentions (defaults) should validate: %v", err)
	}
}

func TestValidateRejectsNegativeRetentionsAndMigrationTimeout(t *testing.T) {
	for name, mutate := range map[string]func(*config.Config){
		"ResponseTurnRetention":     func(c *config.Config) { c.ResponseTurnRetention = -time.Hour },
		"AuditLogRetention":         func(c *config.Config) { c.AuditLogRetention = -time.Hour },
		"Database.MigrationTimeout": func(c *config.Config) { c.Database.MigrationTimeout = -time.Second },
	} {
		cfg := validConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: Validate error = %v, want one naming it", name, err)
		}
	}
}

func TestValidateCapsDailyPartitionedRetention(t *testing.T) {
	tooLong := config.MaxDailyPartitionedRetention + 24*time.Hour
	for name, mutate := range map[string]func(*config.Config){
		"RequestLogRetention":   func(c *config.Config) { c.RequestLogRetention = tooLong },
		"ResponseTurnRetention": func(c *config.Config) { c.ResponseTurnRetention = tooLong },
		"MetricsHistory.Retention": func(c *config.Config) {
			c.MetricsHistory = config.MetricsHistoryConf{RegistryAddr: "http://registry:9091", Interval: time.Minute, Retention: tooLong}
		},
	} {
		cfg := validConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "3650") {
			t.Errorf("%s: Validate error = %v, want one naming it and the 3650-day cap", name, err)
		}
	}
	cfg := validConfig()
	cfg.RequestLogRetention = config.MaxDailyPartitionedRetention
	cfg.UsageRecordRetention = 2 * config.MaxDailyPartitionedRetention
	cfg.AuditLogRetention = 2 * config.MaxDailyPartitionedRetention
	if err := cfg.Validate(); err != nil {
		t.Errorf("the cap itself and monthly-partitioned tables beyond it should validate: %v", err)
	}
}
