package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/common/quota"
	"AIServeWeave/common/runtime"
)

// This file is the fleet-wide limiter. Everything it does that Memory does not
// comes from one fact: several Gateway replicas share one tenant's allowance,
// so the allowance cannot live in any of them.
//
// Both scripts below are the same token bucket bucket.go implements, written a
// second time in Lua. Two implementations of one rule is exactly the shape this
// repository treats as a defect waiting to happen, and the mitigation is
// structural rather than a promise: contract_test.go is a single suite both
// implementations run, so a divergence fails a test rather than surfacing as a
// tenant billed wrongly. Changing the refill rule means changing it in both
// places and the suite is what proves you did.
//
// 本文件是集群级限流器。它比 Memory 多做的一切，都源于一个事实：多个 Gateway 副本
// 共享同一个租户的额度，因此额度不能存在于它们中的任何一个里。
//
// 下面两段脚本就是 bucket.go 实现的那个令牌桶，用 Lua 又写了一遍。同一条规则的两份
// 实现，正是本仓库视为「迟早要出事」的那种形状，而缓解手段是结构性的而非一句承诺：
// contract_test.go 是两个实现共同运行的同一套测试，因此一次分歧会让测试失败，而不是
// 表现为某个租户被错误计费。改补充规则意味着两处都要改，而那套测试就是「你确实改了」
// 的证明。

// DefaultLeaseTTL bounds how long one concurrency slot survives without being
// released. It exists because a Gateway replica can die mid-request, and a slot
// that is only freed by an explicit release would then be held forever by a
// process that no longer exists.
//
// It must be longer than the longest legitimate request. A generation that runs
// for twenty minutes must not have its slot reclaimed at minute five and then
// be double-counted when it finally releases.
//
// DefaultLeaseTTL 限制一个并发槽在未被释放的情况下能存活多久。它的存在是因为 Gateway
// 副本可能在请求中途死掉，而只靠显式释放来腾出的槽，此后会被一个已经不存在的进程
// 永久占着。
//
// 它必须长于最长的合法请求。一次跑二十分钟的生成，不能在第五分钟就被回收掉槽位，
// 然后在它最终释放时又被重复计数。
const DefaultLeaseTTL = 30 * time.Minute

// keyPrefix namespaces this limiter's keys, so one Redis instance can also hold
// the control plane's key-verification cache without a collision.
//
// keyPrefix 为本限流器的键划定命名空间，这样同一个 Redis 实例还能存放控制面的 key
// 校验缓存而不发生碰撞。
const keyPrefix = "aisw:rl:"

// acquireScript admits or rejects one request, atomically across replicas.
//
// It checks every dimension before spending anything. Checking and spending as
// it goes would mean a request rejected by concurrency had already consumed a
// request token, and that token would be gone: the caller was refused and
// charged for it.
//
// acquireScript 原子地跨副本接纳或拒绝一个请求。
//
// 它在花掉任何东西之前先检查所有维度。边检查边花的话，一个被并发维度拒绝的请求就已经
// 消耗掉了一个请求 token，而那个 token 找不回来了：调用方既被拒绝，又为此付了费。
var acquireScript = redis.NewScript(`
local req_key, tok_key, conc_key = KEYS[1], KEYS[2], KEYS[3]
local now_ms       = tonumber(ARGV[1])
local req_cap      = tonumber(ARGV[2])
local tok_cap      = tonumber(ARGV[3])
local max_conc     = tonumber(ARGV[4])
local lease_id     = ARGV[5]
local lease_ttl_ms = tonumber(ARGV[6])

-- read_bucket returns the balance a bucket holds at now_ms, refilled from the
-- rate its capacity implies. It mirrors bucket.refill in bucket.go.
local function read_bucket(key, cap)
  local vals = redis.call('HMGET', key, 'balance', 'last')
  local balance = tonumber(vals[1])
  local last = tonumber(vals[2])
  if balance == nil or last == nil then
    return cap
  end
  local elapsed_min = (now_ms - last) / 60000.0
  if elapsed_min > 0 then
    balance = balance + cap * elapsed_min
    if balance > cap then balance = cap end
  end
  return balance
end

local function write_bucket(key, balance, cap)
  redis.call('HSET', key, 'balance', balance, 'last', now_ms)
  -- Expire an idle bucket rather than keeping one row per tenant forever. The
  -- window is two minutes of allowance: by then a full bucket is what a fresh
  -- one would be anyway, so forgetting it changes nothing.
  redis.call('PEXPIRE', key, 120000)
end

-- wait_ms mirrors bucket.waitFor, including its one-second floor.
local function wait_ms(balance, needed, cap)
  if cap <= 0 then return 0 end
  local missing = needed - balance
  if missing <= 0 then return 0 end
  local ms = (missing / cap) * 60000.0
  if ms < 1000 then return 1000 end
  return math.floor(ms)
end

local req_balance, tok_balance

-- Phase 1: check everything, spend nothing.
if req_cap > 0 then
  req_balance = read_bucket(req_key, req_cap)
  if req_balance < 1 then
    return {0, 'requests_per_minute', wait_ms(req_balance, 1, req_cap)}
  end
end
if tok_cap > 0 then
  tok_balance = read_bucket(tok_key, tok_cap)
  if tok_balance < 1 then
    return {0, 'tokens_per_minute', wait_ms(tok_balance, 1, tok_cap)}
  end
end
if max_conc > 0 then
  -- Drop leases whose holder died before releasing them. Without this a
  -- crashed replica would hold its slots until the key itself expired.
  redis.call('ZREMRANGEBYSCORE', conc_key, '-inf', now_ms - lease_ttl_ms)
  if redis.call('ZCARD', conc_key) >= max_conc then
    return {0, 'max_concurrent', 1000}
  end
end

-- Phase 2: nothing can refuse the request now, so commit.
if req_cap > 0 then
  write_bucket(req_key, req_balance - 1, req_cap)
end
if tok_cap > 0 then
  write_bucket(tok_key, tok_balance, tok_cap)
end
if max_conc > 0 then
  redis.call('ZADD', conc_key, now_ms, lease_id)
  redis.call('PEXPIRE', conc_key, lease_ttl_ms * 2)
end
return {1, '', 0}
`)

// releaseScript returns a concurrency slot and charges the tokens the backend
// reported. The charge may drive the balance negative, which is the debt refill
// repays — see bucket.spend.
//
// releaseScript 归还一个并发槽，并扣减后端上报的 token。这次扣减可能让余额变负，那
// 正是补充要偿还的欠账——见 bucket.spend。
var releaseScript = redis.NewScript(`
local tok_key, conc_key = KEYS[1], KEYS[2]
local now_ms   = tonumber(ARGV[1])
local lease_id = ARGV[2]
local spent    = tonumber(ARGV[3])
local tok_cap  = tonumber(ARGV[4])

redis.call('ZREM', conc_key, lease_id)

if tok_cap > 0 and spent > 0 then
  local vals = redis.call('HMGET', tok_key, 'balance', 'last')
  local balance = tonumber(vals[1])
  local last = tonumber(vals[2])
  if balance == nil or last == nil then
    balance = tok_cap
  else
    local elapsed_min = (now_ms - last) / 60000.0
    if elapsed_min > 0 then
      balance = balance + tok_cap * elapsed_min
      if balance > tok_cap then balance = tok_cap end
    end
  end
  redis.call('HSET', tok_key, 'balance', balance - spent, 'last', now_ms)
  redis.call('PEXPIRE', tok_key, 120000)
end
return 1
`)

// RedisConfig configures NewRedis.
//
// RedisConfig 配置 NewRedis。
type RedisConfig struct {
	// Client is the connected Redis client. Required.
	//
	// Client 是已连接的 Redis 客户端。必填。
	Client *redis.Client
	// Clock supplies the timestamp the scripts refill against. Nil uses the
	// system clock. Time comes from the Gateway rather than from Redis so the
	// contract suite can drive both implementations with one fake clock.
	//
	// Clock 提供脚本据以补充的时间戳。为 nil 时使用系统时钟。时间来自 Gateway 而不是
	// Redis，这样那套契约测试才能用同一个假时钟驱动两个实现。
	Clock runtime.Clock
	// LeaseTTL overrides DefaultLeaseTTL. It must exceed the longest request
	// this deployment serves.
	//
	// LeaseTTL 覆盖 DefaultLeaseTTL。它必须长于本部署所服务的最长请求。
	LeaseTTL time.Duration
}

// Redis enforces limits across every replica sharing one Redis.
//
// Redis 在共享同一个 Redis 的所有副本之间执行限制。
type Redis struct {
	client   *redis.Client
	clock    runtime.Clock
	leaseTTL time.Duration
}

// NewRedis returns a fleet-wide Limiter.
//
// NewRedis 返回一个集群级 Limiter。
func NewRedis(cfg RedisConfig) (*Redis, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("ratelimit: NewRedis needs a Client")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	ttl := cfg.LeaseTTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	return &Redis{client: cfg.Client, clock: clock, leaseTTL: ttl}, nil
}

// Acquire implements Limiter.
//
// Acquire 实现 Limiter。
func (r *Redis) Acquire(ctx context.Context, tenantID string, limits quota.Limits) (Lease, error) {
	if limits.Unlimited() {
		return noopLease{}, nil
	}
	leaseID, err := newLeaseID()
	if err != nil {
		return nil, err
	}
	now := r.clock.Now().UnixMilli()

	raw, err := acquireScript.Run(ctx, r.client,
		[]string{r.key("req", tenantID), r.key("tok", tenantID), r.key("conc", tenantID)},
		now, limits.RequestsPerMinute, limits.TokensPerMinute, limits.MaxConcurrent,
		leaseID, r.leaseTTL.Milliseconds(),
	).Result()
	if err != nil {
		// Not a rejection: the limiter could not answer. The caller decides
		// whether that fails the request, per Limiter's contract.
		//
		// 这不是拒绝：限流器无法作答。按 Limiter 的契约，由调用方决定这是否让请求
		// 失败。
		return nil, fmt.Errorf("ratelimit: redis acquire: %w", err)
	}
	admitted, reason, retryMS, err := parseAcquireResult(raw)
	if err != nil {
		return nil, err
	}
	if !admitted {
		return nil, &RejectedError{Reason: Reason(reason), RetryAfter: time.Duration(retryMS) * time.Millisecond}
	}
	return &redisLease{owner: r, tenantID: tenantID, leaseID: leaseID, limits: limits}, nil
}

// parseAcquireResult decodes the script's {admitted, reason, retry_ms} reply.
//
// parseAcquireResult 解码脚本返回的 {admitted, reason, retry_ms} 三元组。
func parseAcquireResult(raw any) (admitted bool, reason string, retryMS int64, err error) {
	values, ok := raw.([]any)
	if !ok || len(values) != 3 {
		return false, "", 0, fmt.Errorf("ratelimit: unexpected acquire reply %T", raw)
	}
	code, _ := values[0].(int64)
	reason, _ = values[1].(string)
	retryMS, _ = values[2].(int64)
	return code == 1, reason, retryMS, nil
}

func (r *Redis) key(dimension, tenantID string) string {
	return keyPrefix + dimension + ":" + tenantID
}

// release runs the release script, at most once per lease.
//
// release 运行释放脚本，每个租约最多一次。
func (r *Redis) release(ctx context.Context, tenantID, leaseID string, limits quota.Limits, tokens int) error {
	_, err := releaseScript.Run(ctx, r.client,
		[]string{r.key("tok", tenantID), r.key("conc", tenantID)},
		r.clock.Now().UnixMilli(), leaseID, tokens, limits.TokensPerMinute,
	).Result()
	return err
}

// redisLease is one admitted request's slot in the shared allowance.
//
// redisLease 是一个被接纳的请求在共享额度中占的位置。
type redisLease struct {
	owner    *Redis
	tenantID string
	leaseID  string
	limits   quota.Limits
	// once guards against a double release, the same way memoryLease does: a
	// handler releases with defer and may also release explicitly on an early
	// return, and returning one slot twice hands a tenant concurrency it is
	// not entitled to.
	//
	// once 防止重复释放，与 memoryLease 的做法一致：处理器用 defer 释放，也可能在
	// 提前返回时显式释放一次，而把一个槽归还两次，会让租户拿到不该有的并发额度。
	once sync.Once
}

// Release returns the slot. A failure here is logged by the caller, not
// retried: the lease's own TTL is the backstop, which is why that TTL exists.
//
// Release 归还槽位。这里的失败由调用方记录而不重试：租约自身的 TTL 就是兜底，那正是
// 那个 TTL 存在的理由。
func (l *redisLease) Release(ctx context.Context, tokens int) {
	l.once.Do(func() {
		_ = l.owner.release(ctx, l.tenantID, l.leaseID, l.limits, tokens)
	})
}

func newLeaseID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ratelimit: generating a lease id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
