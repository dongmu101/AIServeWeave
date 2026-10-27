package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestBrokerRoundTrip checks identity binding and exactly-once results.
// TestBrokerRoundTrip 检查身份绑定及结果只能投递一次。
func TestBrokerRoundTrip(t *testing.T) {
	owner := principal{"tenant-a", "key-a"}
	b := newBroker(owner)
	t.Cleanup(b.close)
	result := make(chan toolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := b.call(t.Context(), "probe_echo", json.RawMessage(`{"label":"a"}`))
		result <- r
		errCh <- err
	}()
	c := <-b.calls
	for _, tc := range []struct {
		name  string
		owner principal
	}{
		{"other tenant", principal{"tenant-b", "key-a"}},
		{"other key", principal{"tenant-a", "key-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.resolve(tc.owner, c.ID, toolResult{Text: "wrong"}); !errors.Is(err, errCallRejected) {
				t.Fatalf("resolve = %v, want rejected", err)
			}
		})
	}
	if err := b.resolve(owner, c.ID, toolResult{Text: "expected"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if got := (<-result).Text; got != "expected" {
		t.Fatalf("result = %q, want expected", got)
	}
	if err := b.resolve(owner, c.ID, toolResult{Text: "duplicate"}); !errors.Is(err, errCallRejected) {
		t.Fatalf("duplicate = %v, want rejected", err)
	}
}

// TestBrokerLimits checks bounded admission and cancellation cleanup.
// TestBrokerLimits 检查有界准入及取消后的清理。
func TestBrokerLimits(t *testing.T) {
	b := newBroker(principal{"t", "k"})
	t.Cleanup(b.close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, maxPending)
	var ids []string
	for range maxPending {
		go func() { _, err := b.call(ctx, "probe_echo", json.RawMessage(`{"label":"a"}`)); done <- err }()
		ids = append(ids, (<-b.calls).ID)
	}
	if _, err := b.call(ctx, "probe_echo", json.RawMessage(`{"label":"a"}`)); !errors.Is(err, errCapacity) {
		t.Fatalf("overflow = %v, want capacity", err)
	}
	cancel()
	for range maxPending {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel = %v, want canceled", err)
		}
	}
	for _, id := range ids {
		if err := b.resolve(b.owner, id, toolResult{}); !errors.Is(err, errCallRejected) {
			t.Fatalf("stale = %v, want rejected", err)
		}
	}
}

// TestBrokerCloseAndValidation checks fail-closed input and shutdown behavior.
// TestBrokerCloseAndValidation 检查输入拒绝及关闭行为。
func TestBrokerCloseAndValidation(t *testing.T) {
	for _, tc := range []struct{ name, tool, args string }{
		{"unknown tool", "Bash", `{"label":"a"}`},
		{"malformed", "probe_echo", `{`},
		{"unexpected field", "probe_echo", `{"label":"a","command":"secret"}`},
		{"missing label", "probe_echo", `{}`},
		{"large label", "probe_echo", `{"label":"` + strings.Repeat("x", 129) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBroker(principal{"t", "k"})
			defer b.close()
			if _, err := b.call(t.Context(), tc.tool, json.RawMessage(tc.args)); !errors.Is(err, errInvalidTool) {
				t.Fatalf("invalid call = %v, want invalid tool", err)
			}
		})
	}
	b := newBroker(principal{"t", "k"})
	done := make(chan error, 1)
	go func() { _, err := b.call(t.Context(), "probe_echo", json.RawMessage(`{"label":"a"}`)); done <- err }()
	c := <-b.calls
	if err := b.resolve(b.owner, c.ID, toolResult{Text: strings.Repeat("x", maxEventBytes+1)}); !errors.Is(err, errResultTooLarge) {
		t.Fatalf("large result = %v, want too large", err)
	}
	b.close()
	b.close()
	if err := <-done; !errors.Is(err, errClosed) {
		t.Fatalf("closed call = %v, want closed", err)
	}
	if _, err := b.call(t.Context(), "probe_echo", json.RawMessage(`{"label":"a"}`)); !errors.Is(err, errClosed) {
		t.Fatalf("call after close = %v, want closed", err)
	}
}
