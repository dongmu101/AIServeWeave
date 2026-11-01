// Package redeemgate bounds how many install-code redemptions and CA reads
// the control plane admits per minute, across every replica.
//
// Like signupgate the bound is deployment-wide, not per caller: the only
// caller of these endpoints is the Gateway, which sees the public internet
// behind a proxy this service cannot trust to name the real client.
//
// redeemgate 包限定控制面每分钟接受多少次安装码兑换与 CA 读取，跨所有副本计数。
//
// 与 signupgate 一样，上限是全部署范围的而不是按调用方：这些端点唯一的调用方是
// Gateway，而它面对的公网请求位于一个本服务无法信任其标明真实客户端的代理之后。
package redeemgate

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
const Window = time.Minute

// keyPrefix namespaces the window counters in the shared Redis.
//
// keyPrefix 为共享 Redis 中的窗口计数器划出命名空间。
const keyPrefix = "aisw:agent-install:window:"

// Gate decides whether one more redemption may proceed.
//
// Gate 决定是否还允许再进行一次兑换。
type Gate interface {
	// Admit counts one attempt and reports whether it is within the bound.
	// Every attempt counts, successful or not.
	//
	// Admit 记一次尝试，并报告它是否在上限之内。每次尝试都计数，无论成败。
	Admit(ctx context.Context) (bool, error)
}

// Redis is a Gate backed by a fixed-window counter in Redis.
//
// Redis 是以 Redis 固定窗口计数器为后端的 Gate。
type Redis struct {
	client redis.Cmdable
	clock  runtime.Clock
	limit  int64
}

// NewRedis returns a Gate admitting at most limit attempts per Window.
//
// NewRedis 返回一个每个 Window 至多放行 limit 次尝试的 Gate。
func NewRedis(client redis.Cmdable, clock runtime.Clock, limit int) *Redis {
	return &Redis{client: client, clock: clock, limit: int64(limit)}
}

// Admit implements Gate. The counter and its expiry are set in one
// transaction, and the window index is part of the key, so the expiry only
// reclaims space and never decides when a window ends.
//
// Admit 实现 Gate。计数与过期时间在同一个事务里设置，窗口序号是 key 的一部分，因此过期
// 只回收空间，从不决定窗口何时结束。
func (g *Redis) Admit(ctx context.Context) (bool, error) {
	window := g.clock.Now().Unix() / int64(Window/time.Second)
	key := keyPrefix + strconv.FormatInt(window, 10)

	var incr *redis.IntCmd
	_, err := g.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		incr = pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, 2*Window)
		return nil
	})
	if err != nil {
		return false, err
	}
	return incr.Val() <= g.limit, nil
}
