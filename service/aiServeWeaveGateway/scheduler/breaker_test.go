package scheduler

import (
	"errors"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func upstreamErr() error {
	return &runtime.RuntimeError{Code: runtime.ErrorUpstream, Retryable: true}
}

func backpressureErr() error {
	return &runtime.RuntimeError{Code: runtime.ErrorBackpressure, Retryable: true}
}

func rateLimitedErr() error {
	return &runtime.RuntimeError{Code: runtime.ErrorRateLimited, Retryable: true}
}

func TestBreakerFailureQualifiesTheThreeNodeLevelCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"connection failed counts", &runtime.RuntimeError{Code: runtime.ErrorConnection}, true},
		{"timeout counts", &runtime.RuntimeError{Code: runtime.ErrorTimeout}, true},
		{"upstream error counts", &runtime.RuntimeError{Code: runtime.ErrorUpstream}, true},
		{"backpressure does not count", &runtime.RuntimeError{Code: runtime.ErrorBackpressure}, false},
		{"rate limited does not count", &runtime.RuntimeError{Code: runtime.ErrorRateLimited}, false},
		{"protocol error does not count", &runtime.RuntimeError{Code: runtime.ErrorProtocol}, false},
		{"a plain, non-RuntimeError error does not count", errors.New("boom"), false},
		{"nil does not count", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := breakerFailure(tt.err); got != tt.want {
				t.Errorf("breakerFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestBreakerTripsOpenAfterConsecutiveQualifyingFailures(t *testing.T) {
	r := newBreakerRegistry(3, time.Second, time.Minute, newRecorder(nil))
	c := Candidate{NodeID: "node-a", RuntimeID: "rt-1"}
	now := time.Unix(0, 0)

	for i := range 2 {
		r.record(c, upstreamErr(), now)
		if !r.eligible(c, now) {
			t.Fatalf("candidate became ineligible after only %d failures, want threshold 3", i+1)
		}
	}
	r.record(c, upstreamErr(), now)
	if r.eligible(c, now) {
		t.Fatal("candidate still eligible immediately after tripping the breaker")
	}
}

func TestBreakerBecomesEligibleAgainAfterCooldown(t *testing.T) {
	r := newBreakerRegistry(1, time.Second, time.Minute, newRecorder(nil))
	c := Candidate{NodeID: "node-a", RuntimeID: "rt-1"}
	now := time.Unix(0, 0)

	r.record(c, upstreamErr(), now)
	if r.eligible(c, now) {
		t.Fatal("candidate eligible before its cooldown elapsed")
	}
	if !r.eligible(c, now.Add(time.Second)) {
		t.Fatal("candidate still ineligible once the cooldown elapsed")
	}
}

func TestBreakerSuccessResetsTheStreak(t *testing.T) {
	r := newBreakerRegistry(3, time.Second, time.Minute, newRecorder(nil))
	c := Candidate{NodeID: "node-a", RuntimeID: "rt-1"}
	now := time.Unix(0, 0)

	r.record(c, upstreamErr(), now)
	r.record(c, upstreamErr(), now)
	r.record(c, nil, now) // success clears the streak
	r.record(c, upstreamErr(), now)
	r.record(c, upstreamErr(), now)
	if !r.eligible(c, now) {
		t.Fatal("two failures after a reset tripped the breaker; the success should have cleared the streak")
	}
}

func TestBreakerNonQualifyingFailuresDoNotTrip(t *testing.T) {
	r := newBreakerRegistry(3, time.Second, time.Minute, newRecorder(nil))
	c := Candidate{NodeID: "node-a", RuntimeID: "rt-1"}
	now := time.Unix(0, 0)

	for range 20 {
		r.record(c, backpressureErr(), now)
		r.record(c, rateLimitedErr(), now)
	}
	if !r.eligible(c, now) {
		t.Fatal("backpressure/rate-limited failures tripped the breaker; they must never count")
	}
}

func TestBreakerFailedProbeExtendsTheCooldownAndEscalates(t *testing.T) {
	r := newBreakerRegistry(1, time.Second, time.Minute, newRecorder(nil))
	c := Candidate{NodeID: "node-a", RuntimeID: "rt-1"}
	now := time.Unix(0, 0)

	r.record(c, upstreamErr(), now) // trip 0: opens for 1s (base)
	probeAt := now.Add(time.Second)
	if !r.eligible(c, probeAt) {
		t.Fatal("candidate should be eligible for its first probe once the base cooldown elapsed")
	}
	r.record(c, upstreamErr(), probeAt) // failed probe: trip 1, opens for 2s from probeAt
	if r.eligible(c, probeAt) {
		t.Fatal("a failed probe must re-open the breaker immediately")
	}
	if r.eligible(c, probeAt.Add(time.Second)) {
		t.Fatal("cooldown after a second trip should have doubled, not stayed at the base duration")
	}
	if !r.eligible(c, probeAt.Add(2*time.Second)) {
		t.Fatal("candidate still ineligible after the doubled cooldown elapsed")
	}
}

func TestBreakerCooldownIsCappedAtMaxCooldown(t *testing.T) {
	r := newBreakerRegistry(1, time.Second, 5*time.Second, newRecorder(nil))
	if got := r.cooldown(0); got != time.Second {
		t.Errorf("cooldown(0) = %v, want the base cooldown 1s", got)
	}
	if got := r.cooldown(2); got != 4*time.Second {
		t.Errorf("cooldown(2) = %v, want 4s (base doubled twice)", got)
	}
	if got := r.cooldown(10); got != 5*time.Second {
		t.Errorf("cooldown(10) = %v, want it capped at maxCooldown 5s", got)
	}
}

func TestBreakerSuccessfulProbeFullyResets(t *testing.T) {
	r := newBreakerRegistry(1, time.Second, time.Minute, newRecorder(nil))
	c := Candidate{NodeID: "node-a", RuntimeID: "rt-1"}
	now := time.Unix(0, 0)

	r.record(c, upstreamErr(), now)
	probeAt := now.Add(time.Second)
	r.record(c, nil, probeAt) // successful probe

	r.mu.Lock()
	st := r.entries[c]
	r.mu.Unlock()
	if st.open || st.consecutiveFailures != 0 || st.tripCount != 0 {
		t.Errorf("state after a successful probe = %+v, want fully reset", st)
	}
}

func TestNewBreakerRegistryAppliesDefaults(t *testing.T) {
	r := newBreakerRegistry(0, 0, 0, newRecorder(nil))
	if r.failureThreshold != defaultFailureThreshold {
		t.Errorf("failureThreshold = %d, want default %d", r.failureThreshold, defaultFailureThreshold)
	}
	if r.baseCooldown != defaultBaseCooldown {
		t.Errorf("baseCooldown = %v, want default %v", r.baseCooldown, defaultBaseCooldown)
	}
	if r.maxCooldown != defaultMaxCooldown {
		t.Errorf("maxCooldown = %v, want default %v", r.maxCooldown, defaultMaxCooldown)
	}
}
