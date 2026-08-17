package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestRuntimeErrorIsMatchesByCode(t *testing.T) {
	err := &RuntimeError{Code: ErrorTimeout, Kind: KindVLLM, RuntimeID: "r1", Operation: "chat", Cause: errors.New("boom")}
	if !errors.Is(err, &RuntimeError{Code: ErrorTimeout}) {
		t.Fatal("expected errors.Is to match a RuntimeError with the same Code")
	}
	if errors.Is(err, &RuntimeError{Code: ErrorConnection}) {
		t.Fatal("did not expect errors.Is to match a RuntimeError with a different Code")
	}
}

func TestRuntimeErrorUnwrap(t *testing.T) {
	cause := errors.New("dial refused")
	err := &RuntimeError{Code: ErrorConnection, Cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("expected errors.Is to reach the wrapped cause")
	}

	var target *RuntimeError
	if !errors.As(fmt.Errorf("wrap: %w", err), &target) {
		t.Fatal("expected errors.As to unwrap to *RuntimeError")
	}
	if target.Code != ErrorConnection {
		t.Fatalf("unwrapped RuntimeError has Code %s, want %s", target.Code, ErrorConnection)
	}
}

func TestRuntimeErrorMessageOmitsCause(t *testing.T) {
	err := &RuntimeError{Code: ErrorUpstream, Message: "safe summary", Cause: errors.New("Authorization: Bearer secret-key")}
	msg := err.Error()
	if !strings.Contains(msg, "safe summary") {
		t.Fatalf("expected Error() to include Message, got %q", msg)
	}
	if strings.Contains(msg, "secret-key") {
		t.Fatalf("Error() leaked the cause when Message was set: %q", msg)
	}
}

func TestErrorCodeFromStatus(t *testing.T) {
	cases := map[int]ErrorCode{
		401: ErrorUnauthorized,
		403: ErrorUnauthorized,
		429: ErrorRateLimited,
		500: ErrorUpstream,
		503: ErrorUpstream,
		404: ErrorProtocol,
	}
	for status, want := range cases {
		if got := ErrorCodeFromStatus(status); got != want {
			t.Errorf("ErrorCodeFromStatus(%d) = %s, want %s", status, got, want)
		}
	}
}

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "i/o timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func TestClassifyTransportError(t *testing.T) {
	if got := ClassifyTransportError(context.DeadlineExceeded); got != ErrorTimeout {
		t.Errorf("deadline exceeded: got %s, want %s", got, ErrorTimeout)
	}
	if got := ClassifyTransportError(fakeTimeoutErr{}); got != ErrorTimeout {
		t.Errorf("net timeout: got %s, want %s", got, ErrorTimeout)
	}
	if got := ClassifyTransportError(&net.OpError{Op: "dial", Err: errors.New("connection refused")}); got != ErrorConnection {
		t.Errorf("connection refused: got %s, want %s", got, ErrorConnection)
	}
}

func TestRedact(t *testing.T) {
	msg := "Authorization: Bearer sk-secret123 failed for user"
	got := Redact(msg, "sk-secret123")
	want := "Authorization: Bearer [REDACTED] failed for user"
	if got != want {
		t.Errorf("Redact() = %q, want %q", got, want)
	}
	if got := Redact("no secrets here", ""); got != "no secrets here" {
		t.Errorf("Redact should ignore empty secrets, got %q", got)
	}
}
