// Package handler is the Admin API's HTTP layer: routing, authentication
// middleware, and the translation between JSON and the logic layer.
//
// It makes no business decisions. Every permission check lives in logic, so
// the same rule cannot be enforced one way by a handler and another way by a
// future caller that skips the handler.
//
// handler 包是 Admin API 的 HTTP 层：路由、认证中间件，以及 JSON 与 logic 层之间的
// 翻译。
//
// 它不做任何业务决定。所有权限检查都在 logic 中，这样同一条规则就不会被 handler 用
// 一种方式执行、而被将来某个绕过 handler 的调用方用另一种方式执行。
package handler

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// actorKey is the context key the session middleware stores the caller under.
// It is an unexported struct type, not a string: a string key can be written
// by any package that guesses it, and the value under this one decides which
// tenant's data a request may touch.
//
// actorKey 是会话中间件用来存放调用方的 context 键。它是一个未导出的结构体类型，
// 而不是字符串：字符串键可以被任何猜到它的包写入，而这个键之下的值决定了一次请求
// 可以触及哪个租户的数据。
type actorKey struct{}

// withActor returns a context carrying the authenticated caller.
//
// withActor 返回携带已认证调用方的 context。
func withActor(ctx context.Context, actor logic.Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// actorFrom returns the authenticated caller. The second result is false when
// the request did not pass through the session middleware, which is a routing
// mistake rather than an authentication failure — a handler that needs an
// actor must never be reachable without one.
//
// actorFrom 返回已认证的调用方。第二个返回值为 false 表示该请求没有经过会话中间件，
// 那是一个路由错误而不是认证失败——需要 actor 的 handler 绝不应当在没有 actor 的情况下
// 可达。
func actorFrom(ctx context.Context) (logic.Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(logic.Actor)
	return actor, ok
}

// requireSession verifies the caller's session token and attaches the actor it
// asserts.
//
// requireSession 校验调用方的会话令牌，并附上它所主张的 actor。
func requireSession(ctx *svc.ServiceContext, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := ctx.Issuer.Parse(presented)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		actor := logic.Actor{
			UserID:   claims.UserID,
			TenantID: claims.TenantID,
			Role:     claims.Role,
			IP:       clientIP(r),
		}
		next(w, r.WithContext(withActor(r.Context(), actor)))
	}
}

// requireSharedSecret guards the two endpoints that have no signed-in user
// behind them: tenant creation and the Gateway's verification call.
//
// The comparison is constant time. These secrets are compared on every call
// from the Gateway, which is the one place in this service an attacker gets
// unlimited, low-noise attempts at a byte-by-byte timing measurement.
//
// requireSharedSecret 守卫那两个背后没有已登录用户的端点：创建租户，以及 Gateway 的
// 校验调用。
//
// 比较是常数时间的。这些密钥会在 Gateway 的每次调用中被比较，而那正是本服务中唯一
// 一个让攻击者获得无限次、低噪音、逐字节计时测量机会的地方。
func requireSharedSecret(secret string, next http.HandlerFunc) http.HandlerFunc {
	expected := []byte(secret)
	return func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || subtle.ConstantTimeCompare([]byte(presented), expected) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// bearerToken extracts a Bearer credential from an Authorization header.
//
// bearerToken 从 Authorization 头中提取 Bearer 凭据。
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != ""
}

// clientIP returns the address to record in the audit trail.
//
// It reads RemoteAddr only, and deliberately ignores X-Forwarded-For: that
// header is caller-supplied and forgeable unless a trusted proxy is known to
// rewrite it, and an audit trail that records an attacker's chosen address is
// worse than one that records the proxy's. Honoring the header is a change to
// make together with a configured list of trusted proxies, not before.
//
// clientIP 返回要记入审计线索的地址。
//
// 它只读 RemoteAddr，并刻意忽略 X-Forwarded-For：除非确知有可信代理在改写它，否则
// 那个头是调用方提供且可伪造的，而一份记录了攻击者自选地址的审计线索，比一份记录了
// 代理地址的更糟。要采信该头，应当与「配置一份可信代理清单」一并改动，而不是在那之前。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
