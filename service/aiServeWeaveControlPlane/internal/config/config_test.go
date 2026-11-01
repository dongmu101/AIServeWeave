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

func TestModelPullConfValidation(t *testing.T) {
	cfg := validConfig()
	cfg.ModelPull = config.ModelPullConf{Gateways: []string{"http://gateway-1:8092"}, GatewayToken: strings.Repeat("m", 32)}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a fully configured ModelPull", err)
	}

	cfg.ModelPull.GatewayToken = "too-short"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ModelPull.GatewayToken") {
		t.Errorf("Validate error = %v, want one naming ModelPull.GatewayToken", err)
	}

	cfg.ModelPull = config.ModelPullConf{GatewayToken: strings.Repeat("m", 32)}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ModelPull.Gateways") {
		t.Errorf("Validate error = %v, want one naming ModelPull.Gateways for a token with no endpoints", err)
	}
}

func TestModelPullConfDisabledByDefault(t *testing.T) {
	var m config.ModelPullConf
	if m.Enabled() {
		t.Error("Enabled() = true, want false for the zero value")
	}
}

func TestComfyUIManagedConfValidation(t *testing.T) {
	cfg := validConfig()
	cfg.ComfyUIManaged = config.ComfyUIManagedConf{Gateways: []string{"http://gateway-1:8093"}, GatewayToken: strings.Repeat("c", 32)}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a fully configured ComfyUIManaged", err)
	}

	cfg.ComfyUIManaged.GatewayToken = "too-short"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ComfyUIManaged.GatewayToken") {
		t.Errorf("Validate error = %v, want one naming ComfyUIManaged.GatewayToken", err)
	}

	cfg.ComfyUIManaged = config.ComfyUIManagedConf{GatewayToken: strings.Repeat("c", 32)}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ComfyUIManaged.Gateways") {
		t.Errorf("Validate error = %v, want one naming ComfyUIManaged.Gateways for a token with no endpoints", err)
	}
}

func TestComfyUIManagedConfDisabledByDefault(t *testing.T) {
	var c config.ComfyUIManagedConf
	if c.Enabled() {
		t.Error("Enabled() = true, want false for the zero value")
	}
}

func TestAgentInstallConfValidation(t *testing.T) {
	registry := config.RegistryConf{Addr: "registry:9443", AdminToken: strings.Repeat("r", 32), CACertFile: "/etc/aisw/ca.pem"}
	for _, tc := range []struct {
		name      string
		registry  config.RegistryConf
		install   config.AgentInstallConf
		wantError string
	}{
		{name: "omitted block", install: config.AgentInstallConf{}},
		{name: "https origin", registry: registry, install: config.AgentInstallConf{PublicURL: "https://open7x.com", RedeemPerMinute: 60}},
		{name: "trailing slash", registry: registry, install: config.AgentInstallConf{PublicURL: "https://open7x.com/", RedeemPerMinute: 60}},
		{name: "http refused", registry: registry, install: config.AgentInstallConf{PublicURL: "http://open7x.com", RedeemPerMinute: 60}, wantError: "https origin"},
		{name: "path refused", registry: registry, install: config.AgentInstallConf{PublicURL: "https://open7x.com/agent", RedeemPerMinute: 60}, wantError: "https origin"},
		{name: "userinfo refused", registry: registry, install: config.AgentInstallConf{PublicURL: "https://u:p@open7x.com", RedeemPerMinute: 60}, wantError: "https origin"},
		{name: "needs registry", install: config.AgentInstallConf{PublicURL: "https://open7x.com", RedeemPerMinute: 60}, wantError: "Registry"},
		{name: "limit too high", registry: registry, install: config.AgentInstallConf{RedeemPerMinute: 10001}, wantError: "RedeemPerMinute"},
		{name: "negative limit", registry: registry, install: config.AgentInstallConf{RedeemPerMinute: -1}, wantError: "RedeemPerMinute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Registry, cfg.AgentInstall = tc.registry, tc.install
			cfg.TenantNodes.MaxPerTenant, cfg.TenantNodes.MaxCreatesPerDay = 10, 10
			err := cfg.Validate()
			if (tc.wantError == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("Validate() error = %v, want one containing %q", err, tc.wantError)
			}
		})
	}
}

func TestAgentInstallConfMounting(t *testing.T) {
	complete := config.RegistryConf{Addr: "registry:9443", AdminToken: "t", CACertFile: "/ca.pem"}
	withURL := config.AgentInstallConf{PublicURL: "https://open7x.com"}
	for _, tc := range []struct {
		name             string
		install          config.AgentInstallConf
		registry         config.RegistryConf
		serving, issuing bool
	}{
		{"nothing configured", config.AgentInstallConf{}, config.RegistryConf{}, false, false},
		{"registry only", config.AgentInstallConf{}, complete, true, false},
		{"registry and url", withURL, complete, true, true},
		{"url without registry", withURL, config.RegistryConf{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.install.Serving(tc.registry); got != tc.serving {
				t.Errorf("Serving() = %v, want %v", got, tc.serving)
			}
			if got := tc.install.Issuing(tc.registry); got != tc.issuing {
				t.Errorf("Issuing() = %v, want %v", got, tc.issuing)
			}
		})
	}
	if got := (config.AgentInstallConf{}).RedeemLimit(); got != 60 {
		t.Errorf("RedeemLimit() of zero value = %d, want 60", got)
	}
}
