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

// Verifications caches key verification results.
//
// A nil *Verifications is usable and does nothing, which is what a deployment
// with no Redis configured gets: the call sites stay unconditional, and the
// service degrades to querying PostgreSQL rather than to a nil dereference.
//
// Verifications 缓存 key 的校验结果。
//
// nil 的 *Verifications 是可用的，且什么都不做，这正是未配置 Redis 的部署所得到的行为：
// 调用点无需分支判断，服务退化为查询 PostgreSQL，而不是退化成一次空指针解引用。
type Verifications struct {
	client *redis.Client
	ttl    time.Duration
}

// New returns a cache over addr, or nil when addr is empty.
//
// New 基于 addr 返回一个缓存；addr 为空时返回 nil。
func New(addr, password string, db int, ttl time.Duration) *Verifications {
	if addr == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Verifications{
		client: redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db}),
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
	if v == nil {
		return nil
	}
	return v.client.Close()
}

// Get returns a cached verification. A miss, a malformed entry and a Redis
// failure are all reported the same way — not found — because the caller's
// response to each is identical: ask PostgreSQL.
//
// Get 返回一条缓存的校验结果。未命中、条目格式错误与 Redis 故障都以同一种方式上报
// ——未找到——因为调用方对这三者的应对完全相同：去问 PostgreSQL。
func (v *Verifications) Get(ctx context.Context, hash string) (logic.Verification, bool) {
	if v == nil {
		return logic.Verification{}, false
	}
	raw, err := v.client.Get(ctx, keyPrefix+hash).Bytes()
	if err != nil {
		return logic.Verification{}, false
	}
	var out logic.Verification
	if err := json.Unmarshal(raw, &out); err != nil {
		return logic.Verification{}, false
	}
	return out, true
}

// Put caches one verification. A failure is dropped: the request it belongs to
// has already been answered correctly from PostgreSQL, and failing it now
// because a cache write did not land would turn a performance feature into an
// availability risk.
//
// Put 缓存一条校验结果。失败会被丢弃：它所属的那次请求已经由 PostgreSQL 正确作答，
// 此刻因为一次缓存写入没落地而让它失败，等于把一个性能特性变成一个可用性风险。
func (v *Verifications) Put(ctx context.Context, hash string, verification logic.Verification) {
	if v == nil {
		return
	}
	raw, err := json.Marshal(verification)
	if err != nil {
		return
	}
	_ = v.client.Set(ctx, keyPrefix+hash, raw, v.ttl).Err()
}

// Invalidate drops one cached verification, so a revoked key stops working
// now rather than when its TTL runs out.
//
// The TTL alone would be a correctness backstop, not a revocation mechanism:
// revocation is an incident response action, and telling an operator their
// leaked key keeps working for another half minute is not an answer. This is
// why revocation calls into the cache at all.
//
// Invalidate 丢弃一条已缓存的校验结果，好让被吊销的 key 立刻停止工作，而不是等到它的
// TTL 走完。
//
// 仅靠 TTL 只能算一道正确性兜底，不能算一种吊销机制：吊销是应急响应动作，而告诉运维
// 「你那个已泄漏的 key 还能再用半分钟」不是一个像样的答复。这正是吊销流程要回调缓存
// 的原因。
func (v *Verifications) Invalidate(ctx context.Context, hash string) {
	if v == nil {
		return
	}
	_ = v.client.Del(ctx, keyPrefix+hash).Err()
}

var _ logic.Invalidator = (*Verifications)(nil)
