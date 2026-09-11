// Package cache holds the Redis cache in front of key verification.
//
// Verification is the one query on the inference request path: every call the
// Gateway serves resolves a key hash to a tenant. Serving that from PostgreSQL
// every time works, and is what happens when no Redis is configured — but it
// puts the control plane's database in the data plane's latency budget, which
// is not where it belongs.
//
// The cache stores positives only. A hash that is not in Redis is looked up in
// PostgreSQL; a hash that is in Redis is trusted for the configured TTL. There
// is deliberately no negative caching: an attacker probing with invented keys
// would otherwise fill the cache with entries that serve nobody, and a lookup
// that misses is already the cheap path.
//
// cache 包持有 key 校验前面的 Redis 缓存。
//
// 校验是推理请求路径上唯一的一次查询：Gateway 服务的每一次调用，都要把一个 key 哈希
// 解析成一个租户。每次都从 PostgreSQL 取也能工作，未配置 Redis 时就是这么做的——但那
// 会把控制面的数据库放进数据面的延迟预算里，而它不该在那儿。
//
// 缓存只存正向结果。不在 Redis 中的哈希会去 PostgreSQL 查；在 Redis 中的哈希在配置的
// TTL 内被信任。这里刻意没有负向缓存：否则拿着编造的 key 试探的攻击者，会用一堆不为
// 任何人服务的条目把缓存填满，而未命中本就是那条廉价路径。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
)

// keyPrefix namespaces this cache's entries, so the same Redis instance can
// hold other things without a hash colliding with an unrelated key.
//
// keyPrefix 为本缓存的条目划定命名空间，这样同一个 Redis 实例还能存放其他内容，而
// 不会让某个哈希与无关的键发生碰撞。
const keyPrefix = "aisw:apikey:"

const generationKey = keyPrefix + "generation"

const notificationChannel = keyPrefix + "generation:changed"

var getScript = redis.NewScript(`
local generation = tonumber(redis.call('GET', KEYS[1]) or '0')
local value = redis.call('GET', ARGV[1] .. generation .. ':' .. ARGV[2])
return {generation, value}
`)

var putScript = redis.NewScript(`
local generation = tonumber(redis.call('GET', KEYS[1]) or '0')
if generation ~= tonumber(ARGV[1]) then
  return 0
end
redis.call('SET', ARGV[2] .. generation .. ':' .. ARGV[3], ARGV[4], 'PX', ARGV[5])
return 1
`)

var invalidateScript = redis.NewScript(`
local generation = redis.call('INCR', KEYS[1])
redis.call('PUBLISH', ARGV[1], generation)
return generation
`)

// Verifications caches key verification results.
//
// A nil *Verifications remains usable for isolated tests and defensive call
// sites. Production configuration requires Redis for revocable sessions.
//
// Verifications 缓存 key 的校验结果。
//
// nil 的 *Verifications 仍可用于隔离测试与防御性调用点。生产配置因可吊销会话而要求 Redis。
type Verifications struct {
	client *redis.Client
	ttl    time.Duration
	owned  bool
}

// New returns a cache over addr, or nil when addr is empty.
//
// New 基于 addr 返回一个缓存；addr 为空时返回 nil。
func New(addr, password string, db int, ttl time.Duration) *Verifications {
	if addr == "" {
		return nil
	}
	verification := NewWithClient(redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db}), ttl)
	verification.owned = true
	return verification
}

// NewWithClient returns a cache over a caller-owned Redis client.
//
// NewWithClient 基于调用方持有的 Redis 客户端返回一个缓存。
func NewWithClient(client *redis.Client, ttl time.Duration) *Verifications {
	if client == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Verifications{
		client: client,
		ttl:    ttl,
	}
}

// Ping reports whether the cache is reachable, for a startup check. A cache
// that is configured but unreachable is worth failing startup over: it is
// almost always a typo in the address, and discovering it now beats
// discovering it as unexplained database load later.
//
// Ping 报告缓存是否可达，用于启动检查。一个已配置但不可达的缓存值得让启动失败：那几乎
// 总是地址写错了，而现在发现它，好过之后把它当成莫名其妙的数据库负载去发现。
func (v *Verifications) Ping(ctx context.Context) error {
	if v == nil {
		return nil
	}
	return v.client.Ping(ctx).Err()
}

// Close releases the connection pool.
//
// Close 释放连接池。
func (v *Verifications) Close() error {
	if v == nil || !v.owned {
		return nil
	}
	return v.client.Close()
}

// Get returns a cached verification and the generation observed with it. A
// miss, malformed entry, or Redis failure is reported as not found; a failure
// returns generation -1 so Put cannot repopulate an unobserved generation.
//
// Get 返回一条缓存校验及与它一同观测到的代际。未命中、条目畸形或 Redis 故障都报告为
// 未找到；故障返回 -1 代际，使 Put 无法回填一个从未被观测过的代际。
func (v *Verifications) Get(ctx context.Context, hash string) (logic.Verification, bool, int64) {
	if v == nil {
		return logic.Verification{}, false, -1
	}
	result, err := getScript.Run(ctx, v.client, []string{generationKey}, keyPrefix, hash).Result()
	if err != nil {
		return logic.Verification{}, false, -1
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return logic.Verification{}, false, -1
	}
	generation, ok := values[0].(int64)
	if !ok {
		return logic.Verification{}, false, -1
	}
	if values[1] == nil {
		return logic.Verification{}, false, generation
	}
	raw, ok := values[1].(string)
	if !ok {
		return logic.Verification{}, false, generation
	}
	var out logic.Verification
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return logic.Verification{}, false, generation
	}
	return out, true, generation
}

// Put caches one verification only if generation is still current. This gate
// prevents an in-flight database read from repopulating a generation that a
// concurrent revocation already invalidated.
//
// Put 只在 generation 仍是当前代际时缓存一条校验。这道门防止一次在途数据库读取，
// 在并发吊销已经令旧代际失效后又把它填回来。
func (v *Verifications) Put(ctx context.Context, hash string, verification logic.Verification, generation int64) {
	if v == nil || generation < 0 {
		return
	}
	raw, err := json.Marshal(verification)
	if err != nil {
		return
	}
	_ = putScript.Run(ctx, v.client, []string{generationKey}, generation, keyPrefix, hash, raw, v.ttl.Milliseconds()).Err()
}

// Generation returns the current verification generation. An absent Redis key
// is generation zero, while Redis failures remain visible to notification
// callers so they can fail closed.
//
// Generation 返回当前校验 generation。Redis 键不存在表示第零代；Redis 故障则会
// 如实暴露给通知调用方，使其可以失效关闭。
func (v *Verifications) Generation(ctx context.Context) (int64, error) {
	if v == nil {
		return 0, errors.New("cache: revocation notification unavailable")
	}
	generation, err := v.client.Get(ctx, generationKey).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, errors.New("cache: reading verification generation")
	}
	if generation < 0 {
		return 0, errors.New("cache: negative verification generation")
	}
	return generation, nil
}

// WatchGeneration returns when the generation differs from after or the
// heartbeat wait elapses. It confirms the Redis subscription before reading
// the generation, so changes cannot fall into a read-then-subscribe gap.
//
// WatchGeneration 在 generation 与 after 不同或心跳等待结束时返回。它先确认 Redis
// 订阅再读取 generation，因此变化不会落入“先读后订阅”的空窗。
func (v *Verifications) WatchGeneration(ctx context.Context, after int64, wait time.Duration) (int64, error) {
	if v == nil || after < 0 || wait <= 0 {
		return 0, errors.New("cache: invalid revocation watch")
	}
	subscription := v.client.Subscribe(ctx, notificationChannel)
	defer subscription.Close()
	if _, err := subscription.Receive(ctx); err != nil {
		return 0, err
	}
	generation, err := v.Generation(ctx)
	if err != nil || generation != after {
		return generation, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		if _, err := subscription.ReceiveMessage(waitCtx); err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return v.Generation(ctx)
			}
			return 0, err
		}
		generation, err = v.Generation(ctx)
		if err != nil || generation != after {
			return generation, err
		}
	}
}

// Invalidate advances the global verification generation. Invalidating the
// whole bounded-TTL cache is deliberate: it is constant-time, and prevents a
// concurrent old database read from refilling a revoked hash after deletion.
//
// The TTL alone would be a correctness backstop, not a revocation mechanism:
// revocation is an incident response action, and telling an operator their
// leaked key keeps working for another half minute is not an answer. This is
// why revocation calls into the cache at all.
//
// Invalidate 推进全局校验代际。刻意令整份有界 TTL 缓存失效，是因为这能以常数时间完成，
// 且能防止并发的旧数据库读取在删除之后重新填入已吊销哈希。
//
// 仅靠 TTL 只能算一道正确性兜底，不能算一种吊销机制：吊销是应急响应动作，而告诉运维
// 「你那个已泄漏的 key 还能再用半分钟」不是一个像样的答复。这正是吊销流程要回调缓存
// 的原因。
func (v *Verifications) Invalidate(ctx context.Context, hash string) {
	_ = v.PublishInvalidation(ctx)
}

// InvalidateAll drops every positive verification generation in constant
// time. User disable uses it instead of collecting an unbounded set of hashes.
//
// InvalidateAll 以常数时间丢弃整代正向校验。用户禁用使用它，从而无需收集无界哈希集合。
func (v *Verifications) InvalidateAll(ctx context.Context) {
	_ = v.PublishInvalidation(ctx)
}

// PublishInvalidation advances and publishes the generation as one Redis
// operation, returning any Redis error to a durable outbox relay.
//
// PublishInvalidation 用一次 Redis 操作推进并发布 generation，并把 Redis 错误返回给
// 持久化 outbox 中继。
func (v *Verifications) PublishInvalidation(ctx context.Context) error {
	if v == nil {
		return errors.New("cache: revocation notification unavailable")
	}
	if err := invalidateScript.Run(ctx, v.client, []string{generationKey}, notificationChannel).Err(); err != nil {
		return errors.New("cache: publishing verification invalidation")
	}
	return nil
}

var _ logic.Invalidator = (*Verifications)(nil)
