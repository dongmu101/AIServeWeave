package handler

import (
	"net/http"

	"github.com/zeromicro/go-zero/rest"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// RegisterHandlers mounts every route this service serves.
//
// The guard groups are the service's whole authorization surface, and they
// are visible in one screen on purpose: an endpoint's protection should be
// readable without following it into its handler.
//
//   - Public: sign-in only, tenant and platform operator alike.
//   - Tenant session: everything the Console does on behalf of a signed-in
//     tenant user.
//   - Platform session (STATUS.md's P01): the fleet inventory and node-ops
//     writes, on behalf of a signed-in platform operator — a separate
//     identity from any tenant, guarded by requirePlatformSession, which
//     requireSession itself refuses and vice versa (see model.PlatformScope).
//   - Shared secret: tenant bootstrap, platform operator bootstrap (the same
//     BootstrapToken — see logic.CreatePlatformOperator's doc comment), and
//     the Gateway's verification call.
//
// RegisterHandlers 挂载本服务提供的每一条路由。
//
// 这些守卫组就是本服务全部的授权面，而且它们被刻意放在一屏之内：一个端点受什么保护，
// 应当无需追进它的 handler 就能读出来。
//
//   - 公开：仅登录，租户与平台运维皆然。
//   - 租户会话：Console 代表已登录租户用户所做的一切。
//   - 平台会话（STATUS.md 的 P01）：机群清单与节点操作写入，代表一名已登录的平台
//     运维——与任何租户都不同的一种身份，由 requirePlatformSession 守卫，
//     requireSession 本身会拒绝它，反之亦然（见 model.PlatformScope）。
//   - 共享密钥：租户引导、平台运维账户引导（复用同一个 BootstrapToken——见
//     logic.CreatePlatformOperator 的文档注释），以及 Gateway 的校验调用。
func RegisterHandlers(server *rest.Server, ctx *svc.ServiceContext) {
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/admin/v1/auth/login", Handler: login(ctx)},
		// The platform operator login (STATUS.md's P01) is public the same
		// way the tenant login above is: there is no session yet to guard
		// with. It is a distinct endpoint, not an email-based dispatch on
		// the one above, because the two check against different tables and
		// must never be confused with each other by a client bug.
		//
		// 平台运维登录（STATUS.md 的 P01）与上面的租户登录一样公开：此刻还
		// 没有会话可供守卫。它是一个独立的端点，而不是在上面那一个基础上
		// 按 email 分流，因为两者校验的是不同的表，绝不能因为客户端的一个
		// bug 而被彼此混淆。
		{Method: http.MethodPost, Path: "/admin/v1/platform/auth/login", Handler: platformLogin(ctx)},
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
		{Method: http.MethodGet, Path: "/operator/v1/audit", Handler: requirePlatformSession(ctx, listAudit(ctx))},
		// Job history (STATUS.md's J07) reads the jobs table directly and is
		// mounted here, unconditionally, unlike the Fleet-backed live view
		// further down: it has no dependency on a configured Gateway read
		// path, so a deployment without one still gets to see what it ran.
		//
		// Job 历史（STATUS.md 的 J07）直接读 jobs 表，无条件挂载在这里，
		// 与下面由 Fleet 支撑的实时视图不同：它不依赖任何已配置的 Gateway
		// 读取路径，因此没有配置那条路径的部署，依然能看到自己跑过什么。
		{Method: http.MethodGet, Path: "/admin/v1/jobs/history", Handler: requireSession(ctx, listJobHistory(ctx))},
		{Method: http.MethodGet, Path: "/admin/v1/jobs/history/:id", Handler: requireSession(ctx, getJobHistory(ctx))},
	})

	server.AddRoutes([]rest.Route{
		{
			Method:  http.MethodPost,
			Path:    "/admin/v1/tenants",
			Handler: requireSharedSecret(ctx.Config.BootstrapToken, createTenant(ctx)),
		},
		// Platform operator bootstrap (STATUS.md's P01) reuses the same
		// BootstrapToken as tenant creation — see createPlatformOperator's
		// doc comment for why a second secret meaning the same thing would
		// not add anything.
		//
		// 平台运维账户的引导（STATUS.md 的 P01）复用与租户创建相同的
		// BootstrapToken——为什么再引入一个含义相同的密钥不会带来任何好处，
		// 见 createPlatformOperator 的文档注释。
		{
			Method:  http.MethodPost,
			Path:    "/admin/v1/platform/operators",
			Handler: requireSharedSecret(ctx.Config.BootstrapToken, createPlatformOperator(ctx)),
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
	// a route that answers "not configured". It is under its own prefix
	// because a node has no tenant: there is nothing on it for a session to
	// be scoped by, and putting it in the tenant session group would mean
	// every tenant's admin could read the whole fleet.
	//
	// Its guard is requirePlatformSession, not a shared secret: STATUS.md's
	// P01 introduces the platform-operator identity this endpoint always
	// needed, so "who may read the fleet" is now a question this service can
	// answer and record an actor for, the same way any other session-guarded
	// endpoint can — see the Registry README and Console STATUS's own notes
	// on why the shared-secret version was a known, named gap.
	//
	// 机群清单只在被配置时才挂载，因此没有运维控制台的部署是根本没有这条路由，而不是
	// 有一条回答「未配置」的路由。它使用自己的路径前缀，因为节点没有租户：它身上没有
	// 任何东西可供会话限定范围，而把它放进租户会话组，就意味着每个租户的管理员都能
	// 读到整个机群。
	//
	// 它的守卫是 requirePlatformSession，不再是共享密钥：STATUS.md 的 P01 引入了
	// 本端点一直需要的平台运维身份，因此「谁可以读取机群」现在是本服务能够回答、
	// 也能记下行为人的问题，与其他任何由会话守卫的端点一样——为什么共享密钥版本是
	// 一处已知且已点名的缺口，见 Registry README 与 Console STATUS 自己的说明。
	if ctx.Fleet != nil {
		server.AddRoutes([]rest.Route{
			{
				Method:  http.MethodGet,
				Path:    "/operator/v1/nodes",
				Handler: requirePlatformSession(ctx, listFleetNodes(ctx)),
			},
			{
				Method:  http.MethodGet,
				Path:    "/operator/v1/models",
				Handler: requirePlatformSession(ctx, listFleetModels(ctx)),
			},
			{
				Method:  http.MethodGet,
				Path:    "/operator/v1/workflows",
				Handler: requirePlatformSession(ctx, listOperatorWorkflows(ctx)),
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

	// Node-ops writes (STATUS.md's P01) are mounted independently of Fleet:
	// approving, disabling, enabling or maintaining a node_id is a Registry
	// operation and needs no Gateway read path at all. A deployment can run
	// these without ever configuring the fleet inventory, and does run them
	// alongside it in the ordinary case.
	//
	// 节点操作写入（STATUS.md 的 P01）与 Fleet 分开挂载：批准、禁用、启用或
	// 维护一个 node_id 是对 Registry 的操作，完全不需要任何 Gateway 读取
	// 路径。一个部署可以在从未配置机群清单的情况下运行这些端点，常规情形下
	// 也会与机群清单一同运行。
	if ctx.RegistryClient != nil {
		server.AddRoutes([]rest.Route{
			{
				Method:  http.MethodGet,
				Path:    "/operator/v1/nodes/states",
				Handler: requirePlatformSession(ctx, listNodeStates(ctx)),
			},
			{
				Method:  http.MethodPost,
				Path:    "/operator/v1/nodes/:id/approve",
				Handler: requirePlatformSession(ctx, nodeOpsHandler(ctx.Logic.ApproveNode)),
			},
			{
				Method:  http.MethodPost,
				Path:    "/operator/v1/nodes/:id/disable",
				Handler: requirePlatformSession(ctx, nodeOpsHandler(ctx.Logic.DisableNode)),
			},
			{
				Method:  http.MethodPost,
				Path:    "/operator/v1/nodes/:id/enable",
				Handler: requirePlatformSession(ctx, nodeOpsHandler(ctx.Logic.EnableNode)),
			},
			{
				Method:  http.MethodPost,
				Path:    "/operator/v1/nodes/:id/maintenance",
				Handler: requirePlatformSession(ctx, nodeOpsHandler(ctx.Logic.EnterMaintenance)),
			},
			{
				Method:  http.MethodDelete,
				Path:    "/operator/v1/nodes/:id/maintenance",
				Handler: requirePlatformSession(ctx, nodeOpsHandler(ctx.Logic.ExitMaintenance)),
			},
		})
	}
}
