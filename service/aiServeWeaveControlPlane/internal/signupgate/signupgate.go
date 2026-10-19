// Package signupgate bounds how many self-service sign-ups the control plane
// admits per hour, across every replica.
//
// The bound is deployment-wide rather than per caller on purpose. The only
// address this service can trust is the TCP peer (see the handler package's
// clientIP), and behind the Console every sign-up arrives from the Console's
// own address — a per-address limit would be the same global limit wearing a
// misleading name. A per-person limit needs a trusted-proxy list first.
//
// signupgate 包限定控制面每小时接受多少次自助注册，跨所有副本计数。
//
// 这个上限刻意是全部署范围的，而不是按调用方。本服务唯一能信任的地址是 TCP 对端
// （见 handler 包的 clientIP），而在 Console 之后，每一次注册都来自 Console 自己的
// 地址——按地址限流只会是同一个全局上限，挂着一个误导人的名字。要按人限流，得先有
// 一份可信代理清单。
package signupgate

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/common/runtime"
)

// Window is the length of one counting window.
//
// Window 是一个计数窗口的长度。
const Window = time.Hour

// keyPrefix namespaces the window counters in the shared Redis.
//
// keyPrefix 为共享 Redis 中的窗口计数器划出命名空间。
const keyPrefix = "aisw:signup:window:"

// Gate decides whether one more sign-up may proceed.
//
// Gate 决定是否还允许再进行一次注册。
type Gate interface {
	// Admit counts one attempt and reports whether it is within the bound.
	// An attempt is counted whether or not the sign-up later succeeds, so
	// probing for registered addresses spends the same budget as signing up.
	//
	// Admit 记一次尝试，并报告它是否在上限之内。无论随后注册是否成功都会计数，
	// 因此探测已注册地址与真正注册消耗的是同一份额度。
	Admit(ctx context.Context) (bool, error)
}

// Redis is a Gate backed by a fixed-window counter in Redis, shared by every
// replica that points at the same Redis.
//
// Redis 是一个以 Redis 固定窗口计数器为后端的 Gate，指向同一 Redis 的所有副本
// 共享它。
type Redis struct {
	client redis.Cmdable
	clock  runtime.Clock
	limit  int64
}

// NewRedis returns a Gate admitting at most limit sign-ups per Window.
// limit must be positive; a deployment that wants no bound builds no Gate.
//
// NewRedis 返回一个每个 Window 至多放行 limit 次注册的 Gate。limit 必须为正；
// 不想设上限的部署干脆不构造 Gate。
func NewRedis(client redis.Cmdable, clock runtime.Clock, limit int) *Redis {
	return &Redis{client: client, clock: clock, limit: int64(limit)}
}

// Admit implements Gate. The counter and its expiry are set in one
// transaction, so a crash cannot leave a counter that never expires. The
// window index is part of the key, so the expiry only reclaims space; it
// never decides when a window ends.
//
// Admit 实现 Gate。计数与过期时间在同一个事务里设置，因此崩溃不会留下一个永不过期
// 的计数器。窗口序号是 key 的一部分，过期只负责回收空间，从不决定窗口何时结束。
func (g *Redis) Admit(ctx context.Context) (bool, error) {
	window := g.clock.Now().Unix() / int64(Window/time.Second)
	key := keyPrefix + strconv.FormatInt(window, 10)

	var incr *redis.IntCmd
	_, err := g.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		incr = pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, Window)
		return nil
	})
	if err != nil {
		return false, err
	}
	return incr.Val() <= g.limit, nil
}
