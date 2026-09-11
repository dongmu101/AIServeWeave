package config_test

import (
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
)

func validConfig() config.Config {
	return config.Config{
		Database:       config.DatabaseConf{DSN: "postgres://example.invalid/aisw"},
		Redis:          config.RedisConf{Addr: "127.0.0.1:6379"},
		Auth:           config.AuthConf{AccessSecret: strings.Repeat("a", 32), AccessExpire: time.Hour},
		InternalToken:  strings.Repeat("i", 32),
		BootstrapToken: strings.Repeat("b", 32),
	}
}

func TestValidateRequiresRedisForRevocableSessions(t *testing.T) {
	cfg := validConfig()
	cfg.Redis.Addr = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "Redis.Addr") {
		t.Errorf("Validate error = %v, want one naming Redis.Addr", err)
	}
}

func TestValidateAcceptsConfiguredRedis(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Errorf("Validate error = %v, want nil", err)
	}
}
