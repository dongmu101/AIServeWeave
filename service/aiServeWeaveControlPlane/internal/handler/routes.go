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
//   - Shared secret: tenant bootstrap, the Gateway's verification call, and
//     the operator fleet inventory — which is mounted only when configured.
//
// RegisterHandlers 挂载本服务提供的每一条路由。
//
// 那三组守卫就是本服务全部的授权面，而且它们被刻意放在一屏之内：一个端点受什么保护，
// 应当无需追进它的 handler 就能读出来。
//
//   - 公开：仅登录。
//   - 会话：Console 代表已登录用户所做的一切。
//   - 共享密钥：租户引导、Gateway 的校验调用，以及运维机群清单——后者只在被配置时挂载。
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
		{Method: http.MethodGet, Path: "/admin/v1/tenants/current", Handler: requireSession(ctx, currentTenant(ctx))},
		{Method: http.MethodPut, Path: "/admin/v1/tenants/limits", Handler: requireSession(ctx, setTenantLimits(ctx))},
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

	// The Job persistence endpoints (STATUS.md's J04) share the same
	// InternalToken as key verification above: both are the Gateway talking
	// to this service about its own callers' business, not a person acting
	// in a tenant's session, and both already accept the same known cost of
	// a shared secret over mTLS — see the service README's 已知缺口.
	//
	// Job 持久化端点（STATUS.md 的 J04）与上面的 key 校验共用同一个
	// InternalToken：两者都是 Gateway 就自己调用方的业务在与本服务对话，而不是
	// 某个人在租户会话里的操作，且两者已经接受了共享密钥而非 mTLS 这个同样已知
	// 的代价——见服务 README 的「已知缺口」。
	server.AddRoutes([]rest.Route{
		{
			Method:  http.MethodPost,
			Path:    "/internal/v1/jobs",
			Handler: requireSharedSecret(ctx.Config.InternalToken, createJob(ctx)),
		},
		// /jobs/active is registered ahead of /jobs/:id and relies on
		// go-zero's router preferring a literal path segment over a param
		// one at the same depth — e2e/jobs_e2e_test.go's
		// TestListActiveJobsForRouteRoutesAheadOfTheParameterizedGetJobRoute
		// pins that behavior so a router upgrade that changed it would be
		// caught here rather than by a Gateway recovery sweep silently
		// hitting the wrong handler.
		//
		// /jobs/active 排在 /jobs/:id 之前，依赖 go-zero 的路由器在同一深度上
		// 优先选择字面路径段而不是参数段这条行为。e2e/jobs_e2e_test.go 的
		// TestListActiveJobsForRouteRoutesAheadOfTheParameterizedGetJobRoute
		// 把这个行为钉住，好让路由器升级一旦改变了它，能在这里被发现，而不是
		// 让 Gateway 的恢复扫描默默命中错误的 handler。
		{
			Method:  http.MethodGet,
			Path:    "/internal/v1/jobs/active",
			Handler: requireSharedSecret(ctx.Config.InternalToken, listActiveJobsForRoute(ctx)),
		},
		{
			Method:  http.MethodGet,
			Path:    "/internal/v1/jobs/:id",
			Handler: requireSharedSecret(ctx.Config.InternalToken, getJob(ctx)),
		},
		{
			Method:  http.MethodPatch,
			Path:    "/internal/v1/jobs/:id/state",
			Handler: requireSharedSecret(ctx.Config.InternalToken, updateJobState(ctx)),
		},
		{
			Method:  http.MethodPost,
			Path:    "/internal/v1/jobs/:id/artifacts",
			Handler: requireSharedSecret(ctx.Config.InternalToken, createJobArtifact(ctx)),
		},
		{
			Method:  http.MethodGet,
			Path:    "/internal/v1/jobs/:id/artifacts",
			Handler: requireSharedSecret(ctx.Config.InternalToken, listJobArtifacts(ctx)),
		},
	})

	// The fleet inventory is mounted only when it is configured, so a
	// deployment without an operations console has no such route rather than
	// a route that answers "not configured". It is under its own prefix and
	// its own secret because a node has no tenant: there is nothing on it for
	// a session to be scoped by, and putting it in the session group would
	// mean every tenant's admin could read the whole fleet.
	//
	// 机群清单只在被配置时才挂载，因此没有运维控制台的部署是根本没有这条路由，而不是
	// 有一条回答「未配置」的路由。它使用自己的路径前缀与自己的密钥，因为节点没有租户：
	// 它身上没有任何东西可供会话限定范围，而把它放进会话组，就意味着每个租户的管理员
	// 都能读到整个机群。
	if ctx.Fleet != nil {
		server.AddRoutes([]rest.Route{
			{
				Method:  http.MethodGet,
				Path:    "/operator/v1/nodes",
				Handler: requireSharedSecret(ctx.Config.Fleet.OperatorToken, listFleetNodes(ctx)),
			},
			{
				Method:  http.MethodGet,
				Path:    "/operator/v1/models",
				Handler: requireSharedSecret(ctx.Config.Fleet.OperatorToken, listFleetModels(ctx)),
			},
			{
				Method:  http.MethodGet,
				Path:    "/operator/v1/workflows",
				Handler: requireSharedSecret(ctx.Config.Fleet.OperatorToken, listOperatorWorkflows(ctx)),
			},
		})

		// The workflow menu and a tenant's own runs are tenant questions, so
		// they are session-guarded like the rest of the Admin API. They are
		// mounted here rather than in the session group above only because
		// they need a configured Gateway read path — without one this service
		// cannot see a template or a job at all, and a route that answered
		// "not configured" would be worse than no route.
		//
		// 工作流菜单与租户自己的运行是租户的问题，因此与 Admin API 的其余部分一样由
		// 会话守卫。它们挂在这里而不是上面的会话组，只是因为它们需要一条已配置的
		// Gateway 读取路径——没有它，本服务根本看不到任何模板或 job，而一条回答
		// 「未配置」的路由比没有路由更糟。
		server.AddRoutes([]rest.Route{
			{
				Method:  http.MethodGet,
				Path:    "/admin/v1/workflows",
				Handler: requireSession(ctx, listWorkflows(ctx)),
			},
			{
				Method:  http.MethodGet,
				Path:    "/admin/v1/jobs",
				Handler: requireSession(ctx, listJobs(ctx)),
			},
		})
	}
}
