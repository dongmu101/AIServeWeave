package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/token"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"

	"github.com/zeromicro/go-zero/rest/pathvar"
)

// maxBodyBytes bounds a request body. These are small JSON documents; anything
// larger is a mistake or an attempt to make this service allocate.
//
// maxBodyBytes 限制请求体大小。这些都是很小的 JSON 文档；更大的要么是失误，要么是想
// 让本服务大量分配内存的尝试。
const maxBodyBytes = 64 << 10

// -----------------------------------------------------------------------
// Sessions
// -----------------------------------------------------------------------

// login authenticates a Console user and issues a session token.
//
// login 认证一个 Console 用户并签发会话令牌。
func login(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.LoginRequest
		if !decode(w, r, &req) {
			return
		}

		user, err := ctx.Logic.Authenticate(r.Context(), req.Email, req.Password, clientIP(r))
		if err != nil {
			respondErr(w, err)
			return
		}
		signed, expiry, err := ctx.Issuer.Issue(sessionClaims(user))
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.LoginResponse{
			Token:     signed,
			ExpiresAt: expiry,
			User:      renderUser(user),
		})
	}
}

// -----------------------------------------------------------------------
// Tenants
// -----------------------------------------------------------------------

// createTenant bootstraps a tenant and its owner. It is guarded by the
// bootstrap token, not a session: there is no user to sign in as yet.
//
// createTenant 引导创建一个租户及其 owner。它由 bootstrap token 守卫而不是会话：
// 此时还没有可供登录的用户。
func createTenant(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateTenantRequest
		if !decode(w, r, &req) {
			return
		}

		tenant, owner, err := ctx.Logic.CreateTenant(r.Context(), req.Name, req.OwnerEmail, req.OwnerPassword, clientIP(r))
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, types.CreateTenantResponse{
			Tenant: types.Tenant{
				ID:        tenant.ID,
				Name:      tenant.Name,
				Status:    tenant.Status,
				CreatedAt: tenant.CreatedAt,
			},
			Owner: renderUser(owner),
		})
	}
}

// -----------------------------------------------------------------------
// Users
// -----------------------------------------------------------------------

// listUsers returns the caller's tenant's users.
//
// listUsers 返回调用方所属租户的用户。
func listUsers(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		users, err := ctx.Logic.ListUsers(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.User, len(users))
		for i, user := range users {
			out[i] = renderUser(user)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// createUser adds a user to the caller's tenant.
//
// createUser 向调用方所属租户添加一个用户。
func createUser(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.CreateUserRequest
		if !decode(w, r, &req) {
			return
		}

		user, err := ctx.Logic.CreateUser(r.Context(), actor, req.Email, req.Password, req.Name, req.Role)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderUser(user))
	}
}

// -----------------------------------------------------------------------
// API keys
// -----------------------------------------------------------------------

// listAPIKeys returns the caller's tenant's keys.
//
// listAPIKeys 返回调用方所属租户的 key。
func listAPIKeys(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		keys, err := ctx.Logic.ListAPIKeys(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.APIKey, len(keys))
		for i, key := range keys {
			out[i] = renderAPIKey(key)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// createAPIKey mints a key and returns its plaintext, once.
//
// createAPIKey 铸造一个 key，并返回它的明文，仅此一次。
func createAPIKey(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.CreateAPIKeyRequest
		if !decode(w, r, &req) {
			return
		}

		created, err := ctx.Logic.CreateAPIKey(r.Context(), actor, req.Name, time.Duration(req.TTLSeconds)*time.Second)
		if err != nil {
			respondErr(w, err)
			return
		}
		// The plaintext leaves the process here and nowhere else. It is not
		// logged on the way out: this response is the one copy the requester
		// will ever see.
		//
		// 明文只在此处离开本进程，别无他处。它在离开时不会被记录日志：这个响应就是
		// 索取者能看到的唯一一份副本。
		writeJSON(w, http.StatusCreated, types.CreateAPIKeyResponse{
			Key:    created.Plaintext,
			APIKey: renderAPIKey(created.Key),
		})
	}
}

// revokeAPIKey revokes one of the caller's tenant's keys.
//
// revokeAPIKey 吊销调用方所属租户的一个 key。
func revokeAPIKey(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		keyID := pathvar.Vars(r)["id"]
		if keyID == "" {
			writeError(w, http.StatusBadRequest, "a key id is required")
			return
		}

		if err := ctx.Logic.RevokeAPIKey(r.Context(), actor, keyID); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// -----------------------------------------------------------------------
// Audit
// -----------------------------------------------------------------------

// listAudit returns the caller's tenant's audit trail.
//
// listAudit 返回调用方所属租户的审计线索。
func listAudit(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		entries, err := ctx.Logic.ListAudit(r.Context(), actor, limit)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.AuditEntry, len(entries))
		for i, entry := range entries {
			out[i] = types.AuditEntry{
				ID:        entry.ID,
				ActorID:   entry.ActorID,
				Action:    entry.Action,
				Target:    entry.Target,
				Detail:    entry.Detail,
				IP:        entry.IP,
				CreatedAt: entry.CreatedAt,
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// -----------------------------------------------------------------------
// Internal: the Gateway's verification
// -----------------------------------------------------------------------

// verifyKey resolves a key hash to a tenant, for the Gateway.
//
// It reads the cache first and writes it on a miss, so the inference request
// path costs a Redis round trip rather than a PostgreSQL one. A verification
// failure returns 404 rather than 401: the Gateway is authenticated here — the
// key it is asking about is not, and conflating the two would make an
// unauthorized Gateway look like an unknown key.
//
// verifyKey 为 Gateway 把一个 key 哈希解析成一个租户。
//
// 它先读缓存、未命中时回写，这样推理请求路径付出的是一次 Redis 往返而不是一次
// PostgreSQL 往返。校验失败返回 404 而不是 401：Gateway 在这里是已认证的——未通过认证
// 的是它所询问的那个 key，把两者混为一谈会让「未授权的 Gateway」看起来像「未知的 key」。
func verifyKey(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.VerifyRequest
		if !decode(w, r, &req) {
			return
		}

		if cached, ok := ctx.Cache.Get(r.Context(), req.Hash); ok {
			writeJSON(w, http.StatusOK, types.VerifyResponse{TenantID: cached.TenantID, KeyID: cached.KeyID, Limits: cached.Limits})
			return
		}

		verification, err := ctx.Logic.VerifyKeyHash(r.Context(), req.Hash)
		if err != nil {
			respondErr(w, err)
			return
		}
		ctx.Cache.Put(r.Context(), req.Hash, verification)
		writeJSON(w, http.StatusOK, types.VerifyResponse{
			TenantID: verification.TenantID,
			KeyID:    verification.KeyID,
			Limits:   verification.Limits,
		})
	}
}

// setTenantLimits handles PUT /admin/v1/tenants/limits: the caller's own
// tenant's quota. It is PUT rather than PATCH because the body is the whole
// set — a partial update would need a way to say "leave this one alone" that
// is distinct from "set it to unlimited", and zero already means unlimited.
//
// setTenantLimits 处理 PUT /admin/v1/tenants/limits：调用方自己所属租户的配额。用 PUT
// 而不是 PATCH，因为请求体就是完整的一组——部分更新需要一种区别于「设为不限制」的方式
// 来表达「这个不动」，而零已经表示不限制了。
func setTenantLimits(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.SetLimitsRequest
		if !decode(w, r, &req) {
			return
		}
		limits, err := ctx.Logic.SetTenantLimits(r.Context(), actor, req.Limits())
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, limits)
	}
}

// -----------------------------------------------------------------------
// Rendering and error mapping
// -----------------------------------------------------------------------

// sessionClaims is what a session token asserts about a user.
//
// sessionClaims 是会话令牌就一个用户所主张的内容。
func sessionClaims(user model.User) token.Claims {
	return token.Claims{UserID: user.ID, TenantID: user.TenantID, Role: user.Role}
}

// renderUser converts a stored user to its wire form. The digest has no field
// to land in, which is the guarantee this function relies on.
//
// renderUser 把存储的用户转换成线上形式。摘要没有可以落脚的字段，这正是本函数所依赖
// 的那条保证。
func renderUser(user model.User) types.User {
	return types.User{
		ID:          user.ID,
		TenantID:    user.TenantID,
		Email:       user.Email,
		Name:        user.Name,
		Role:        user.Role,
		Status:      user.Status,
		LastLoginAt: user.LastLoginAt,
		CreatedAt:   user.CreatedAt,
	}
}

// renderAPIKey converts a stored key to its wire form, without the hash.
//
// renderAPIKey 把存储的 key 转换成线上形式，不含哈希。
func renderAPIKey(key model.APIKey) types.APIKey {
	return types.APIKey{
		ID:         key.ID,
		TenantID:   key.TenantID,
		Name:       key.Name,
		Display:    key.Display,
		Status:     key.Status,
		CreatedBy:  key.CreatedBy,
		ExpiresAt:  key.ExpiresAt,
		LastUsedAt: key.LastUsedAt,
		RevokedAt:  key.RevokedAt,
		CreatedAt:  key.CreatedAt,
	}
}

// decode reads a JSON body, bounded, and reports whether the handler may
// proceed. It writes the error response itself so every call site is one line.
//
// decode 读取一个有大小上限的 JSON 请求体，并报告 handler 是否可以继续。它自己写出
// 错误响应，因此每个调用点只需一行。
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	// An unknown field is refused rather than ignored: a Console sending
	// "expires_at" when the API takes "ttl_seconds" should be told, not
	// silently given a default.
	//
	// 未知字段会被拒绝而不是忽略：当 API 接受的是 "ttl_seconds" 而 Console 发来
	// "expires_at" 时，应当明确告知，而不是悄悄给它一个默认值。
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "the request body is not valid JSON for this endpoint")
		return false
	}
	return true
}

// respondErr maps a logic error onto a status code. An error this function
// does not recognize becomes a 500 with a fixed message: an unrecognized
// error's text may name a table, a DSN or a driver, and none of that belongs
// in a response.
//
// respondErr 把 logic 层的错误映射成状态码。本函数无法识别的错误会变成带固定文案的
// 500：一个无法识别的错误，其文本可能点出表名、DSN 或驱动，而这些都不该出现在响应里。
func respondErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, logic.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid email or password")
	case errors.Is(err, logic.ErrForbidden):
		writeError(w, http.StatusForbidden, "your role does not permit this")
	case errors.Is(err, logic.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, logic.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, logic.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "the request is not valid")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// writeJSON writes one JSON response.
//
// writeJSON 写出一个 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError writes one error response.
//
// writeError 写出一个错误响应。
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, types.ErrorResponse{Error: message})
}
