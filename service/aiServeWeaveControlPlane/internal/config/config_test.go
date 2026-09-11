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

func TestMetricsHistoryConfValidation(t *testing.T) {
	// conf.Load applies the `default=` struct tags at YAML-load time, not on
	// a plain Go struct literal, so a test constructing one directly must set
	// Interval/Retention itself to simulate what a loaded config would carry.
	//
	// conf.Load 在从 YAML 加载时才应用 `default=` 结构体标签，而不是在普通 Go
	// 结构体字面量上，因此直接构造字面量的测试要自己设置 Interval/Retention，
	// 模拟一份真正加载出来的配置会携带的值。
	cfg := validConfig()
	cfg.MetricsHistory = config.MetricsHistoryConf{RegistryAddr: "http://registry:9091", Interval: 5 * time.Minute, Retention: 2160 * time.Hour}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a MetricsHistory with only RegistryAddr set", err)
	}

	cfg.MetricsHistory.Interval = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error for a negative Interval")
	}
}

func TestMetricsHistoryConfDisabledByDefault(t *testing.T) {
	var m config.MetricsHistoryConf
	if m.Enabled() {
		t.Error("Enabled() = true, want false for the zero value")
	}
}
