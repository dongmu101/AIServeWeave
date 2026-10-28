package scheduler

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

// Default thresholds for breakerRegistry, used whenever a Config leaves the
// corresponding field at its zero value. These are a starting point, not a
// value validated against real traffic — like the tunnel README's own
// node_total default, the real number has to wait for production numbers.
const (
	defaultFailureThreshold = 5
	defaultBaseCooldown     = 5 * time.Second
	defaultMaxCooldown      = 2 * time.Minute
)

// Default bounds for the rate-limit cooldown, used whenever a Config leaves
// RateLimitCooldown or MaxRateLimitCooldown at zero. The default applies when
// the backend gave no reset time; the cap bounds a reset time that is far
// away (a weekly subscription window), so one request an hour still finds out
// whether the quota came back early — a plan upgrade, a manual reset.
//
// 限流冷却的默认边界，Config 的 RateLimitCooldown 或 MaxRateLimitCooldown 为零时使用。
// 默认值用于后端没有给出重置时间的情况；上限约束遥远的重置时间（按周的订阅窗口），使得
// 每小时仍有一次请求去确认额度是否提前恢复——套餐升级、手动重置。
const (
	defaultRateLimitCooldown    = 30 * time.Second
	defaultMaxRateLimitCooldown = time.Hour
)

// breakerFailure reports whether err should count toward a candidate's
// consecutive-failure streak. Only the three codes that plausibly mean "this
// node instance itself is broken" qualify: a connection that cannot be
// established, a request that times out, or the upstream answering with an
// error. ErrorBackpressure and ErrorRateLimited are deliberately excluded —
// they mean "busy right now", not "broken", which is the distinction
// README's 调度流程 section already drew when it said backpressure does not
// count toward circuit-breaking. Every other code is either not retryable to
// begin with (so candidates() never gets a second chance to skip the node
// anyway) or is a client-side problem the node cannot fix by resting.
func breakerFailure(err error) bool {
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		return false
	}
	switch rtErr.Code {
	case runtime.ErrorConnection, runtime.ErrorTimeout, runtime.ErrorUpstream:
		return true
	default:
		return false
	}
}

// breakerState is one candidate's circuit-breaker bookkeeping.
//
// limitedUntil is independent of the breaker proper: a rate-limited
// candidate is not broken, it has run out of quota, so the answer never
// counts toward a trip — like any other answer from the node it ends the
// failure streak instead. It only says "do not send work here before this
// instant".
//
// limitedUntil 独立于熔断本身：被限流的候选没有坏，只是额度用完了，所以这个答复从不
// 计入跳闸——与节点的其他答复一样，它反而结束失败连击。它只表示「在此刻之前不要往这里
// 派活」。
type breakerState struct {
	consecutiveFailures int
	tripCount           int
	open                bool
	openUntil           time.Time
	limitedUntil        time.Time
}

// breakerRegistry tracks one breakerState per Candidate. All access goes
// through a single mutex: candidates() reads it once per scheduling decision
// and record is called once per dispatch attempt, which is nowhere near hot
// enough to need finer-grained locking.
type breakerRegistry struct {
	failureThreshold int
	baseCooldown     time.Duration
	maxCooldown      time.Duration
	limitCooldown    time.Duration
	maxLimitCooldown time.Duration
	metrics          *recorder
	logger           *slog.Logger

	mu      sync.Mutex
	entries map[Candidate]*breakerState
}

func newBreakerRegistry(failureThreshold int, baseCooldown, maxCooldown time.Duration, rec *recorder) *breakerRegistry {
	if failureThreshold <= 0 {
		failureThreshold = defaultFailureThreshold
	}
	if baseCooldown <= 0 {
		baseCooldown = defaultBaseCooldown
	}
	if maxCooldown <= 0 {
		maxCooldown = defaultMaxCooldown
	}
	if maxCooldown < baseCooldown {
		maxCooldown = baseCooldown
	}
	return &breakerRegistry{
		failureThreshold: failureThreshold,
		baseCooldown:     baseCooldown,
		maxCooldown:      maxCooldown,
		limitCooldown:    defaultRateLimitCooldown,
		maxLimitCooldown: defaultMaxRateLimitCooldown,
		metrics:          rec,
		logger:           slog.New(slog.DiscardHandler),
		entries:          make(map[Candidate]*breakerState),
	}
}

// withRateLimit sets the rate-limit cooldown bounds and the logger that
// reports each cooldown, applying the same zero-means-default rule as
// newBreakerRegistry. It is called once, before the registry is shared.
//
// withRateLimit 设置限流冷却的边界及报告每次冷却的日志器，与 newBreakerRegistry 一样
// 以零值表示默认。它只在注册表被共享之前调用一次。
func (r *breakerRegistry) withRateLimit(limitCooldown, maxLimitCooldown time.Duration, logger *slog.Logger) *breakerRegistry {
	if limitCooldown > 0 {
		r.limitCooldown = limitCooldown
	}
	if maxLimitCooldown > 0 {
		r.maxLimitCooldown = maxLimitCooldown
	}
	r.maxLimitCooldown = max(r.maxLimitCooldown, r.limitCooldown)
	if logger != nil {
		r.logger = logger
	}
	return r
}

// limited reports how much of c's rate-limit cooldown is left at now, and
// whether there is any. It is a separate question from eligible: a cooling
// candidate is skipped at dispatch time rather than dropped from the
// candidate set, so a model whose every candidate is cooling answers
// rate_limited with a retry time instead of "no node serves this model".
//
// limited 报告 c 在 now 时刻还剩多少限流冷却，以及是否还在冷却中。它与 eligible 是两个
// 问题：冷却中的候选在派发时被跳过，而不是从候选集中删除，因此一个所有候选都在冷却的
// 模型会回答带重试时间的 rate_limited，而不是「没有节点能服务这个模型」。
func (r *breakerRegistry) limited(c Candidate, now time.Time) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.entries[c]
	if !ok || !now.Before(st.limitedUntil) {
		return 0, false
	}
	return st.limitedUntil.Sub(now), true
}

// limitWindow is how long a rate-limited candidate is set aside: the
// backend's own RetryAfter when it gave one, otherwise limitCooldown, never
// more than maxLimitCooldown.
//
// limitWindow 是被限流的候选被搁置的时长：后端给了 RetryAfter 就用它，否则用
// limitCooldown，且从不超过 maxLimitCooldown。
func (r *breakerRegistry) limitWindow(retryAfter time.Duration) time.Duration {
	if retryAfter <= 0 {
		retryAfter = r.limitCooldown
	}
	return min(retryAfter, r.maxLimitCooldown)
}

// eligible reports whether c may be tried right now. A candidate with no
// recorded failures is always eligible; an open breaker becomes eligible
// again once its cooldown has elapsed, so the next attempt doubles as a
// probe — see the package-level note on why this skips a dedicated
// half-open state.
func (r *breakerRegistry) eligible(c Candidate, now time.Time) bool {
	r.mu.Lock()
	st, ok := r.entries[c]
	switch {
	case !ok || !st.open:
		r.mu.Unlock()
		return true
	case now.Before(st.openUntil):
		r.mu.Unlock()
		return false
	}
	// The cooldown has elapsed, so the candidate is eligible again even
	// though the breaker has not yet been reset by a successful probe. The
	// gauge follows eligibility rather than the internal open flag: an
	// operator reading it wants to know whether this candidate is being
	// excluded right now, not which field says so.
	//
	// 冷却已过，因此即便熔断器尚未被一次成功探测重置，该候选也重新可选。量表跟随的是
	// 「是否可选」而不是内部的 open 标志：读它的运维想知道的是这个候选此刻是否正被
	// 排除，而不是哪个字段这么写。
	r.mu.Unlock()
	r.metrics.BreakerOpen(c, false)
	return true
}

// record applies the outcome of one dispatch attempt against c. Only errors
// breakerFailure recognizes move the state machine; any other outcome
// (success or a non-qualifying error) resets the failure streak, since a
// non-qualifying error already says nothing about whether the node itself is
// healthy.
func (r *breakerRegistry) record(c Candidate, err error, now time.Time) {
	// Whether this call changed the breaker's decision is worked out under the
	// lock and published after it: the recorder is a foreign object, and
	// calling into one while holding a lock is how a lock ordering rule gets
	// invented by accident.
	//
	// 这次调用是否改变了熔断器的判断，在锁内算出、锁外发布：记录器是外部对象，持锁
	// 调用外部对象，正是一条锁顺序规则被意外发明出来的方式。
	var (
		closed  bool
		tripped bool
		limited time.Duration
	)

	r.mu.Lock()
	if retryAfter, ok := rateLimitedError(err); ok {
		// The node answered, so the failure streak ends here exactly as for
		// any other non-qualifying error; on top of that the candidate rests
		// until its quota comes back.
		//
		// 节点作出了回答，因此失败连击与其他不计入熔断的错误一样就此结束；在此之上，候选
		// 还要休息到它的额度恢复。
		st, ok := r.entries[c]
		if !ok {
			st = &breakerState{}
			r.entries[c] = st
		}
		closed = st.open
		st.consecutiveFailures = 0
		st.tripCount = 0
		st.open = false
		limited = r.limitWindow(retryAfter)
		st.limitedUntil = now.Add(limited)
	} else if !breakerFailure(err) {
		if st, ok := r.entries[c]; ok {
			closed = st.open
			st.consecutiveFailures = 0
			st.tripCount = 0
			st.open = false
			if err == nil {
				// A success proves the quota is back, whatever the reset
				// time said.
				// 一次成功证明额度已经恢复，不管重置时间怎么说。
				st.limitedUntil = time.Time{}
			}
		}
	} else {
		st, ok := r.entries[c]
		if !ok {
			st = &breakerState{}
			r.entries[c] = st
		}
		st.consecutiveFailures++
		if st.consecutiveFailures >= r.failureThreshold {
			st.open = true
			st.openUntil = now.Add(r.cooldown(st.tripCount))
			st.tripCount++
			tripped = true
		}
	}
	r.mu.Unlock()

	switch {
	case tripped:
		r.metrics.BreakerTrip(c)
		r.metrics.BreakerOpen(c, true)
	case closed:
		r.metrics.BreakerOpen(c, false)
	}
	if limited > 0 {
		r.logger.Info("candidate rate limited; cooling down until its quota resets",
			slog.String("node_id", c.NodeID), slog.String("runtime_id", c.RuntimeID),
			slog.Duration("cooldown", limited))
	}
}

// rateLimitedError reports whether err is a rate_limited RuntimeError, and the
// RetryAfter it carries.
//
// rateLimitedError 报告 err 是否为 rate_limited 的 RuntimeError，以及它携带的 RetryAfter。
func rateLimitedError(err error) (time.Duration, bool) {
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorRateLimited {
		return 0, false
	}
	return rtErr.RetryAfter, true
}

// cooldown returns the open-window duration for the tripCount'th trip:
// baseCooldown doubled once per trip, capped at maxCooldown. No jitter is
// applied — this paces one Gateway replica's own probes against one node,
// not many independent clients converging on a shared server, so there is no
// thundering-herd problem to spread out.
func (r *breakerRegistry) cooldown(tripCount int) time.Duration {
	const maxShift = 32 // more than enough to saturate past maxCooldown
	shift := min(tripCount, maxShift)
	d := r.baseCooldown << shift
	if d <= 0 || d > r.maxCooldown {
		return r.maxCooldown
	}
	return d
}
