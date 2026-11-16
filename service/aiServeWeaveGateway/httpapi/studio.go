package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/common/apikey"
)

// studioPrefix is the path prefix of the Studio API. Every path under it is
// served by the Studio front door, whatever the method or the rest of the
// path, so a session token never reaches the API-key front door and an API
// key never reaches this one.
//
// studioPrefix 是 Studio API 的路径前缀。其下的每个路径，无论方法与其余部分为何，
// 都由 Studio 前门处理，因此会话令牌永远到不了 API Key 前门，API Key 也永远到不了
// 这里。
const studioPrefix = "/studio/"

// ErrSessionRejected is what a SessionVerifier returns for a session token
// that does not authenticate, whatever the reason: unknown, expired, revoked,
// a platform operator's, or a suspended tenant's. Like ErrKeyRejected it does
// not say which.
//
// ErrSessionRejected 是 SessionVerifier 对无法通过认证的会话令牌返回的错误，无论
// 原因是未知、过期、已吊销、属于平台运维，还是租户已暂停。与 ErrKeyRejected 一样，
// 它不说明是哪一种。
var ErrSessionRejected = errors.New("httpapi: session rejected")

// SessionVerifier resolves a Console session token to the tenant user it
// authenticates. It is implemented in controlplaneclient, as KeyVerifier is.
// Any error other than one wrapping ErrSessionRejected means the verifier is
// unavailable, which is a 503: a user is not signed out because a dependency
// blinked.
//
// SessionVerifier 把 Console 会话令牌解析为它所认证的租户用户。与 KeyVerifier
// 一样实现在 controlplaneclient。除包装了 ErrSessionRejected 的错误之外，任何错误
// 都表示校验器不可用，对应 503：不能因为某个依赖眨了一下眼就把用户登出。
type SessionVerifier interface {
	VerifySession(ctx context.Context, token string) (Identity, error)
}

// withStudio routes every Studio path to studio and everything else to front.
//
// withStudio 把每个 Studio 路径交给 studio，其余交给 front。
func withStudio(front, studio http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, studioPrefix) {
			studio.ServeHTTP(w, r)
			return
		}
		front.ServeHTTP(w, r)
	})
}

// studioHandler assembles the Studio front door: session authentication, the
// tenant's quota, then the Studio routes. It shares the scheduler, the model
// and node policies, and the limiter with the API-key front door, so a tenant
// consumes nothing here it could not consume with a key.
//
// studioHandler 组装 Studio 前门：会话鉴权、租户配额，然后是 Studio 路由。它与 API
// Key 前门共用调度器、模型与节点授权和限流器，因此租户在这里能消费的，不会多于用一把
// key 能消费的。
func (h *handlers) studioHandler(sessions SessionVerifier, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /studio/v1/me", h.studioMe)
	mux.HandleFunc("GET /studio/v1/models", h.studioModels)
	mux.HandleFunc("/studio/", func(w http.ResponseWriter, _ *http.Request) {
		writeStudioError(w, http.StatusNotFound, "not_found", "no such Studio endpoint")
	})
	return h.observe(withLogging(logger, studioAuth(sessions, logger, h.rateLimit(mux))))
}

// studioAuth requires a live Console session of a tenant user. A credential
// shaped like an API key is refused here without a round trip: it must not
// be sent to the control plane's session endpoint, where it would leave this
// Gateway in plaintext.
//
// studioAuth 要求一个租户用户的有效 Console 会话。形如 API Key 的凭据在这里直接
// 拒绝、不做往返：它不得被发往控制面的会话端点，否则会以明文离开本 Gateway。
func studioAuth(sessions SessionVerifier, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r.Header.Get("Authorization"))
		if !ok {
			writeStudioError(w, http.StatusUnauthorized, "unauthorized", "a signed-in session is required")
			return
		}
		if _, err := apikey.HashOf(token); err == nil {
			writeStudioError(w, http.StatusUnauthorized, "unauthorized", "a signed-in session is required")
			return
		}
		identity, err := sessions.VerifySession(r.Context(), token)
		switch {
		case errors.Is(err, ErrSessionRejected):
			writeStudioError(w, http.StatusUnauthorized, "unauthorized", "a signed-in session is required")
			return
		case err != nil:
			logger.Error("studio session verification is unavailable", slog.Any("error", err))
			writeStudioError(w, http.StatusServiceUnavailable, "verification_unavailable", "cannot verify the session right now")
			return
		}
		if identity.BillingBlocked && startsBillableWork(r) {
			writeStudioError(w, http.StatusPaymentRequired, "insufficient_balance", "the account balance is exhausted")
			return
		}
		if identity.Accounts != nil && identity.Accounts.Only && !identity.Accounts.Active(time.Now()) && startsBillableWork(r) {
			writeStudioError(w, http.StatusPaymentRequired, "account_subscription_expired", "renew an account subscription to continue")
			return
		}
		ctx := context.WithValue(r.Context(), identityKey{}, identity)
		ctx = withSchedulingAccess(ctx, identity.TenantID, identity.Accounts, identity.Models, identity.Nodes)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearer extracts a Bearer credential.
//
// bearer 提取 Bearer 凭据。
func bearer(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

type studioMeJSON struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
}

// studioMe implements GET /studio/v1/me: who the session belongs to.
//
// studioMe 实现 GET /studio/v1/me：会话属于谁。
func (h *handlers) studioMe(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	writeStudioJSON(w, http.StatusOK, studioMeJSON{TenantID: identity.TenantID, UserID: identity.UserID, Role: identity.Role})
}

type studioModelJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type studioModelsJSON struct {
	Data []studioModelJSON `json:"data"`
}

// studioModels implements GET /studio/v1/models: the models this user may
// chat with, in catalog order. It is the same list GET /v1/models gives a key
// of the same tenant, because both read Scheduler.Models under the same model
// and node policies.
//
// studioModels 实现 GET /studio/v1/models：该用户可对话的模型，按目录顺序。它与同一
// 租户的 key 从 GET /v1/models 得到的列表相同，因为两者都在相同的模型与节点授权下
// 读取 Scheduler.Models。
func (h *handlers) studioModels(w http.ResponseWriter, r *http.Request) {
	models := h.sched.Models(r.Context())
	out := studioModelsJSON{Data: make([]studioModelJSON, len(models))}
	for i, m := range models {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out.Data[i] = studioModelJSON{ID: m.ID, Name: name}
	}
	writeStudioJSON(w, http.StatusOK, out)
}

type studioErrorJSON struct {
	Error studioErrorBody `json:"error"`
}

type studioErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeStudioError writes the Studio API's error shape. code is a stable
// machine-readable value Studio maps to its own wording; message is for
// people reading raw responses and never carries request content.
//
// writeStudioError 写出 Studio API 的错误形状。code 是稳定的机读值，由 Studio 映射成
// 自己的文案；message 供阅读原始响应的人使用，绝不携带请求内容。
func writeStudioError(w http.ResponseWriter, status int, code, message string) {
	writeStudioJSON(w, status, studioErrorJSON{Error: studioErrorBody{Code: code, Message: message}})
}

func writeStudioJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
