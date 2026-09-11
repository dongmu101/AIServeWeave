// Package reqid is the one place a request-correlation id is minted and
// carried on a context.Context, shared by Gateway's httpapi (which mints it)
// and tunnelserver (which must read the same value httpapi already
// generated, not mint a second, disconnected one) — see the P08 design doc
// for why two independently-minted ids made request_id useless for
// cross-service correlation before this package existed.
//
// reqid 包是请求关联 id 被铸造、并挂在 context.Context 上传递的唯一地方，由
// Gateway 的 httpapi(铸造方)与 tunnelserver(必须读到 httpapi 已经生成的同一个
// 值，而不是再铸造一个互不相干的第二个)共用——两个各自独立铸造的 id 为什么会让
// request_id 在跨服务关联上形同虚设，见 P08 设计文档。
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type contextKey struct{}

// New returns a fresh 16-byte hex-encoded id. A broken OS entropy source is
// not a condition worth crashing a request over, so it falls back to a fixed
// sentinel instead of panicking — degraded correlation for one request beats
// a downed handler.
//
// New 返回一个新的 16 字节十六进制 id。操作系统熵源损坏不值得让一次请求崩溃，
// 因此退化为一个固定哨兵值而不是 panic——一次请求关联能力下降，好过 handler 被
// 打挂。
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// WithValue attaches id to ctx.
//
// WithValue 把 id 挂到 ctx 上。
func WithValue(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext reads back the id attached by WithValue, or "" if none.
//
// FromContext 读回由 WithValue 挂上的 id，未挂载时返回空串。
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
