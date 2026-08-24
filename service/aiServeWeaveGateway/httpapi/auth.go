package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"AIServeWeave/common/quota"
)

// Identity is who a verified API key belongs to. It is attached to the request
// context so later work — usage records, per-tenant quotas, rate limits — has
// the attribution without re-verifying anything.
//
// Identity 是一个已校验 API Key 的归属。它会被附加到请求 context 上，好让后续工作
// ——用量记录、按租户配额、限流——无需重新校验即可拿到归属信息。
type Identity struct {
	TenantID string
	KeyID    string
	// Limits is the tenant's quota, learned from the same verification that
	// established the identity. It travels with the identity rather than being
	// fetched separately so enforcement adds no round trip to the request path.
	//
	// Limits 是租户的配额，与确立身份的那次校验一同得知。它随身份一起传递而不是单独
	// 拉取，好让执行不给请求路径增加任何往返。
	Limits quota.Limits
}

// ErrKeyRejected is what a verifier returns for a key that does not
// authenticate, whatever the reason. A verifier must not distinguish unknown
// from revoked from expired: the caller cannot act differently on any of them,
// and the distinction is exactly what a probe is looking for.
//
// ErrKeyRejected 是校验器对一个无法通过认证的 key 所返回的错误，无论原因为何。校验器
// 不得区分「未知」「已吊销」与「已过期」：调用方对这三者做不出不同的应对，而这个区分
// 恰恰是试探者想要的东西。
var ErrKeyRejected = errors.New("httpapi: api key rejected")

// KeyVerifier resolves a presented API key to the identity it authenticates.
//
// The interface is declared here, where it is used, and implemented in
// controlplaneclient. That is what lets this package be tested without a
// control plane, and lets a deployment run with the static key list instead.
//
// KeyVerifier 把出示的 API Key 解析为它所认证的身份。
//
// 接口声明在使用它的这里，实现在 controlplaneclient。这既让本包无需控制面即可测试，
// 也让某个部署可以改用静态 key 列表运行。
type KeyVerifier interface {
	// Verify returns the identity behind key, or an error wrapping
	// ErrKeyRejected when the key does not authenticate. Any other error is
	// treated as the verifier being unavailable, which is a 503 rather than a
	// 401: refusing a valid key because a dependency is down would be a worse
	// answer than saying the service cannot answer right now.
	//
	// Verify 返回 key 背后的身份；key 无法通过认证时返回包装了 ErrKeyRejected 的
	// 错误。其他任何错误都被视为校验器不可用，对应 503 而不是 401：因为某个依赖挂了
	// 而拒绝一个有效的 key，是比「本服务此刻无法作答」更糟的答复。
	Verify(ctx context.Context, key string) (Identity, error)
}

// identityKey is the context key an authenticated identity is stored under.
// It is an unexported struct type so no other package can write one.
//
// identityKey 是已认证身份在 context 中存放所用的键。它是未导出的结构体类型，因此
// 其他包无法写入。
type identityKey struct{}

// IdentityFrom returns the identity behind the request's API key. The second
// result is false when the request was admitted without authentication, which
// is what an unconfigured local deployment does.
//
// IdentityFrom 返回该请求 API Key 背后的身份。第二个返回值为 false 表示该请求是在
// 未经认证的情况下被放行的，未配置的本地部署就是这种行为。
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}

// authenticator checks the Authorization header. It has three modes, in
// descending order of what a real deployment should use:
//
//   - A verifier, which resolves keys against the control plane. Keys are
//     stored hashed, can be revoked, and carry a tenant.
//   - A static list from -api-keys. Keys are plaintext in a process listing
//     and are rotated by restarting. It exists for local development and for
//     running the data plane before a control plane is deployed.
//   - Neither, which admits everything. Only ever appropriate on a loopback
//     listener, and warned about at startup.
//
// authenticator 校验 Authorization 头。它有三种模式，按真实部署应当采用的优先级排列：
//
//   - 校验器，对着控制面解析 key。key 以哈希存储、可吊销、且携带租户信息。
//   - 来自 -api-keys 的静态列表。key 以明文出现在进程列表里，靠重启来轮换。它的存在
//     是为了本地开发，以及在控制面尚未部署时先把数据面跑起来。
//   - 两者皆无，放行一切。仅在监听回环地址时才谈得上合适，且启动时会有告警。
type authenticator struct {
	verifier KeyVerifier
	keys     map[string]struct{}
	logger   *slog.Logger
}

func newAuthenticator(verifier KeyVerifier, keys []string, logger *slog.Logger) *authenticator {
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			set[k] = struct{}{}
		}
	}
	switch {
	case verifier != nil && len(set) > 0:
		// Both configured is a misconfiguration worth naming rather than
		// silently resolving: an operator who set both believes something
		// about which one wins, and half of them believe wrong.
		//
		// 两者都配置属于配置错误，值得明确指出而不是悄悄消化：同时设置了两者的运维，
		// 心里对「哪个生效」有一个预期，而其中一半人的预期是错的。
		logger.Warn("both a control plane and -api-keys are configured; the control plane wins and -api-keys is ignored")
	case verifier == nil && len(set) == 0:
		logger.Warn("no API keys configured; every request is admitted without authentication")
	}
	return &authenticator{verifier: verifier, keys: set, logger: logger}
}

// middleware rejects a request that does not carry a valid key as a Bearer
// token.
//
// middleware 拒绝未以 Bearer token 形式携带有效 key 的请求。
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.verifier == nil && len(a.keys) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		key, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "missing or invalid API key")
			return
		}

		if a.verifier == nil {
			if !a.staticallyAuthorized(key) {
				writeOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "missing or invalid API key")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		identity, err := a.verifier.Verify(r.Context(), key)
		switch {
		case errors.Is(err, ErrKeyRejected):
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "missing or invalid API key")
			return
		case err != nil:
			// The control plane is unreachable. This is not the caller's
			// fault and their key may well be valid, so it is a 503: a 401
			// here would tell a legitimate caller to go regenerate a key that
			// was never the problem.
			//
			// 控制面不可达。这不是调用方的错，而且他们的 key 很可能是有效的，因此
			// 返回 503：此处返回 401 会让一个合法调用方跑去重新生成一个本来就没问题
			// 的 key。
			a.logger.Error("api key verification is unavailable", slog.Any("error", err))
			writeOpenAIError(w, http.StatusServiceUnavailable, "api_error", "verification_unavailable",
				"cannot verify the API key right now")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
	})
}

// staticallyAuthorized compares against the configured plaintext keys.
//
// staticallyAuthorized 对照已配置的明文 key 进行比较。
func (a *authenticator) staticallyAuthorized(key string) bool {
	// Every configured key is compared, rather than stopping at the first
	// match, so the response time does not leak how many keys exist or
	// where in the set a near-miss landed.
	//
	// 会比较每一个已配置的 key，而不是命中第一个就停下，这样响应时间就不会泄漏总共
	// 有多少个 key，也不会泄漏一次接近的匹配落在集合的什么位置。
	var ok bool
	for candidate := range a.keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			ok = true
		}
	}
	return ok
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", false
	}
	return token, true
}
