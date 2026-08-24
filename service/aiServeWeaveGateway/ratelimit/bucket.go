package ratelimit

import "time"

// bucket is a continuously refilled token bucket. It is the shape both rate
// dimensions use, and it is continuous rather than a per-minute counter for
// one reason: a fixed window lets a caller spend a whole minute's allowance at
// 0:59 and the next at 1:01, which is twice the configured rate inside two
// seconds.
//
// The balance may go negative. Only the token dimension does that — its cost
// is charged after the fact — and a negative balance is exactly the debt that
// refill has to repay before the next request is admitted.
//
// bucket 是一个连续补充的令牌桶。两个速率维度用的都是这个形状；它连续而不是按分钟
// 计数，只为一个理由：固定窗口允许调用方在 0:59 花掉一整分钟的额度、在 1:01 再花掉
// 下一分钟的，两秒之内跑出两倍于配置的速率。
//
// 余额可以为负。只有 token 维度会这样——它的代价是事后扣减的——而负余额恰恰就是补充
// 必须先偿还、然后才能接纳下一个请求的那笔欠账。
type bucket struct {
	// capacity is the per-minute allowance, which doubles as the burst size:
	// a bucket that starts full lets a caller spend a minute's worth at once
	// and then proceed at the sustained rate.
	//
	// capacity 是每分钟额度，同时也是突发容量：初始满桶让调用方可以一次花掉一分钟的
	// 量，随后按持续速率前进。
	capacity float64
	balance  float64
	last     time.Time
}

// configure sets the bucket's capacity, filling it on first use and on any
// change to the configured allowance. A limit raised mid-flight takes effect
// immediately rather than after the old capacity drains, which is what an
// operator raising a limit during an incident expects.
//
// configure 设置桶的容量，在首次使用以及配置额度发生变化时把它填满。运行中调高的
// 限制立即生效，而不是等旧容量耗尽——那正是运维在故障处置中调高限制时所期望的。
func (b *bucket) configure(capacity float64, now time.Time) {
	if b.capacity == capacity {
		return
	}
	b.capacity = capacity
	b.balance = capacity
	b.last = now
}

// refill credits the time elapsed since the last operation.
//
// refill 结算自上次操作以来经过的时间。
func (b *bucket) refill(now time.Time) {
	if b.last.IsZero() {
		b.last = now
		return
	}
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	b.last = now
	b.balance += b.capacity * elapsed.Minutes()
	if b.balance > b.capacity {
		b.balance = b.capacity
	}
}

// take removes n from the bucket, reporting how long to wait when it cannot.
//
// take 从桶中取走 n；取不到时报告需要等待多久。
func (b *bucket) take(n float64, now time.Time) (time.Duration, bool) {
	b.refill(now)
	if b.balance < n {
		return b.waitFor(n, now), false
	}
	b.balance -= n
	return 0, true
}

// available reports whether the bucket holds anything at all, which is the
// entry check for a dimension charged on the way out.
//
// available 报告桶里是否还有任何余量，这是「出口才扣减」的那个维度在入口处的检查。
func (b *bucket) available(now time.Time) bool {
	b.refill(now)
	return b.balance >= 1
}

// spend removes n without checking, letting the balance go negative.
//
// spend 不作检查地取走 n，允许余额变为负数。
func (b *bucket) spend(n float64, now time.Time) {
	b.refill(now)
	b.balance -= n
}

// waitFor is how long until the bucket holds n, derived from the refill rate
// rather than fixed, so a client that honours it retries exactly once rather
// than polling.
//
// waitFor 是桶攒够 n 所需的时间，由补充速率推出而非固定值，这样遵守它的客户端只会
// 重试一次，而不是轮询。
func (b *bucket) waitFor(n float64, now time.Time) time.Duration {
	_ = now
	if b.capacity <= 0 {
		return 0
	}
	missing := n - b.balance
	if missing <= 0 {
		return 0
	}
	minutes := missing / b.capacity
	wait := time.Duration(minutes * float64(time.Minute))
	if wait < time.Second {
		// Sub-second hints round up: a client told to retry in 200ms will
		// arrive before the token exists, be rejected again, and treat the
		// endpoint as flapping.
		//
		// 亚秒级的提示向上取整：一个被告知 200ms 后重试的客户端会在 token 存在之前
		// 就到达、再次被拒，进而把这个端点当成在抖动。
		return time.Second
	}
	return wait
}
