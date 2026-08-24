package handler

import (
	"net/http"

	"github.com/zeromicro/go-zero/rest"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// RegisterHandlers mounts every route this service serves.
//
// The three guard groups are the service's whole authorization surface, and
// they are visible in one screen on purpose: an endpoint's protection should
// be readable without following it into its handler.
//
//   - Public: sign-in only.
//   - Session: everything the Console does on behalf of a signed-in user.
//   - Shared secret: tenant bootstrap, and the Gateway's verification call.
//
// RegisterHandlers 挂载本服务提供的每一条路由。
//
// 那三组守卫就是本服务全部的授权面，而且它们被刻意放在一屏之内：一个端点受什么保护，
// 应当无需追进它的 handler 就能读出来。
//
//   - 公开：仅登录。
//   - 会话：Console 代表已登录用户所做的一切。
//   - 共享密钥：租户引导，以及 Gateway 的校验调用。
func RegisterHandlers(server *rest.Server, ctx *svc.ServiceContext) {
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/admin/v1/auth/login", Handler: login(ctx)},
	})

	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/admin/v1/users", Handler: requireSession(ctx, listUsers(ctx))},
		{Method: http.MethodPost, Path: "/admin/v1/users", Handler: requireSession(ctx, createUser(ctx))},
		{Method: http.MethodGet, Path: "/admin/v1/apikeys", Handler: requireSession(ctx, listAPIKeys(ctx))},
		{Method: http.MethodPost, Path: "/admin/v1/apikeys", Handler: requireSession(ctx, createAPIKey(ctx))},
		{Method: http.MethodDelete, Path: "/admin/v1/apikeys/:id", Handler: requireSession(ctx, revokeAPIKey(ctx))},
		{Method: http.MethodGet, Path: "/admin/v1/audit", Handler: requireSession(ctx, listAudit(ctx))},
	})

	server.AddRoutes([]rest.Route{
		{
			Method:  http.MethodPost,
			Path:    "/admin/v1/tenants",
			Handler: requireSharedSecret(ctx.Config.BootstrapToken, createTenant(ctx)),
		},
	})

	server.AddRoutes([]rest.Route{
		{
			Method:  http.MethodPost,
			Path:    "/internal/v1/apikeys/verify",
			Handler: requireSharedSecret(ctx.Config.InternalToken, verifyKey(ctx)),
		},
	})
}
