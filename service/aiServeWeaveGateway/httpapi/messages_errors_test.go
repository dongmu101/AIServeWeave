package httpapi

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

func TestMessagesRejectionSurvivesTunnel(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		exposed       bool
	}{
		{"thinking", "capability_rejected: thinking_type:adaptive", true},
		{"context", "context_management: Extra inputs are not permitted", true},
		{"beta", "capability_rejected: beta_header:future-feature-2099-01-01", true},
		{"private suffix", "capability_rejected: thinking_signature PRIVATE_MARKER", false},
		{"unknown token", "capability_rejected: PRIVATE_MARKER", false},
		{"invalid beta", "capability_rejected: beta_header:PRIVATE_MARKER\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := &runtime.RuntimeError{Code: runtime.ErrorCapability, Kind: runtime.KindClaude, Operation: "messages", Message: tc.message}
			err := tunnelwire.ErrorFromProto(tunnelwire.ErrorToProto(cause))
			status, _, _, message := dispatchErrorDetails(slog.New(slog.NewTextHandler(io.Discard, nil)), err)
			if status != 400 || (tc.exposed && message != tc.message) || (!tc.exposed && strings.Contains(message, "PRIVATE_MARKER")) {
				t.Fatalf("status/message=%d/%q, exposed=%t", status, message, tc.exposed)
			}
		})
	}
}
