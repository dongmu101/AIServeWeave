// Package revocationoutbox relays durable API-key revocation generations to
// the verification cache.
//
// revocationoutbox 包把持久化的 API Key 吊销代际中继给校验缓存。
package revocationoutbox

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

const (
	retryInterval  = time.Second
	attemptTimeout = 5 * time.Second
)

// Store atomically publishes and confirms one pending revocation generation.
//
// Store 原子地发布并确认一个待发送的吊销代际。
type Store interface {
	FlushRevocations(ctx context.Context, publish func(context.Context) error) (bool, error)
}

// Publisher advances the verification cache generation.
//
// Publisher 推进校验缓存的代际。
type Publisher interface {
	PublishInvalidation(ctx context.Context) error
}

// Relay synchronously attempts revocation delivery after mutations and retries
// durable pending work in the background.
//
// Relay 在变更后同步尝试发送吊销通知，并在后台重试持久化的待发送工作。
type Relay struct {
	store     Store
	publisher Publisher
	clock     runtime.Clock
}

// New returns a Relay over the durable store and cache publisher.
//
// New 基于持久化存储与缓存发布者返回一个 Relay。
func New(store Store, publisher Publisher, clock runtime.Clock) *Relay {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &Relay{store: store, publisher: publisher, clock: clock}
}

// Run immediately checks for recovered work, then retries once per second
// until ctx is cancelled.
//
// Run 立即检查恢复出的待发送工作，之后每秒重试一次，直到 ctx 被取消。
func (r *Relay) Run(ctx context.Context) {
	for {
		r.flush(ctx)
		timer, stop := r.clock.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-timer:
			stop()
		}
	}
}

// Invalidate attempts to deliver the durable revocation generation. keyHash
// is deliberately ignored because the outbox invalidates the whole cache
// generation and never retains credential material.
//
// Invalidate 尝试发送持久化的吊销代际。keyHash 被刻意忽略，因为 outbox 会令整代缓存
// 失效，且绝不保留凭据材料。
func (r *Relay) Invalidate(ctx context.Context, keyHash string) {
	_ = keyHash
	r.flush(ctx)
}

// InvalidateAll attempts to deliver the durable revocation generation.
//
// InvalidateAll 尝试发送持久化的吊销代际。
func (r *Relay) InvalidateAll(ctx context.Context) {
	r.flush(ctx)
}

func (r *Relay) flush(ctx context.Context) {
	if r == nil || r.store == nil || r.publisher == nil {
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	if _, err := r.store.FlushRevocations(attemptCtx, r.publisher.PublishInvalidation); err != nil {
		// Keep logs free of database, Redis, and credential details. Operators
		// only need the fixed category here; the durable row carries the retry.
		//
		// 日志不携带数据库、Redis 或凭据细节。这里运维只需要固定分类；持久化行会承接重试。
		slog.Error("revocation outbox flush failed")
	}
}
