package capprofile_test

import (
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/capprofile"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   [3]int
		wantOK bool
	}{
		{name: "full triple", input: "0.32.14", want: [3]int{0, 32, 14}, wantOK: true},
		{name: "v prefix", input: "v0.6.3", want: [3]int{0, 6, 3}, wantOK: true},
		{name: "surrounding space", input: "  1.2.3 ", want: [3]int{1, 2, 3}, wantOK: true},
		{name: "prerelease suffix", input: "0.5.4-rc1", want: [3]int{0, 5, 4}, wantOK: true},
		{name: "build metadata", input: "0.9.1+cu124", want: [3]int{0, 9, 1}, wantOK: true},
		{name: "python post release", input: "0.6.3post1", want: [3]int{0, 6, 0}, wantOK: true},
		{name: "minor only", input: "0.5", want: [3]int{0, 5, 0}, wantOK: true},
		{name: "extra components ignored", input: "1.2.3.4", want: [3]int{1, 2, 3}, wantOK: true},
		{name: "empty", input: "", wantOK: false},
		{name: "only space", input: "   ", wantOK: false},
		{name: "non numeric", input: "dev", wantOK: false},
		{name: "git describe", input: "unknown-build", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := capprofile.ParseVersion(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ParseVersion(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("ParseVersion(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b [3]int
		want int
	}{
		{name: "equal", a: [3]int{1, 2, 3}, b: [3]int{1, 2, 3}, want: 0},
		{name: "major lower", a: [3]int{0, 9, 9}, b: [3]int{1, 0, 0}, want: -1},
		{name: "minor higher", a: [3]int{0, 32, 0}, b: [3]int{0, 5, 0}, want: 1},
		{name: "patch lower", a: [3]int{0, 5, 1}, b: [3]int{0, 5, 2}, want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capprofile.Compare(tt.a, tt.b); got != tt.want {
				t.Errorf("Compare(%v, %v) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestTableResolve(t *testing.T) {
	// Declared highest-floor-first on purpose: Resolve must apply floors in
	// ascending order regardless of how the table is written, or a table's
	// declaration order would silently change its meaning.
	table := capprofile.Table{
		{
			MinVersion: "1.0.0",
			Detail:     "newer release",
			Caps: map[runtime.Capability]runtime.SupportLevel{
				runtime.CapabilityTools: runtime.SupportSupported,
			},
		},
		{
			MinVersion: "0.4.0",
			Detail:     "baseline release",
			Caps: map[runtime.Capability]runtime.SupportLevel{
				runtime.CapabilityChat:  runtime.SupportSupported,
				runtime.CapabilityTools: runtime.SupportUnsupported,
			},
		},
	}

	tests := []struct {
		name       string
		version    string
		wantChat   runtime.SupportLevel
		wantTools  runtime.SupportLevel
		wantVision runtime.SupportLevel
	}{
		{name: "below every floor", version: "0.3.9", wantChat: runtime.SupportUnknown, wantTools: runtime.SupportUnknown, wantVision: runtime.SupportUnknown},
		{name: "baseline floor only", version: "0.4.0", wantChat: runtime.SupportSupported, wantTools: runtime.SupportUnsupported, wantVision: runtime.SupportUnknown},
		{name: "higher floor refines lower", version: "1.2.0", wantChat: runtime.SupportSupported, wantTools: runtime.SupportSupported, wantVision: runtime.SupportUnknown},
		{name: "unparsable version claims nothing", version: "dev", wantChat: runtime.SupportUnknown, wantTools: runtime.SupportUnknown, wantVision: runtime.SupportUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := table.Resolve(tt.version)
			if got := set.Resolve(runtime.CapabilityChat).Level; got != tt.wantChat {
				t.Errorf("chat = %q, want %q", got, tt.wantChat)
			}
			if got := set.Resolve(runtime.CapabilityTools).Level; got != tt.wantTools {
				t.Errorf("tools = %q, want %q", got, tt.wantTools)
			}
			if got := set.Resolve(runtime.CapabilityVision).Level; got != tt.wantVision {
				t.Errorf("vision = %q, want %q", got, tt.wantVision)
			}
		})
	}
}

func TestTableResolveTagsEvidenceAsRuntimeProfile(t *testing.T) {
	table := capprofile.Table{{
		MinVersion: "0.1.0",
		Detail:     "documented in the backend's compatibility page",
		Caps:       map[runtime.Capability]runtime.SupportLevel{runtime.CapabilityChat: runtime.SupportSupported},
	}}

	ev := table.Resolve("0.2.0").Resolve(runtime.CapabilityChat)
	if ev.Source != runtime.SourceRuntimeProfile {
		t.Errorf("Source = %q, want %q", ev.Source, runtime.SourceRuntimeProfile)
	}
	if ev.Detail == "" {
		t.Error("Detail is empty, want the table's evidence citation")
	}
}
