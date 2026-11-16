package controlplaneclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// SessionCacheTTL bounds how long a verified Studio session is trusted
// without asking again. It is much shorter than an API key's TTL because a
// logout or session revocation does not advance the revocation generation
// this cache is otherwise invalidated by: a signed-out token stays usable on
// this replica for at most this long.
//
// SessionCacheTTL 限定一次已校验的 Studio 会话在不再询问的情况下被信任多久。它远短于
// API Key 的 TTL，因为登出或会话吊销不会推进本缓存据以失效的吊销 generation：已登出的
// 令牌在本副本上至多在这段时间内仍可使用。
const SessionCacheTTL = 5 * time.Second

// sessionCachePrefix keeps session entries apart from key hashes in the one
// shared cache, which also lets the revocation watch clear both together.
//
// sessionCachePrefix 让会话条目在同一个共享缓存里与 key 哈希分开，也让吊销监听能一并
// 清空两者。
const sessionCachePrefix = "session:"

var _ httpapi.SessionVerifier = (*Verifier)(nil)

// VerifySession resolves a Console session token to the tenant user it
// authenticates. The token itself is sent to the control plane, which alone
// holds the secret that verifies it; the cache is keyed by its SHA-256 so the
// token is not kept in this process longer than the request.
//
// VerifySession 把 Console 会话令牌解析为它所认证的租户用户。令牌本身被发往控制面，
// 只有控制面持有校验它的密钥；缓存以它的 SHA-256 为键，令牌不会在本进程中保留得比
// 请求更久。
func (v *Verifier) VerifySession(ctx context.Context, token string) (httpapi.Identity, error) {
	sum := sha256.Sum256([]byte(token))
	key := sessionCachePrefix + hex.EncodeToString(sum[:])
	if identity, ok, epoch := v.cached(key); ok {
		return identity, nil
	} else {
		identity, err := v.askAt(ctx, "/internal/v1/sessions/verify", map[string]string{"token": token}, httpapi.ErrSessionRejected)
		if err != nil {
			return httpapi.Identity{}, err
		}
		ttl := min(v.ttl, SessionCacheTTL)
		v.storeFor(key, identity, epoch, ttl)
		return identity, nil
	}
}
