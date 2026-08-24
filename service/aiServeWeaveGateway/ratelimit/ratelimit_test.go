package ratelimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/common/quota"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/ratelimit"
)

// This file is the contract every Limiter implementation must satisfy. It runs
// against Memory unconditionally and against Redis when one is configured (see
// redis_test.go), because the token bucket exists twice — once in Go, once in
// Lua — and a rule that lives in two places needs one test that proves both
// places still agree.
//
// Every case takes its Limiter from a factory rather than constructing one, so
// adding an implementation means adding a factory, not a second copy of these
// assertions.
//
// 本文件是每个 Limiter 实现都必须满足的契约。它无条件针对 Memory 运行，并在配置了
// Redis 时针对 Redis 运行（见 redis_test.go），因为那个令牌桶存在两份——一份 Go、
// 一份 Lua——而一条活在两个地方的规则，需要一个证明两处仍然一致的测试。
//
// 每个用例都从工厂取 Limiter 而不是自己构造，因此新增一个实现意味着新增一个工厂，
// 而不是把这些断言再抄一遍。

// factory builds a Limiter driven by clock. An implementation that cannot be
// driven by an injected clock cannot join this suite, which is why both take
// one.
//
// factory 用 clock 驱动构造一个 Limiter。无法由注入时钟驱动的实现不能加入本套件，
// 这正是两个实现都接受时钟的原因。
type factory struct {
	name string
	// build returns a Limiter and, when the implementation supports it, a
	// function reporting how many tenants it tracks. A nil tracked means the
	// sweep case is skipped for that implementation.
	//
	// build 返回一个 Limiter；实现支持时还返回一个报告其跟踪租户数的函数。tracked
	// 为 nil 时，该实现跳过清理相关的用例。
	build func(t *testing.T, clock *gatewaytest.Clock) (limiter ratelimit.Limiter, tracked func() int)
}

// memoryFactory is the in-replica implementation, always available.
//
// memoryFactory 是副本内实现，始终可用。
func memoryFactory(idleTTL time.Duration) factory {
	return factory{
		name: "memory",
		build: func(t *testing.T, clock *gatewaytest.Clock) (ratelimit.Limiter, func() int) {
			m := ratelimit.NewMemory(ratelimit.MemoryConfig{Clock: clock, IdleTTL: idleTTL})
			return m, m.Tracked
		},
	}
}

// factories is what the suite runs against. redis_test.go appends to it when
// AISW_REDIS_ADDR names a reachable server.
//
// factories 是本套件运行的对象。AISW_REDIS_ADDR 指向一个可达服务器时，redis_test.go
// 会向它追加。
func factories(t *testing.T, idleTTL time.Duration) []factory {
	out := []factory{memoryFactory(idleTTL)}
	if f, ok := redisFactory(t); ok {
		out = append(out, f)
	}
	return out
}

// runSuite runs one case against every available implementation.
//
// runSuite 针对每一个可用实现运行同一个用例。
func runSuite(t *testing.T, idleTTL time.Duration, body func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, tracked func() int)) {
	t.Helper()
	for _, f := range factories(t, idleTTL) {
		t.Run(f.name, func(t *testing.T) {
			clock := gatewaytest.NewClock()
			l, tracked := f.build(t, clock)
			body(t, l, clock, tracked)
		})
	}
}

// acquire is the call under test, reduced to what every case here asserts on.
//
// acquire 是被测调用，收敛成本文件每个用例都会断言的那部分。
func acquire(t *testing.T, l ratelimit.Limiter, tenant string, limits quota.Limits) (ratelimit.Lease, error) {
	t.Helper()
	return l.Acquire(context.Background(), tenant, limits)
}

// rejection unwraps the typed rejection, failing the test when err is not one.
//
// rejection 拆出类型化的拒绝错误；err 不是拒绝时让测试失败。
func rejection(t *testing.T, err error) *ratelimit.RejectedError {
	t.Helper()
	var rejected *ratelimit.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error = %v (%T), want *ratelimit.RejectedError", err, err)
	}
	return rejected
}

func TestUnlimitedTenantIsNeverRejected(t *testing.T) {
	runSuite(t, 0, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, _ func() int) {

		for i := 0; i < 1000; i++ {
			lease, err := acquire(t, l, "tnt_a", quota.Limits{})
			if err != nil {
				t.Fatalf("request %d: Acquire() error = %v, want nil for an unlimited tenant", i, err)
			}
			lease.Release(context.Background(), 1_000_000)
		}
	})
}

func TestRequestsPerMinuteAllowsTheFullBurstThenRejects(t *testing.T) {
	runSuite(t, 0, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, _ func() int) {
		limits := quota.Limits{RequestsPerMinute: 3}

		for i := 0; i < 3; i++ {
			lease, err := acquire(t, l, "tnt_a", limits)
			if err != nil {
				t.Fatalf("request %d: Acquire() error = %v, want nil within the burst", i, err)
			}
			lease.Release(context.Background(), 0)
		}

		_, err := acquire(t, l, "tnt_a", limits)
		rejected := rejection(t, err)
		if rejected.Reason != ratelimit.ReasonRequests {
			t.Errorf("reason = %q, want %q", rejected.Reason, ratelimit.ReasonRequests)
		}
		if rejected.RetryAfter <= 0 {
			t.Errorf("RetryAfter = %v, want a positive hint", rejected.RetryAfter)
		}
	})
}

// TestRequestsRefillContinuously is why the bucket is not a per-minute
// counter: a fixed window lets a caller spend one minute's allowance at 0:59
// and the next at 1:01, which is twice the configured rate in two seconds.
//
// TestRequestsRefillContinuously 说明这个桶为什么不是「每分钟计数器」：固定窗口允许
// 调用方在 0:59 花掉一分钟的额度、在 1:01 再花掉下一分钟的，两秒内跑出两倍于配置的
// 速率。
func TestRequestsRefillContinuously(t *testing.T) {
	runSuite(t, 0, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, _ func() int) {
		limits := quota.Limits{RequestsPerMinute: 60} // one per second

		for i := 0; i < 60; i++ {
			lease, err := acquire(t, l, "tnt_a", limits)
			if err != nil {
				t.Fatalf("request %d: Acquire() error = %v", i, err)
			}
			lease.Release(context.Background(), 0)
		}
		if _, err := acquire(t, l, "tnt_a", limits); err == nil {
			t.Fatal("Acquire() error = nil after the bucket was drained, want a rejection")
		}

		// One second of refill buys exactly one request, not sixty.
		//
		// 一秒的补充恰好买到一个请求，而不是六十个。
		clock.Advance(time.Second)
		lease, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("Acquire() after one second of refill: %v", err)
		}
		lease.Release(context.Background(), 0)
		if _, err := acquire(t, l, "tnt_a", limits); err == nil {
			t.Error("Acquire() error = nil, want the refill to have bought exactly one request")
		}
	})
}

func TestTenantsAreIsolated(t *testing.T) {
	runSuite(t, 0, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, _ func() int) {
		limits := quota.Limits{RequestsPerMinute: 1}

		lease, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("tenant A: %v", err)
		}
		lease.Release(context.Background(), 0)
		if _, err := acquire(t, l, "tnt_a", limits); err == nil {
			t.Fatal("tenant A was not rejected after spending its only request")
		}

		lease, err = acquire(t, l, "tnt_b", limits)
		if err != nil {
			t.Errorf("tenant B: Acquire() error = %v, want nil — one tenant's spend must not bind another", err)
		} else {
			lease.Release(context.Background(), 0)
		}
	})
}

func TestMaxConcurrentCountsOnlyUnreleasedLeases(t *testing.T) {
	runSuite(t, 0, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, _ func() int) {
		limits := quota.Limits{MaxConcurrent: 2}

		first, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("first: %v", err)
		}
		second, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("second: %v", err)
		}

		_, err = acquire(t, l, "tnt_a", limits)
		rejected := rejection(t, err)
		if rejected.Reason != ratelimit.ReasonConcurrency {
			t.Errorf("reason = %q, want %q", rejected.Reason, ratelimit.ReasonConcurrency)
		}

		// Releasing one frees exactly one slot.
		//
		// 释放一个恰好腾出一个槽。
		first.Release(context.Background(), 0)
		third, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("after releasing one: %v", err)
		}
		third.Release(context.Background(), 0)
		second.Release(context.Background(), 0)
	})
}

// TestReleaseIsIdempotent matters because a handler releases with defer and
// may also release explicitly on an early return. Double-counting a release
// would hand a tenant more concurrency than it is entitled to.
//
// TestReleaseIsIdempotent 之所以重要，是因为处理器用 defer 释放，也可能在提前返回时
// 显式释放一次。把一次释放算作两次，会让租户拿到超出应得的并发额度。
func TestReleaseIsIdempotent(t *testing.T) {
	runSuite(t, 0, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, _ func() int) {
		limits := quota.Limits{MaxConcurrent: 1}

		lease, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		lease.Release(context.Background(), 0)
		lease.Release(context.Background(), 0)
		lease.Release(context.Background(), 0)

		held, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("after releases: %v", err)
		}
		if _, err := acquire(t, l, "tnt_a", limits); err == nil {
			t.Error("three releases of one lease freed more than one slot")
		}
		held.Release(context.Background(), 0)
	})
}

// TestTokensAreChargedAfterTheResponse covers the dimension whose cost is not
// known until the backend reports it: the request that overspends is allowed
// through, and the requests behind it pay for the overspend.
//
// TestTokensAreChargedAfterTheResponse 覆盖那个「后端上报之前无从得知代价」的维度：
// 超支的那个请求被放行，超支由排在它后面的请求偿付。
func TestTokensAreChargedAfterTheResponse(t *testing.T) {
	runSuite(t, 0, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, _ func() int) {
		limits := quota.Limits{TokensPerMinute: 1000}

		lease, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("first request: %v", err)
		}
		// One oversized response drains the whole minute's token allowance.
		//
		// 一次超大的响应耗尽整分钟的 token 额度。
		lease.Release(context.Background(), 5000)

		_, err = acquire(t, l, "tnt_a", limits)
		rejected := rejection(t, err)
		if rejected.Reason != ratelimit.ReasonTokens {
			t.Errorf("reason = %q, want %q", rejected.Reason, ratelimit.ReasonTokens)
		}

		// The overspend is repaid by time, not forgiven: a 5000-token spend
		// against a 1000/minute allowance takes five minutes to clear.
		//
		// 超支靠时间偿还而不是一笔勾销：1000/分钟的额度上花掉 5000，需要五分钟才能还清。
		clock.Advance(4 * time.Minute)
		if _, err := acquire(t, l, "tnt_a", limits); err == nil {
			t.Error("Acquire() error = nil after four of the five minutes owed, want it still rejected")
		}
		clock.Advance(time.Minute + time.Second)
		if _, err := acquire(t, l, "tnt_a", limits); err != nil {
			t.Errorf("Acquire() error = %v once the overspend was repaid, want nil", err)
		}
	})
}

// TestIdleTenantsAreForgotten keeps the tenant table from being an unbounded
// map keyed by something a caller controls the cardinality of.
//
// TestIdleTenantsAreForgotten 防止租户表变成一张无界的映射，而它的键的基数正是调用方
// 能左右的。
func TestIdleTenantsAreForgotten(t *testing.T) {
	runSuite(t, time.Minute, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, tracked func() int) {
		if tracked == nil {
			t.Skip("this implementation keeps no per-tenant state to sweep; its equivalent is Redis's own key expiry")
		}
		limits := quota.Limits{RequestsPerMinute: 60}

		lease, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		lease.Release(context.Background(), 0)
		if got := tracked(); got != 1 {
			t.Fatalf("Tracked() = %d, want 1", got)
		}

		clock.Advance(2 * time.Minute)
		// A request for another tenant is what drives the sweep; nothing here
		// runs a background goroutine for it.
		//
		// 触发清理的是另一个租户的请求；这里没有为它跑任何后台协程。
		lease, err = acquire(t, l, "tnt_b", limits)
		if err != nil {
			t.Fatalf("Acquire for tnt_b: %v", err)
		}
		lease.Release(context.Background(), 0)

		if got := tracked(); got != 1 {
			t.Errorf("Tracked() = %d after the idle tenant aged out, want 1 (only tnt_b)", got)
		}
	})
}

// TestHeldLeasesKeepATenantTracked stops the sweep from forgetting a tenant
// whose requests are still in flight, which would reset its concurrency count
// to zero underneath them.
//
// TestHeldLeasesKeepATenantTracked 阻止清理忘掉一个仍有请求在途的租户——那会在这些
// 请求脚下把它的并发计数清零。
func TestHeldLeasesKeepATenantTracked(t *testing.T) {
	runSuite(t, time.Minute, func(t *testing.T, l ratelimit.Limiter, clock *gatewaytest.Clock, tracked func() int) {
		_ = tracked
		limits := quota.Limits{MaxConcurrent: 1}

		held, err := acquire(t, l, "tnt_a", limits)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}

		clock.Advance(2 * time.Minute)
		lease, err := acquire(t, l, "tnt_b", limits)
		if err != nil {
			t.Fatalf("Acquire for tnt_b: %v", err)
		}
		lease.Release(context.Background(), 0)

		if _, err := acquire(t, l, "tnt_a", limits); err == nil {
			t.Error("tnt_a accepted a second concurrent request; its held lease was swept away")
		}
		held.Release(context.Background(), 0)
	})
}
