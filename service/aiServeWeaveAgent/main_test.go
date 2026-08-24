package main

import (
	"slices"
	"testing"
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
