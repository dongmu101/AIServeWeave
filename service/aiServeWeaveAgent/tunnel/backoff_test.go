package tunnel_test

import (
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

func TestBackoffFullJitterWindow(t *testing.T) {
	// The window is min(max, initial*2^attempt); full jitter draws uniformly
	// from [0, window). Fixing the jitter source at both ends of that range
	// pins the interval exactly.
	tests := []struct {
		name       string
		attempts   int
		wantWindow time.Duration
	}{
		{"first attempt", 0, time.Second},
		{"second attempt", 1, 2 * time.Second},
		{"third attempt", 2, 4 * time.Second},
		{"fifth attempt", 4, 16 * time.Second},
		{"clamped at max", 5, 30 * time.Second},
		{"stays clamped", 20, 30 * time.Second},
		{"never overflows", 200, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lowest := tunnel.NewBackoff(time.Second, 30*time.Second, func() float64 { return 0 })
			highest := tunnel.NewBackoff(time.Second, 30*time.Second, func() float64 { return 0.999999 })
			for range tt.attempts {
				lowest.Next()
				highest.Next()
			}

			if got := lowest.Window(); got != tt.wantWindow {
				t.Errorf("window after %d attempts = %s, want %s", tt.attempts, got, tt.wantWindow)
			}
			if got := lowest.Next(); got != 0 {
				t.Errorf("delay with jitter 0 = %s, want 0: full jitter must be able to retry immediately", got)
			}
			got := highest.Next()
			if got <= 0 || got >= tt.wantWindow {
				t.Errorf("delay with jitter ~1 = %s, want in (0, %s)", got, tt.wantWindow)
			}
		})
	}
}

func TestBackoffResetAndDefaults(t *testing.T) {
	b := tunnel.NewBackoff(0, 0, func() float64 { return 0.5 })
	if got, want := b.Window(), time.Second; got != want {
		t.Errorf("default initial window = %s, want %s", got, want)
	}
	for range 10 {
		b.Next()
	}
	if got, want := b.Window(), 30*time.Second; got != want {
		t.Errorf("default max window = %s, want %s", got, want)
	}
	if got, want := b.Attempt(), 10; got != want {
		t.Errorf("attempt = %d, want %d", got, want)
	}

	// A tunnel that has been up for a week must not resume at 30s after its
	// first blip, which is what Reset is for.
	b.Reset()
	if got, want := b.Attempt(), 0; got != want {
		t.Errorf("attempt after Reset = %d, want %d", got, want)
	}
	if got, want := b.Window(), time.Second; got != want {
		t.Errorf("window after Reset = %s, want %s", got, want)
	}
}

func TestBackoffJitterSpreadsRetries(t *testing.T) {
	// Without a jitter function the delays must still be inside the window
	// and must not all be identical — that spread is the whole point: it
	// stops every Agent from reconnecting to a restarted replica at once.
	b := tunnel.NewBackoff(time.Second, 30*time.Second, nil)
	seen := map[time.Duration]int{}
	for range 50 {
		window := b.Window()
		d := b.Next()
		if d < 0 || d >= window {
			t.Fatalf("delay %s outside [0, %s)", d, window)
		}
		seen[d]++
		b.Reset()
	}
	if len(seen) < 2 {
		t.Errorf("distinct delays = %d, want more than one", len(seen))
	}
}
