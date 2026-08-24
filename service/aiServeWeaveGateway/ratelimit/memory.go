package ratelimit

import (
	"context"
	"sync"
	"time"

	"AIServeWeave/common/quota"
	"AIServeWeave/common/runtime"
)

// DefaultIdleTTL is how long a tenant with no in-flight request and no recent
// activity is kept before its buckets are dropped. The map is keyed by tenant,
// and while a tenant id is not caller-chosen, a long-lived process serving many
// tenants would otherwise accumulate one entry per tenant that ever called and
// never release it.
//
// DefaultIdleTTL 是一个既无在途请求、也无近期活动的租户，其桶被丢弃前保留的时长。
// 这张映射以租户为键，虽然租户 id 不由调用方选定，但一个长期运行、服务众多租户的
// 进程，否则会为每一个曾经调用过的租户各留一条永不释放的记录。
const DefaultIdleTTL = 10 * time.Minute

// MemoryConfig configures NewMemory. Every field is optional.
//
// MemoryConfig 配置 NewMemory。所有字段均为可选。
type MemoryConfig struct {
	// Clock supplies time for bucket refill. Nil uses the system clock; tests
	// inject a fake so refill is exercised without sleeping.
	//
	// Clock 为桶的补充提供时间。为 nil 时使用系统时钟；测试注入假时钟，好在不睡眠的
	// 前提下检验补充行为。
	Clock runtime.Clock
	// IdleTTL overrides DefaultIdleTTL.
	//
	// IdleTTL 覆盖 DefaultIdleTTL。
	IdleTTL time.Duration
}

// Memory enforces limits within one replica. Across a fleet it admits N times
// the configured allowance, one full allowance per replica — see the package
// doc, and the Gateway README, which records this as the known cost of not
// requiring Redis.
//
// Memory 在单个副本内执行限制。在整个集群上它会放行 N 倍的配置额度，每副本一份完整
// 额度——见包文档，以及 Gateway README，那里把这一点记录为「不要求 Redis」所付的
// 已知代价。
type Memory struct {
	clock   runtime.Clock
	idleTTL time.Duration

	mu      sync.Mutex
	tenants map[string]*tenantState
}

// tenantState is one tenant's live allowance.
//
// tenantState 是一个租户的实时额度。
type tenantState struct {
	requests bucket
	tokens   bucket
	// inflight counts leases taken and not yet released. A tenant with a
	// non-zero count is never swept, because sweeping it would reset that
	// count to zero underneath requests that are still running.
	//
	// inflight 统计已取得但尚未释放的租约。计数非零的租户永不被清理，因为清理它会在
	// 仍在运行的请求脚下把这个计数清零。
	inflight int
	lastSeen time.Time
}

// NewMemory returns an in-replica Limiter.
//
// NewMemory 返回一个副本内的 Limiter。
func NewMemory(cfg MemoryConfig) *Memory {
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	ttl := cfg.IdleTTL
	if ttl <= 0 {
		ttl = DefaultIdleTTL
	}
	return &Memory{clock: clock, idleTTL: ttl, tenants: make(map[string]*tenantState)}
}

// Acquire implements Limiter.
//
// The three dimensions are checked in the order they are cheapest to give back:
// concurrency last, because it is the only one Acquire has to undo if a later
// check fails. Requests and tokens are checked first and, on rejection, nothing
// has been spent.
//
// Acquire 实现 Limiter。
//
// 三个维度按「归还代价从低到高」的顺序检查：并发放在最后，因为它是唯一一个在后续检查
// 失败时 Acquire 必须撤销的东西。请求与 token 先检查，被拒时什么都还没花掉。
func (m *Memory) Acquire(ctx context.Context, tenantID string, limits quota.Limits) (Lease, error) {
	_ = ctx
	if limits.Unlimited() {
		return noopLease{}, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	m.sweepLocked(now)

	state, ok := m.tenants[tenantID]
	if !ok {
		state = &tenantState{}
		m.tenants[tenantID] = state
	}
	state.lastSeen = now

	if limits.RequestsPerMinute > 0 {
		state.requests.configure(float64(limits.RequestsPerMinute), now)
		if wait, ok := state.requests.take(1, now); !ok {
			return nil, &RejectedError{Reason: ReasonRequests, RetryAfter: wait}
		}
	}
	if limits.TokensPerMinute > 0 {
		state.tokens.configure(float64(limits.TokensPerMinute), now)
		// Tokens are charged on release, so entry only checks that the bucket
		// is not already empty. Requiring a whole request's worth up front
		// would mean guessing a cost nothing has measured yet.
		//
		// token 在释放时扣减，因此入口只检查桶是否已经空了。在入口就要求一整个请求
		// 的量，等于去猜一个还没有任何东西度量过的代价。
		if !state.tokens.available(now) {
			return nil, &RejectedError{Reason: ReasonTokens, RetryAfter: state.tokens.waitFor(1, now)}
		}
	}
	if limits.MaxConcurrent > 0 && state.inflight >= limits.MaxConcurrent {
		// Concurrency has no refill rate, so there is no honest wait to
		// compute: the slot frees when some other request finishes, which
		// this side cannot predict. The hint is the smallest one that is not
		// a busy-retry invitation.
		//
		// 并发没有补充速率，因此算不出诚实的等待时长：槽在别的请求结束时才腾出，而
		// 这一侧无法预测。这里的提示取「不至于变成忙重试邀请」的最小值。
		return nil, &RejectedError{Reason: ReasonConcurrency, RetryAfter: time.Second}
	}
	state.inflight++

	return &memoryLease{owner: m, tenantID: tenantID}, nil
}

// Tracked is how many tenants currently hold state. It exists for the sweep's
// test, which otherwise could not observe that forgetting happened.
//
// Tracked 是当前持有状态的租户数。它为清理逻辑的测试而存在，否则测试无从观察到
// 「遗忘」确实发生了。
func (m *Memory) Tracked() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tenants)
}

// sweepLocked drops tenants that have been idle past the TTL and hold no
// in-flight lease. It runs on the Acquire path rather than in a goroutine: a
// limiter that needed its own goroutine would need its own shutdown, and this
// map only grows when Acquire is called anyway.
//
// sweepLocked 丢弃空闲超过 TTL、且没有在途租约的租户。它跑在 Acquire 路径上而不是
// 协程里：需要自带协程的限流器就需要自带关停，而这张映射本来也只在 Acquire 被调用时
// 才会增长。
func (m *Memory) sweepLocked(now time.Time) {
	for id, state := range m.tenants {
		if state.inflight == 0 && now.Sub(state.lastSeen) > m.idleTTL {
			delete(m.tenants, id)
		}
	}
}

// release returns one lease's hold. It is idempotent through memoryLease's own
// once guard, so this method assumes it is called at most once per lease.
//
// release 归还一个租约的占用。幂等性由 memoryLease 自身的 once 保证，因此本方法假定
// 每个租约最多调用它一次。
func (m *Memory) release(tenantID string, tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.tenants[tenantID]
	if !ok {
		// The tenant was swept while this request ran, which the sweep's
		// in-flight guard is meant to prevent. Nothing to return.
		//
		// 该租户在本请求运行期间被清理了，而清理的在途保护正是为了防止这件事。
		// 没有什么可归还的。
		return
	}
	if state.inflight > 0 {
		state.inflight--
	}
	if tokens > 0 {
		// Spending past empty is allowed and leaves the bucket negative: the
		// debt is repaid by refill, so an oversized response delays the
		// requests behind it by exactly what it cost.
		//
		// 允许透支，桶会变成负数：欠账靠补充偿还，因此一次超大的响应把它后面的请求
		// 恰好延迟它所耗费的那么多。
		state.tokens.spend(float64(tokens), m.clock.Now())
	}
	state.lastSeen = m.clock.Now()
}

// memoryLease is one admitted request, released at most once.
//
// memoryLease 是一个被接纳的请求，最多释放一次。
type memoryLease struct {
	owner    *Memory
	tenantID string
	once     sync.Once
}

func (l *memoryLease) Release(ctx context.Context, tokens int) {
	_ = ctx
	l.once.Do(func() { l.owner.release(l.tenantID, tokens) })
}

// noopLease is what an unlimited tenant gets: nothing was reserved, so nothing
// is returned.
//
// noopLease 是不受限租户所得到的：什么都没预留，因此什么都不必归还。
type noopLease struct{}

func (noopLease) Release(context.Context, int) {}
