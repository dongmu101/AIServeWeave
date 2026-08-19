package runtime

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Dependencies injects every external collaborator a Factory or Manager
// needs, so adapters never reach for package-level globals and tests can
// replace each one individually. The zero value is not usable: Registry.Create
// validates the required fields before constructing a Runtime.
type Dependencies struct {
	HTTPClient *http.Client
	WSDialer   WSDialer // only used by the ComfyUI adapter; nil is fine otherwise
	Clock      Clock
	Logger     *slog.Logger
	Metrics    Metrics
}

// Clock abstracts time so Manager's periodic scheduling and an adapter's
// timeouts can be driven deterministically in tests instead of through real
// sleeps.
type Clock interface {
	Now() time.Time
	// NewTimer returns a channel that receives once after d elapses, and a
	// stop function with the same semantics as time.Timer.Stop: it reports
	// whether the timer was stopped before firing.
	NewTimer(d time.Duration) (<-chan time.Time, func() bool)
}

// systemClock is the production Clock, backed by the real wall clock.
type systemClock struct{}

// NewSystemClock returns the production Clock backed by time.Now and
// time.NewTimer.
func NewSystemClock() Clock {
	return systemClock{}
}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// WSDialer is the minimal WebSocket dial surface the ComfyUI adapter needs.
// The production implementation wraps github.com/coder/websocket; tests
// substitute a fake that never touches the network.
type WSDialer interface {
	Dial(ctx context.Context, url string, header http.Header) (WSConn, error)
}

// WSConn is the minimal WebSocket connection surface the ComfyUI adapter
// needs: reading inbound frames and closing the connection. Writing is not
// part of this interface because the adapter only subscribes to events.
type WSConn interface {
	Read(ctx context.Context) (messageType int, data []byte, err error)
	Close() error
}

// Metrics accepts only the instruments this package's Observability section
// documents; the implementation is responsible for label cardinality
// control. Runtime code never depends on a specific metrics library.
type Metrics interface {
	Counter(name string, labels map[string]string) Counter
	Gauge(name string, labels map[string]string) Gauge
	Histogram(name string, labels map[string]string) Histogram
}

// Counter is a monotonically increasing instrument.
type Counter interface {
	Add(delta float64)
}

// Gauge is a point-in-time instrument.
type Gauge interface {
	Set(value float64)
}

// Histogram records a distribution of observed values.
type Histogram interface {
	Observe(value float64)
}
