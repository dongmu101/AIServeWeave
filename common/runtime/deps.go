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
	// Quota, when set, is the Agent's record of its CLI subscription
	// accounts' quota: a CLI runtime asks it before starting a turn whether
	// the account is already known to refuse, and tells it what the CLI
	// reports. Nil is fine; the helpers below then do nothing.
	//
	// Quota 设置后，是 Agent 对其 CLI 订阅账户额度的记录：CLI 运行时在开始一个回合之前
	// 询问它账户是否已知会拒绝，并把 CLI 报告的情况告诉它。可以为 nil，此时下面的辅助方法
	// 什么也不做。
	Quota QuotaGuard
}

// QuotaGuard keeps what is known about the quota of the subscription
// accounts behind CLI runtimes, so that a runtime does not send the upstream
// a request it already knows will be refused: an account at its limit is
// asked again only once its quota may be back. Every method must be safe for
// concurrent use and must not block.
//
// QuotaGuard 保存 CLI 运行时背后订阅账户的额度情况，使运行时不会把已知会被拒绝的请求
// 发给上游：已到上限的账户只在额度可能恢复之后才再次请求。每个方法都必须可并发调用且
// 不得阻塞。
type QuotaGuard interface {
	// Notice says runtimeID's CLI reported its account near or at a limit,
	// so the guard should refresh its reading of that account soon.
	//
	// Notice 表示 runtimeID 的 CLI 报告其账户接近或已到上限，守卫应尽快刷新对该账户的读数。
	Notice(runtimeID string)
	// Refused records that runtimeID's account refused a request for model
	// until resetsAt, as the upstream reported. An empty model means the
	// refusing limit covers every model of the account. A zero or past
	// resetsAt records nothing beyond Notice.
	//
	// Refused 记录 runtimeID 的账户拒绝了 model 的请求，按上游的报告直到 resetsAt。model
	// 为空表示拒绝它的限额覆盖该账户的所有模型。resetsAt 为零或已过时，除 Notice 之外
	// 不记录任何东西。
	Refused(runtimeID, model string, resetsAt time.Time)
	// Hold reports whether runtimeID's account is known to refuse model
	// right now, and how long until its quota may be back.
	//
	// Hold 报告 runtimeID 的账户此刻是否已知会拒绝 model，以及距额度可能恢复还有多久。
	Hold(runtimeID, model string) (time.Duration, bool)
}

// NoticeQuota calls deps.Quota.Notice for runtimeID when a guard is set.
//
// NoticeQuota 在设置了守卫时以 runtimeID 调用 deps.Quota.Notice。
func (deps Dependencies) NoticeQuota(runtimeID string) {
	if deps.Quota != nil {
		deps.Quota.Notice(runtimeID)
	}
}

// QuotaRefused calls deps.Quota.Refused when a guard is set.
//
// QuotaRefused 在设置了守卫时调用 deps.Quota.Refused。
func (deps Dependencies) QuotaRefused(runtimeID, model string, resetsAt time.Time) {
	if deps.Quota != nil {
		deps.Quota.Refused(runtimeID, model, resetsAt)
	}
}

// QuotaHold calls deps.Quota.Hold when a guard is set, and otherwise reports
// that nothing is known.
//
// QuotaHold 在设置了守卫时调用 deps.Quota.Hold，否则报告一无所知。
func (deps Dependencies) QuotaHold(runtimeID, model string) (time.Duration, bool) {
	if deps.Quota == nil {
		return 0, false
	}
	return deps.Quota.Hold(runtimeID, model)
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
