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
