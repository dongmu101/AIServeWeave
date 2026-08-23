package oaibase_test

import (
	"errors"
	"os"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/oaibase"
)

// TestMain enforces the package's goroutine-leak gate. Base's streaming
// path is exercised through the adapter packages, which run the same check;
// this one covers anything started directly from here.
func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !goroutineCountSettles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func goroutineCountSettles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChatCapabilitiesCoverOnlyTheFeaturesTheRequestUses(t *testing.T) {
	tests := []struct {
		name string
		req  runtime.ChatRequest
		want []runtime.Capability
	}{
		{
			name: "plain chat",
			req:  runtime.ChatRequest{Model: "m"},
			want: []runtime.Capability{runtime.CapabilityChat},
		},
		{
			name: "with tools",
			req: runtime.ChatRequest{
				Model: "m",
				Tools: []runtime.Tool{{Type: "function", Function: runtime.FunctionDefinition{Name: "now"}}},
			},
			want: []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityTools},
		},
		{
			name: "with response format",
			req: runtime.ChatRequest{
				Model:          "m",
				ResponseFormat: &runtime.ResponseFormat{Type: "json_object"},
			},
			want: []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityStructuredOutput},
		},
		{
			name: "with both",
			req: runtime.ChatRequest{
				Model:          "m",
				Tools:          []runtime.Tool{{Type: "function", Function: runtime.FunctionDefinition{Name: "now"}}},
				ResponseFormat: &runtime.ResponseFormat{Type: "json_object"},
			},
			want: []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityTools, runtime.CapabilityStructuredOutput},
		},
		{
			// An empty (non-nil) Tools slice means the caller sends no
			// tools, so gating on tool support would reject a request that
			// does not need it.
			name: "empty tool slice is not a tool request",
			req:  runtime.ChatRequest{Model: "m", Tools: []runtime.Tool{}},
			want: []runtime.Capability{runtime.CapabilityChat},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := oaibase.ChatCapabilities(tt.req)
			if len(got) != len(tt.want) {
				t.Fatalf("ChatCapabilities = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ChatCapabilities[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestErrorSummaryPrefersTheSanitizedMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "runtime error message",
			err:  &runtime.RuntimeError{Code: runtime.ErrorUpstream, Message: "engine is dead"},
			want: "engine is dead",
		},
		{
			// Falling through to err.Error() could leak an unredacted
			// transport string into a health report, so a message-less
			// RuntimeError gets the generic summary instead.
			name: "runtime error without a message",
			err:  &runtime.RuntimeError{Code: runtime.ErrorConnection, Cause: errors.New("dial tcp 10.0.0.1:8000: refused")},
			want: "health check failed",
		},
		{
			name: "plain error",
			err:  errors.New("something opaque"),
			want: "health check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oaibase.ErrorSummary(tt.err); got != tt.want {
				t.Errorf("ErrorSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConflictWarningsSurfaceOnlyMergeConflicts(t *testing.T) {
	set := runtime.Merge(
		runtime.CapabilitySet{
			runtime.CapabilityTools: {
				Capability: runtime.CapabilityTools,
				Level:      runtime.SupportSupported,
				Source:     runtime.SourceRuntimeProfile,
				Detail:     "profile says yes",
			},
			runtime.CapabilityChat: {
				Capability: runtime.CapabilityChat,
				Level:      runtime.SupportSupported,
				Source:     runtime.SourceEndpoint,
				Detail:     "endpoint answered",
			},
		},
		runtime.CapabilitySet{
			runtime.CapabilityTools: {
				Capability: runtime.CapabilityTools,
				Level:      runtime.SupportUnsupported,
				Source:     runtime.SourceRuntimeProfile,
				Detail:     "profile says no",
			},
		},
	)

	warnings := oaibase.ConflictWarnings(set)
	if len(warnings) != 1 {
		t.Fatalf("ConflictWarnings = %v, want exactly the one conflicted capability", warnings)
	}
	if !strings.Contains(warnings[0], string(runtime.CapabilityTools)) {
		t.Errorf("warning = %q, want it to name the conflicted capability", warnings[0])
	}
	if got := set.Resolve(runtime.CapabilityTools).Level; got != runtime.SupportUnsupported {
		t.Errorf("tools resolved to %q, want the conservative %q", got, runtime.SupportUnsupported)
	}
}
