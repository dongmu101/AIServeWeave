package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
)

// The agent's own wiring has one decision worth testing on its own: how the
// tunnel flags turn into a runtime allowlist. Getting this wrong widens the
// last line of defence against a compromised gateway, so an empty result and
// a populated one must both be unambiguous.
func TestTunnelOptionsRuntimeIDs(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "no narrowing configured", value: "", want: nil},
		{name: "a single id", value: "ollama-local", want: []string{"ollama-local"}},
		{name: "several ids", value: "ollama-local,comfy-1", want: []string{"ollama-local", "comfy-1"}},
		{name: "surrounding spaces are trimmed", value: " ollama-local , comfy-1 ", want: []string{"ollama-local", "comfy-1"}},
		{name: "empty entries are dropped", value: ",ollama-local,,", want: []string{"ollama-local"}},
		{name: "only separators is no narrowing", value: " , , ", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &tunnelOptions{allowedRuntimes: tt.value}
			if got := opts.runtimeIDs(); !slices.Equal(got, tt.want) {
				t.Errorf("runtimeIDs(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// The seed list only has to contain one reachable replica, so parsing it
// leniently matters: a stray comma must not cost the operator a tunnel.
func TestTunnelOptionsSeeds(t *testing.T) {
	tests := []struct {
		name    string
		gateway string
		want    []string
		enabled bool
	}{
		{name: "no gateway leaves the tunnel off", gateway: "", want: nil},
		{name: "whitespace is not a gateway", gateway: "   ", want: nil},
		{
			name:    "a single seed",
			gateway: "gw-1.example.com:8443",
			want:    []string{"gw-1.example.com:8443"},
			enabled: true,
		},
		{
			name:    "several seeds",
			gateway: "gw-1.example.com:8443,gw-2.example.com:8443",
			want:    []string{"gw-1.example.com:8443", "gw-2.example.com:8443"},
			enabled: true,
		},
		{
			name:    "spaces and empty entries are dropped",
			gateway: " gw-1.example.com:8443 , , gw-2.example.com:8443,",
			want:    []string{"gw-1.example.com:8443", "gw-2.example.com:8443"},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &tunnelOptions{gateway: tt.gateway}
			if got := opts.seeds(); !slices.Equal(got, tt.want) {
				t.Errorf("seeds(%q) = %v, want %v", tt.gateway, got, tt.want)
			}
			if got := opts.enabled(); got != tt.enabled {
				t.Errorf("enabled() with gateway %q = %v, want %v", tt.gateway, got, tt.enabled)
			}
		})
	}
}

// TestTunnelOptionsNodeLabels pins the parse, including what it drops. A
// malformed entry is dropped rather than fatal: labels express a routing
// preference, and refusing to start over a typo in one would take a working
// node offline for something that only affects where requests prefer to go.
//
// TestTunnelOptionsNodeLabels 钉住这次解析，包括它丢弃了什么。格式错误的条目被丢弃
// 而不是致命错误：标签表达的是路由偏好，为其中一个的笔误而拒绝启动，会为「只影响请求
// 偏好去哪」的事情让一个本来能工作的节点下线。
func TestTunnelOptionsNodeLabels(t *testing.T) {
	tests := []struct {
		name  string
		flag  string
		want  map[string]string
		empty bool
	}{
		{
			name:  "empty flag",
			flag:  "",
			empty: true,
		},
		{
			name: "one pair",
			flag: "region=local",
			want: map[string]string{"region": "local"},
		},
		{
			name: "several pairs with spacing",
			flag: " region=local , gpu=4090 ",
			want: map[string]string{"region": "local", "gpu": "4090"},
		},
		{
			name: "an empty value is a value",
			flag: "maintenance=",
			want: map[string]string{"maintenance": ""},
		},
		{
			name: "a value containing = keeps the rest",
			flag: "note=a=b",
			want: map[string]string{"note": "a=b"},
		},
		{
			name:  "an entry with no separator is dropped",
			flag:  "justakey",
			empty: true,
		},
		{
			name:  "an entry with no key is dropped",
			flag:  "=value",
			empty: true,
		},
		{
			name: "a bad entry does not take the good ones with it",
			flag: "region=local,justakey",
			want: map[string]string{"region": "local"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&tunnelOptions{labels: tt.flag}).nodeLabels()
			if tt.empty {
				if got != nil {
					t.Fatalf("nodeLabels() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("nodeLabels() = %v, want %v", got, tt.want)
			}
			for k, w := range tt.want {
				if got[k] != w {
					t.Errorf("nodeLabels()[%q] = %q, want %q", k, got[k], w)
				}
			}
		})
	}
}

// TestApplyConfigFillsGapsWithoutOverridingExplicitFlags pins the one rule
// applyConfig exists to enforce: a flag the operator actually typed always
// wins, and -config only ever fills in what a flag left at its default.
// Getting the direction backwards would mean the file silently overrides a
// deliberate command-line choice, which is the opposite of what
// "explicitFlags" is for.
//
// TestApplyConfigFillsGapsWithoutOverridingExplicitFlags 钉住 applyConfig
// 存在的唯一规则：操作者实际敲过的 flag 永远优先，-config 只填补 flag 留在
// 默认值上的空白。方向反了就意味着这份文件会悄悄覆盖一次刻意的命令行选
// 择，与 explicitFlags 存在的目的正相反。
func TestApplyConfigFillsGapsWithoutOverridingExplicitFlags(t *testing.T) {
	cfg := &agentconfig.Config{
		Gateway: agentconfig.GatewayConfig{
			Endpoints:   []string{"gw-1.example.com:8443", "gw-2.example.com:8443"},
			Registry:    "registry.example.com:9443",
			NodeID:      "node-from-file",
			MaxGateways: 8,
			Labels:      map[string]string{"region": "local"},
		},
		AutoDiscoverInterval: "45s",
		LogLevel:             "debug",
	}
	autoDiscover := true
	autoDiscoverInterval := 30 * time.Second
	metricsAddr := "127.0.0.1:9091"
	logLevel := "info"
	opts := &tunnelOptions{nodeID: "node-from-flag"}
	explicit := map[string]bool{"node-id": true}

	if err := applyConfig(cfg, explicit, opts, &autoDiscover, &autoDiscoverInterval, &metricsAddr, &logLevel); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}

	if opts.nodeID != "node-from-flag" {
		t.Errorf("nodeID = %q, want the explicitly-passed flag value %q", opts.nodeID, "node-from-flag")
	}
	if opts.gateway != "gw-1.example.com:8443,gw-2.example.com:8443" {
		t.Errorf("gateway = %q, want the config file's endpoints joined", opts.gateway)
	}
	if opts.registry != "registry.example.com:9443" {
		t.Errorf("registry = %q, want %q", opts.registry, "registry.example.com:9443")
	}
	if opts.maxGateways != 8 {
		t.Errorf("maxGateways = %d, want 8", opts.maxGateways)
	}
	if opts.labels != "region=local" {
		t.Errorf("labels = %q, want %q", opts.labels, "region=local")
	}
	if autoDiscoverInterval != 45*time.Second {
		t.Errorf("autoDiscoverInterval = %v, want 45s", autoDiscoverInterval)
	}
	if logLevel != "debug" {
		t.Errorf("logLevel = %q, want %q", logLevel, "debug")
	}
	if metricsAddr != "127.0.0.1:9091" {
		t.Errorf("metricsAddr = %q, want it left untouched at its default", metricsAddr)
	}
}

// TestApplyConfigRejectsBadDuration pins that a malformed
// auto_discover_interval fails startup rather than silently keeping the
// flag's default: unlike a single bad -labels entry, this setting controls
// how often the agent scans for local backends, and a typo here should be
// visible immediately, not discovered later from unexpected scan behavior.
func TestApplyConfigRejectsBadDuration(t *testing.T) {
	cfg := &agentconfig.Config{AutoDiscoverInterval: "not-a-duration"}
	autoDiscover := true
	autoDiscoverInterval := 30 * time.Second
	metricsAddr := ""
	logLevel := "info"

	err := applyConfig(cfg, map[string]bool{}, &tunnelOptions{}, &autoDiscover, &autoDiscoverInterval, &metricsAddr, &logLevel)
	if err == nil {
		t.Fatal("applyConfig with a malformed auto_discover_interval returned no error")
	}
}

// TestRuntimesFromConfig pins the conversion runtimesFromConfig performs:
// declared runtimes become manager.Add-ready runtime.Config values in the
// same order they were declared, and an empty declaration yields nil rather
// than an empty-but-non-nil slice.
func TestRuntimesFromConfig(t *testing.T) {
	if got := runtimesFromConfig(nil); got != nil {
		t.Errorf("runtimesFromConfig(nil) = %v, want nil", got)
	}

	declared := []agentconfig.RuntimeConfig{
		{ID: "ollama-local", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"},
		{ID: "vllm-local", Kind: "vllm", BaseURL: "http://127.0.0.1:8000"},
	}
	want := []runtime.Config{
		{ID: "ollama-local", Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434"},
		{ID: "vllm-local", Kind: runtime.KindVLLM, BaseURL: "http://127.0.0.1:8000"},
	}
	got := runtimesFromConfig(declared)
	if len(got) != len(want) {
		t.Fatalf("runtimesFromConfig() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Kind != want[i].Kind || got[i].BaseURL != want[i].BaseURL {
			t.Errorf("runtimesFromConfig()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestResolveConfigPath pins the two things that matter about -config's
// default lookup: an explicit -config always wins verbatim even when the
// file behind it does not exist (so a typo surfaces as a hard failure, not
// a silent fallback), and the default path is only used when it is
// actually there — a fresh checkout with no config.yaml must behave
// exactly like this feature never shipped.
func TestResolveConfigPath(t *testing.T) {
	dir := t.TempDir()
	defaultPath := filepath.Join(dir, "config.yaml")

	t.Run("explicit flag wins even over a missing file", func(t *testing.T) {
		got := resolveConfigPath("/no/such/file.yaml", true, defaultPath)
		if got != "/no/such/file.yaml" {
			t.Errorf("resolveConfigPath() = %q, want the explicit path unchanged", got)
		}
	})

	t.Run("no flag and no default file present yields nothing", func(t *testing.T) {
		got := resolveConfigPath("", false, defaultPath)
		if got != "" {
			t.Errorf("resolveConfigPath() = %q, want empty when the default file does not exist", got)
		}
	})

	t.Run("no flag but the default file exists falls back to it", func(t *testing.T) {
		if err := os.WriteFile(defaultPath, []byte("gateway: {}\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got := resolveConfigPath("", false, defaultPath)
		if got != defaultPath {
			t.Errorf("resolveConfigPath() = %q, want %q", got, defaultPath)
		}
	})
}

// TestShouldAutoOpenConfigUI pins the one rule that keeps a first-time
// operator's setup page from turning into a trap for everyone else: it only
// fires when nothing at all points anywhere, and any explicit signal —
// including a deliberate -gateway="" — turns it off, even when a resolved
// config path would otherwise say "nothing found".
func TestShouldAutoOpenConfigUI(t *testing.T) {
	tests := []struct {
		name               string
		configUI           bool
		explicit           map[string]bool
		resolvedConfigPath string
		want               bool
	}{
		{
			name:               "nothing configured at all triggers the fallback",
			explicit:           map[string]bool{},
			resolvedConfigPath: "",
			want:               true,
		},
		{
			name:               "a resolved config file suppresses it",
			explicit:           map[string]bool{},
			resolvedConfigPath: "config.yaml",
			want:               false,
		},
		{
			name:               "explicit -config-ui already covers it, so no need to auto-trigger",
			configUI:           true,
			explicit:           map[string]bool{"config-ui": true},
			resolvedConfigPath: "",
			want:               false,
		},
		{
			name:               "explicit -config-ui-addr opts out even without -config-ui",
			explicit:           map[string]bool{"config-ui-addr": true},
			resolvedConfigPath: "",
			want:               false,
		},
		{
			name:               "explicit -config opts out",
			explicit:           map[string]bool{"config": true},
			resolvedConfigPath: "",
			want:               false,
		},
		{
			name:               "explicit -gateway opts out even when empty",
			explicit:           map[string]bool{"gateway": true},
			resolvedConfigPath: "",
			want:               false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAutoOpenConfigUI(tt.configUI, tt.explicit, tt.resolvedConfigPath); got != tt.want {
				t.Errorf("shouldAutoOpenConfigUI(%v, %v, %q) = %v, want %v",
					tt.configUI, tt.explicit, tt.resolvedConfigPath, got, tt.want)
			}
		})
	}
}
