package runtime

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		ID:      "r1",
		Kind:    KindVLLM,
		BaseURL: "http://127.0.0.1:8000",
	}
}

func TestNormalizeFillsZeroValueDefaults(t *testing.T) {
	got := validConfig().Normalize()

	cases := map[string]struct {
		got  time.Duration
		want time.Duration
	}{
		"ProbeTimeout":      {got.ProbeTimeout, defaultProbeTimeout},
		"RequestTimeout":    {got.RequestTimeout, defaultRequestTimeout},
		"StreamIdleTimeout": {got.StreamIdleTimeout, defaultStreamIdleTimeout},
		"HealthInterval":    {got.HealthInterval, defaultHealthInterval},
		"DiscoveryInterval": {got.DiscoveryInterval, defaultDiscoveryInterval},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want default %v", name, c.got, c.want)
		}
	}
	if got.MaxConcurrent != defaultMaxConcurrent {
		t.Errorf("MaxConcurrent = %d, want default %d", got.MaxConcurrent, defaultMaxConcurrent)
	}
}

func TestNormalizeLeavesNegativeValuesAsExplicitOptOut(t *testing.T) {
	cfg := validConfig()
	cfg.MaxConcurrent = -1
	cfg.ProbeTimeout = -1

	got := cfg.Normalize()
	if got.MaxConcurrent != -1 {
		t.Errorf("MaxConcurrent = %d, want -1 (explicit unlimited) preserved", got.MaxConcurrent)
	}
	if got.ProbeTimeout != -1 {
		t.Errorf("ProbeTimeout = %v, want -1 preserved", got.ProbeTimeout)
	}
}

func TestNormalizeDoesNotMutateReceiver(t *testing.T) {
	cfg := validConfig()
	_ = cfg.Normalize()
	if cfg.MaxConcurrent != 0 {
		t.Fatalf("Normalize mutated the receiver: MaxConcurrent = %d", cfg.MaxConcurrent)
	}
}

func TestNormalizeStripsTrailingSlashFromBaseURL(t *testing.T) {
	cfg := validConfig()
	cfg.BaseURL = "http://127.0.0.1:8000/vendor/"

	got := cfg.Normalize().BaseURL
	if got != "http://127.0.0.1:8000/vendor" {
		t.Fatalf("BaseURL = %q, want trailing slash trimmed", got)
	}
}

func TestNormalizePreservesPathPrefixWithoutTrailingSlash(t *testing.T) {
	cfg := validConfig()
	cfg.BaseURL = "http://127.0.0.1:8000/vendor"

	got := cfg.Normalize().BaseURL
	if got != "http://127.0.0.1:8000/vendor" {
		t.Fatalf("BaseURL = %q, want prefix preserved unchanged", got)
	}
}

func TestValidateAcceptsAValidConfig(t *testing.T) {
	if err := validConfig().Normalize().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a valid config", err)
	}
}

func TestValidateRejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*Config)
	}{
		{"empty id", func(c *Config) { c.ID = "" }},
		{"blank id", func(c *Config) { c.ID = "   " }},
		{"unknown kind", func(c *Config) { c.Kind = Kind("made-up") }},
		{"empty base_url", func(c *Config) { c.BaseURL = "" }},
		{"non-http scheme", func(c *Config) { c.BaseURL = "ftp://127.0.0.1" }},
		{"userinfo in base_url", func(c *Config) { c.BaseURL = "http://user:pass@127.0.0.1" }},
		{"query in base_url", func(c *Config) { c.BaseURL = "http://127.0.0.1?x=1" }},
		{"fragment in base_url", func(c *Config) { c.BaseURL = "http://127.0.0.1#frag" }},
		{"forbidden Host header", func(c *Config) { c.Headers = map[string]string{"Host": "evil"} }},
		{"forbidden Content-Length header", func(c *Config) { c.Headers = map[string]string{"Content-Length": "0"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.modify(&cfg)
			err := cfg.Normalize().Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error for %s", tc.name)
			}
			var rtErr *RuntimeError
			if !errors.As(err, &rtErr) || rtErr.Code != ErrorInvalidConfig {
				t.Fatalf("Validate() error = %v, want Code %s", err, ErrorInvalidConfig)
			}
		})
	}
}

func TestValidateAggregatesAllProblems(t *testing.T) {
	cfg := Config{} // empty id, empty kind, empty base_url: three problems at once
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an aggregated error")
	}
	msg := err.Error()
	for _, want := range []string{"id", "kind", "base_url"} {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error %q does not mention %q", msg, want)
		}
	}
}

func TestLogValueOmitsSecrets(t *testing.T) {
	cfg := validConfig()
	cfg.APIKey = "sk-super-secret"
	cfg.Headers = map[string]string{"X-Trace-Id": "trace-value-xyz"}
	cfg.TLS = TLSConfig{CAFile: "/etc/secret/ca.pem", ServerName: "internal.example"}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("config loaded", "config", cfg)

	out := buf.String()
	for _, secret := range []string{"sk-super-secret", "trace-value-xyz", "/etc/secret/ca.pem"} {
		if strings.Contains(out, secret) {
			t.Fatalf("LogValue leaked secret %q into log output: %s", secret, out)
		}
	}
	if !strings.Contains(out, cfg.ID) {
		t.Fatalf("LogValue omitted the non-secret ID field: %s", out)
	}
}
