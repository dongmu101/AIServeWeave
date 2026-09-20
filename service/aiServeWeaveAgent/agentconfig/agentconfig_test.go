package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesGatewayAndRuntimes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	yaml := `
gateway:
  endpoints:
    - gw-1.example.com:8443
    - gw-2.example.com:8443
  registry: registry.example.com:9443
  node_id: node-a
  cert_file: /etc/aisw/cert.pem
  key_file: /etc/aisw/key.pem
  ca_file: /etc/aisw/ca.pem
  allowed_runtimes:
    - ollama-local
  labels:
    region: local
    gpu: "4090"
  max_gateways: 4
runtimes:
  - id: ollama-local
    kind: ollama
    base_url: http://127.0.0.1:11434
  - id: vllm-local
    kind: vllm
    base_url: http://127.0.0.1:8000
auto_discover: false
auto_discover_interval: 45s
metrics_addr: 127.0.0.1:9091
log_level: debug
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantEndpoints := []string{"gw-1.example.com:8443", "gw-2.example.com:8443"}
	if len(cfg.Gateway.Endpoints) != len(wantEndpoints) {
		t.Fatalf("Endpoints = %v, want %v", cfg.Gateway.Endpoints, wantEndpoints)
	}
	for i, want := range wantEndpoints {
		if cfg.Gateway.Endpoints[i] != want {
			t.Errorf("Endpoints[%d] = %q, want %q", i, cfg.Gateway.Endpoints[i], want)
		}
	}
	if cfg.Gateway.Registry != "registry.example.com:9443" {
		t.Errorf("Registry = %q, want %q", cfg.Gateway.Registry, "registry.example.com:9443")
	}
	if cfg.Gateway.NodeID != "node-a" {
		t.Errorf("NodeID = %q, want %q", cfg.Gateway.NodeID, "node-a")
	}
	if cfg.Gateway.MaxGateways != 4 {
		t.Errorf("MaxGateways = %d, want 4", cfg.Gateway.MaxGateways)
	}
	if cfg.Gateway.Labels["region"] != "local" || cfg.Gateway.Labels["gpu"] != "4090" {
		t.Errorf("Labels = %v, want region=local,gpu=4090", cfg.Gateway.Labels)
	}
	if len(cfg.Runtimes) != 2 {
		t.Fatalf("Runtimes = %v, want 2 entries", cfg.Runtimes)
	}
	if cfg.Runtimes[0] != (RuntimeConfig{ID: "ollama-local", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"}) {
		t.Errorf("Runtimes[0] = %+v, want ollama-local/ollama", cfg.Runtimes[0])
	}
	if cfg.AutoDiscover == nil || *cfg.AutoDiscover != false {
		t.Errorf("AutoDiscover = %v, want pointer to false", cfg.AutoDiscover)
	}
	if cfg.AutoDiscoverInterval != "45s" {
		t.Errorf("AutoDiscoverInterval = %q, want %q", cfg.AutoDiscoverInterval, "45s")
	}
	if cfg.MetricsAddr == nil || *cfg.MetricsAddr != "127.0.0.1:9091" {
		t.Errorf("MetricsAddr = %v, want pointer to 127.0.0.1:9091", cfg.MetricsAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("Load of a missing file returned no error")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("gateway: [this is not a mapping"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load of malformed YAML returned no error")
	}
}

// TestSaveLoadRoundTrip pins that whatever configui's local setup page
// writes with Save can be read back byte-for-byte-equivalent with Load —
// the whole point of a single YAML contract shared by both directions.
//
// TestSaveLoadRoundTrip 钉住 configui 本地设置页面用 Save 写下的内容，能被
// Load 原样读回——这正是两个方向共用同一份 YAML 契约的全部意义。
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")

	autoDiscover := true
	metricsAddr := "127.0.0.1:9091"
	want := &Config{
		Gateway: GatewayConfig{
			Endpoints: []string{"gw-1.example.com:8443"},
			Registry:  "registry.example.com:9443",
			NodeID:    "node-a",
			Labels:    map[string]string{"region": "local"},
		},
		Runtimes: []RuntimeConfig{
			{ID: "ollama-local", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"},
		},
		AutoDiscover: &autoDiscover,
		MetricsAddr:  &metricsAddr,
		LogLevel:     "info",
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(got.Gateway.Endpoints) != 1 || got.Gateway.Endpoints[0] != "gw-1.example.com:8443" {
		t.Errorf("Endpoints = %v, want [gw-1.example.com:8443]", got.Gateway.Endpoints)
	}
	if got.Gateway.NodeID != want.Gateway.NodeID {
		t.Errorf("NodeID = %q, want %q", got.Gateway.NodeID, want.Gateway.NodeID)
	}
	if len(got.Runtimes) != 1 || got.Runtimes[0] != want.Runtimes[0] {
		t.Errorf("Runtimes = %v, want %v", got.Runtimes, want.Runtimes)
	}
	if got.AutoDiscover == nil || *got.AutoDiscover != true {
		t.Errorf("AutoDiscover = %v, want pointer to true", got.AutoDiscover)
	}
	if got.MetricsAddr == nil || *got.MetricsAddr != "127.0.0.1:9091" {
		t.Errorf("MetricsAddr = %v, want pointer to 127.0.0.1:9091", got.MetricsAddr)
	}
}
