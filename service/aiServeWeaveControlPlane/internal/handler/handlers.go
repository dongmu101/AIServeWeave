package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
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

// currentTenant returns the caller's own tenant and its quota. The tenant
// comes from the session, so there is nothing in the request to point
// elsewhere.
//
// currentTenant 返回调用方自己所属的租户及其配额。租户来自会话，因此请求里没有任何
// 可以指向别处的东西。
func currentTenant(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		tenant, err := ctx.Logic.CurrentTenant(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.TenantProfileResponse{
			Tenant: types.Tenant{
				ID:        tenant.ID,
				Name:      tenant.Name,
				Status:    tenant.Status,
				CreatedAt: tenant.CreatedAt,
			},
			Limits: tenant.Limits(),
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
		query := r.URL.Query()
		page, err := ctx.Logic.ListUsers(r.Context(), actor, listQuery(query), store.UserFilter{
			Role:  query.Get("role"),
			Query: query.Get("q"),
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.User, len(page.Items))
		for i, user := range page.Items {
			out[i] = renderUser(user)
		}
		writeJSON(w, http.StatusOK, types.UserListResponse{Items: out, NextCursor: page.NextCursor})
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
		query := r.URL.Query()
		page, err := ctx.Logic.ListAPIKeys(r.Context(), actor, listQuery(query), store.APIKeyFilter{
			Status: query.Get("status"),
			Query:  query.Get("q"),
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.APIKey, len(page.Items))
		for i, key := range page.Items {
			out[i] = renderAPIKey(key)
		}
		writeJSON(w, http.StatusOK, types.APIKeyListResponse{Items: out, NextCursor: page.NextCursor})
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
		query := r.URL.Query()
		since, sinceOK := timeParam(query.Get("since"))
		until, untilOK := timeParam(query.Get("until"))
		if !sinceOK || !untilOK {
			writeError(w, http.StatusBadRequest, "since and until must be RFC 3339 timestamps")
			return
		}

		page, err := ctx.Logic.ListAudit(r.Context(), actor, listQuery(query), store.AuditFilter{
			Action:  query.Get("action"),
			ActorID: query.Get("actor_id"),
			Since:   since,
			Until:   until,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.AuditEntry, len(page.Items))
		for i, entry := range page.Items {
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
		writeJSON(w, http.StatusOK, types.AuditListResponse{Items: out, NextCursor: page.NextCursor})
	}
}

// -----------------------------------------------------------------------
// Job history (STATUS.md's J07)
// -----------------------------------------------------------------------

// listJobHistory returns one page of the caller's tenant's persisted job
// history. Unlike listJobs (the Fleet-backed live view further down, mounted
// only when a Gateway read path is configured), this reads the jobs table
// directly and therefore works regardless of Fleet configuration — a
// deployment with no operations console still gets to see what it ran.
//
// listJobHistory 返回调用方所属租户持久化 job 历史中的一页。与下面由 Fleet
// 支撑的实时视图 listJobs（只在配置了 Gateway 读取路径时才挂载）不同，这里
// 直接读 jobs 表，因此与 Fleet 是否配置无关——一个没有运维控制台的部署，依然
// 能看到自己跑过什么。
func listJobHistory(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		query := r.URL.Query()
		since, sinceOK := timeParam(query.Get("since"))
		until, untilOK := timeParam(query.Get("until"))
		if !sinceOK || !untilOK {
			writeError(w, http.StatusBadRequest, "since and until must be RFC 3339 timestamps")
			return
		}
		page, err := ctx.Logic.ListJobs(r.Context(), actor.TenantID, listQuery(query), store.JobFilter{
			State:      query.Get("state"),
			WorkflowID: query.Get("workflow_id"),
			Since:      since,
			Until:      until,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.JobHistoryResponse, len(page.Items))
		for i, job := range page.Items {
			out[i] = renderJobHistory(job)
		}
		writeJSON(w, http.StatusOK, types.JobHistoryListResponse{Items: out, NextCursor: page.NextCursor})
	}
}

// getJobHistory returns one persisted job, scoped to the caller's tenant.
//
// getJobHistory 返回一个持久化 job，限定在调用方所属租户范围内。
func getJobHistory(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		jobID := pathvar.Vars(r)["id"]
		if jobID == "" {
			writeError(w, http.StatusBadRequest, "a job id is required")
			return
		}
		job, err := ctx.Logic.GetJob(r.Context(), actor.TenantID, jobID)
		if err != nil {
			respondErr(w, err)
			return
		}
		artifacts, err := ctx.Logic.ListJobArtifacts(r.Context(), actor.TenantID, jobID)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := renderJobHistory(job)
		out.Artifacts = make([]types.JobArtifactResponse, len(artifacts))
		for i, a := range artifacts {
			out.Artifacts[i] = renderJobArtifact(a)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// renderJobHistory converts a persisted job to the tenant-facing wire form,
// stripping the route binding JobResponse (the internal API's own
// rendering) carries. See types.JobHistoryResponse for why.
//
// renderJobHistory 把一个持久化 job 转换成面向租户的线上形式，剥离
// JobResponse（内部 API 自己的渲染）携带的路由绑定。为什么这样做，见
// types.JobHistoryResponse。
func renderJobHistory(j model.Job) types.JobHistoryResponse {
	return types.JobHistoryResponse{
		JobID:           j.ID,
		WorkflowID:      j.WorkflowID,
		WorkflowVersion: j.WorkflowVersion,
		State:           j.State,
		ErrorSummary:    j.ErrorSummary,
		CreatedAt:       j.CreatedAt,
		UpdatedAt:       j.UpdatedAt,
		TerminalAt:      j.TerminalAt,
	}
}

// listQuery reads the two paging parameters every list endpoint accepts.
//
// A limit that is not a number becomes zero, which the store reads as its
// default. That is deliberate: `?limit=abc` is a caller's mistake in a
// parameter that only bounds a page, and answering it with a default page is
// more useful than refusing the read. A limit above the cap is clamped by the
// store, not here, so one rule governs it.
//
// listQuery 读取每个列表端点都接受的那两个分页参数。
//
// 一个不是数字的 limit 会变成零，而 store 把零读作它的默认值。这是刻意的：`?limit=abc`
// 是调用方在一个只用于限制单页大小的参数上犯的错，用一页默认大小的数据作答，比拒绝这次
// 读取更有用。超过上限的 limit 由 store 截断而不是在这里截断，好让这条规则只有一处。
func listQuery(query url.Values) store.ListQuery {
	limit, _ := strconv.Atoi(query.Get("limit"))
	return store.ListQuery{Limit: limit, Cursor: query.Get("cursor")}
}

// timeParam reads an optional RFC 3339 bound. An absent parameter is the zero
// time, which means that end is unbounded; a malformed one is reported, not
// ignored, because silently dropping a time filter answers a different
// question than the one that was asked.
//
// timeParam 读取一个可选的 RFC 3339 边界。参数缺席即零值时间，表示该端不设边界；格式
// 错误则会被报出而不是被忽略，因为悄悄丢掉一个时间筛选，等于回答了一个与提问不同的问题。
func timeParam(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// -----------------------------------------------------------------------
// Operator: the fleet inventory
// -----------------------------------------------------------------------

// listFleetNodes returns every node the configured Gateway replicas report.
//
// It serves the fleet package's own types rather than a copy in this package.
// Those structs already carry the JSON tags that are the contract — they have
// to, because the aggregation reads the same shape from the Gateway — and a
// second declaration here would be a second thing to keep in step with it.
//
// The response is deliberately not filtered by anything: there is no tenant
// dimension on a node to filter by, which is exactly why this endpoint is
// behind the operator token instead of a session.
//
// listFleetNodes 返回已配置的各 Gateway 副本所报告的全部节点。
//
// 它直接提供 fleet 包自己的类型，而不是本包里的一份副本。那些结构体本来就带着构成契约
// 的 JSON 标签——它们必须带，因为聚合正是从 Gateway 读取同一种形状——在这里再声明一遍，
// 只会多出一样需要与之保持同步的东西。
//
// 该响应刻意不做任何过滤：节点身上没有可供过滤的租户维度，而这恰恰就是本端点由运维
// token 而不是会话守卫的原因。
func listFleetNodes(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := ctx.Fleet.Nodes(r.Context())
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}

// listFleetModels returns the same read, seen as a model catalog.
//
// listFleetModels 返回同一次读取，以模型目录的视角呈现。
func listFleetModels(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		catalog, err := ctx.Fleet.Models(r.Context())
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, catalog)
	}
}

// listWorkflows returns the workflow menu to a signed-in tenant user.
//
// The catalogue is the same for every tenant — templates are the Gateway's own
// file configuration — and it is on the session-guarded API because it is what
// a caller needs in order to submit a run at all. What it does not carry is
// the graph, which never leaves the Gateway; see common/workflowview.
//
// listWorkflows 把工作流菜单返回给已登录的租户用户。
//
// 这份目录对每个租户都相同——模板是 Gateway 自己的文件配置——它放在由会话守卫的 API 上，
// 因为那是调用方提交一次运行所必需的东西。它不携带的是图，图从不离开 Gateway；
// 见 common/workflowview。
func listWorkflows(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := actorFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		catalogue, err := ctx.Fleet.Workflows(r.Context())
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		// A tenant is shown the menu, not the fleet. Both the per-template
		// replica list and the per-replica status carry replica ids and the
		// configured endpoints — internal hostnames and ports — and this
		// response is on its way to a tenant's browser. What survives is
		// Partial, which is the part a tenant can act on: the list may be
		// incomplete.
		//
		// 租户看到的是菜单，不是机群。逐模板的副本列表与逐副本的状态都携带副本 id 与
		// 配置的 endpoint——内部主机名与端口——而这个响应正在前往租户的浏览器。留下来的
		// 是 Partial，那是租户能据以行动的部分：这份列表可能不完整。
		catalogue.Replicas = nil
		for i := range catalogue.Templates {
			catalogue.Templates[i].Replicas = nil
		}
		writeJSON(w, http.StatusOK, catalogue)
	}
}

// listOperatorWorkflows returns the same catalogue with the rollout visible.
//
// listOperatorWorkflows 返回同一份目录，但发布状态可见。
func listOperatorWorkflows(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		catalogue, err := ctx.Fleet.Workflows(r.Context())
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, catalogue)
	}
}

// listJobs returns the caller's own tenant's current runs.
//
// The tenant comes from the session and is passed down to each replica, so
// the filtering happens in the job table rather than here. This is a live
// view: the Gateway's job table is in memory, bounded and per replica, and the
// response says so through Truncated and Partial rather than leaving a short
// list to be read as a quiet week.
//
// listJobs 返回调用方自己所属租户当前的运行。
//
// 租户来自会话，并被向下传给每个副本，因此过滤发生在 job 表里而不是这里。这是一个实时
// 视图：Gateway 的 job 表位于内存、有上限、且每副本各自持有，响应通过 Truncated 与
// Partial 说明这一点，而不是任由一份短列表被读成「这一周很清闲」。
func listJobs(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		view, err := ctx.Fleet.Jobs(r.Context(), actor.TenantID)
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		// Where a run is held is not a tenant's answer, by the same reasoning
		// that keeps the node inventory off this API: a replica id and a
		// configured endpoint are infrastructure identity. Partial and
		// Truncated survive because they are what stop the list from being
		// read as complete.
		//
		// 一次运行被谁持有不是租户的答案，理由与把节点清单挡在本 API 之外的相同：副本
		// id 与配置的 endpoint 都属于基础设施身份。Partial 与 Truncated 保留下来，
		// 因为它们正是阻止这份列表被读成「完整」的东西。
		view.Replicas = nil
		for i := range view.Jobs {
			view.Jobs[i].Replica = ""
		}
		writeJSON(w, http.StatusOK, view)
	}
}

// respondFleetErr maps an aggregation failure. A replica that did not answer
// is not one of these — that is a partial success, and it is reported inside
// the document rather than as a status code, because the nodes that did answer
// are still worth showing.
//
// respondFleetErr 映射一次聚合失败。某个副本没有作答不属于这里的情形——那是部分成功，
// 它在文档内部报告而不是用状态码报告，因为已经作答的那些节点依然值得展示。
func respondFleetErr(w http.ResponseWriter, err error) {
	if errors.Is(err, fleet.ErrDisabled) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
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

// -----------------------------------------------------------------------
// Internal: Job persistence (STATUS.md's J04)
// -----------------------------------------------------------------------

// createJob handles POST /internal/v1/jobs: a Gateway replica reporting a
// run it just submitted. See the ControlPlane README's 「Job 持久化契约」 for
// why this call must never be on the inference request's own critical path
// — that discipline belongs to the Gateway-side client (STATUS.md's J04)
// and the caller of it (J05), not to this handler, which only does the
// write it is asked to do.
//
// createJob 处理 POST /internal/v1/jobs：一个 Gateway 副本报告它刚提交的一次
// 运行。为什么这次调用绝不能出现在推理请求自己的关键路径上，见 ControlPlane
// README「Job 持久化契约」——那份纪律属于 Gateway 侧的客户端（STATUS.md 的
// J04）与调用它的那一方（J05），不属于这个只负责完成被要求的写入的 handler。
func createJob(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateJobRequest
		if !decode(w, r, &req) {
			return
		}
		job, err := ctx.Logic.CreateJob(r.Context(), logic.CreateJobParams{
			JobID:           req.JobID,
			TenantID:        req.TenantID,
			WorkflowID:      req.WorkflowID,
			WorkflowVersion: req.WorkflowVersion,
			NodeID:          req.NodeID,
			RuntimeID:       req.RuntimeID,
			BackendRunID:    req.BackendRunID,
			State:           req.State,
			ObservedSeq:     req.ObservedSeq,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderJob(job))
	}
}

// listActiveJobsForRoute handles GET /internal/v1/jobs/active. It answers a
// recovering Gateway replica's question "what do I owe this route binding"
// (STATUS.md's J06): node_id and runtime_id are query parameters and there
// is no tenant_id — this is the one internal Job endpoint not scoped by
// tenant, for the same reason GetAPIKeyByHash is not (see store.Jobs'
// ListActiveJobsForRoute).
//
// listActiveJobsForRoute 处理 GET /internal/v1/jobs/active。它回答一个正在
// 恢复的 Gateway 副本的问题——「我欠这个路由绑定什么」（STATUS.md 的 J06）：
// node_id 与 runtime_id 是查询参数，且没有 tenant_id——这是唯一一个不按租户
// 限定范围的内部 Job 端点，理由与 GetAPIKeyByHash 相同（见 store.Jobs 的
// ListActiveJobsForRoute）。
func listActiveJobsForRoute(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		nodeID, runtimeID := query.Get("node_id"), query.Get("runtime_id")
		if nodeID == "" || runtimeID == "" {
			writeError(w, http.StatusBadRequest, "node_id and runtime_id are required")
			return
		}
		jobs, err := ctx.Logic.ListActiveJobsForRoute(r.Context(), nodeID, runtimeID)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.JobResponse, len(jobs))
		for i, j := range jobs {
			out[i] = renderJob(j)
		}
		writeJSON(w, http.StatusOK, types.ListActiveJobsResponse{Items: out})
	}
}

// getJob handles GET /internal/v1/jobs/:id. tenant_id is a query parameter
// rather than something this endpoint infers, for the same reason it is a
// body field on createJob: there is no session here to read it from, only
// the Gateway's own assertion.
//
// getJob 处理 GET /internal/v1/jobs/:id。tenant_id 是查询参数，而不是本端点自行
// 推断的东西，理由与它在 createJob 里是请求体字段相同：这里没有会话可供读取，
// 只有 Gateway 自己的断言。
func getJob(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := pathvar.Vars(r)["id"]
		tenantID := r.URL.Query().Get("tenant_id")
		if jobID == "" || tenantID == "" {
			writeError(w, http.StatusBadRequest, "a job id and tenant_id are required")
			return
		}
		job, err := ctx.Logic.GetJob(r.Context(), tenantID, jobID)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderJob(job))
	}
}

// updateJobState handles PATCH /internal/v1/jobs/:id/state. It is PATCH
// rather than PUT because the request is one observation to be reconciled
// against what is already on record, per store.JobStateUpdate's
// ObservedSeq gate — not the whole row to overwrite.
//
// updateJobState 处理 PATCH /internal/v1/jobs/:id/state。用 PATCH 而不是 PUT，
// 是因为这次请求是一次要与已有记录相协调的观测——依据 store.JobStateUpdate 的
// ObservedSeq 门槛——而不是要整行覆盖。
func updateJobState(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := pathvar.Vars(r)["id"]
		if jobID == "" {
			writeError(w, http.StatusBadRequest, "a job id is required")
			return
		}
		var req types.UpdateJobStateRequest
		if !decode(w, r, &req) {
			return
		}
		applied, job, err := ctx.Logic.UpdateJobState(r.Context(), req.TenantID, jobID, logic.UpdateJobStateParams{
			State:        req.State,
			ErrorSummary: req.ErrorSummary,
			ObservedSeq:  req.ObservedSeq,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.UpdateJobStateResponse{Applied: applied, Job: renderJob(job)})
	}
}

// createJobArtifact handles POST /internal/v1/jobs/:id/artifacts.
//
// createJobArtifact 处理 POST /internal/v1/jobs/:id/artifacts。
func createJobArtifact(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := pathvar.Vars(r)["id"]
		if jobID == "" {
			writeError(w, http.StatusBadRequest, "a job id is required")
			return
		}
		var req types.CreateJobArtifactRequest
		if !decode(w, r, &req) {
			return
		}
		artifact, err := ctx.Logic.CreateJobArtifact(r.Context(), logic.CreateJobArtifactParams{
			ArtifactID: req.ArtifactID,
			JobID:      jobID,
			TenantID:   req.TenantID,
			Filename:   req.Filename,
			Subfolder:  req.Subfolder,
			Type:       req.Type,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderJobArtifact(artifact))
	}
}

// listJobArtifacts handles GET /internal/v1/jobs/:id/artifacts.
//
// listJobArtifacts 处理 GET /internal/v1/jobs/:id/artifacts。
func listJobArtifacts(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := pathvar.Vars(r)["id"]
		tenantID := r.URL.Query().Get("tenant_id")
		if jobID == "" || tenantID == "" {
			writeError(w, http.StatusBadRequest, "a job id and tenant_id are required")
			return
		}
		artifacts, err := ctx.Logic.ListJobArtifacts(r.Context(), tenantID, jobID)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := make([]types.JobArtifactResponse, len(artifacts))
		for i, a := range artifacts {
			out[i] = renderJobArtifact(a)
		}
		writeJSON(w, http.StatusOK, types.ListJobArtifactsResponse{Items: out})
	}
}

// renderJob converts a stored job to its internal wire form — the storage
// layer's view, route binding included. See types.JobResponse for why this
// is safe only because nothing on the tenant-facing Admin API ever calls it.
//
// renderJob 把存储的 job 转换成内部线上形式——存储层的视角，包含路由绑定。这样
// 做为何安全，仅仅是因为面向租户的 Admin API 从不调用它，见 types.JobResponse。
func renderJob(job model.Job) types.JobResponse {
	return types.JobResponse{
		JobID:           job.ID,
		TenantID:        job.TenantID,
		WorkflowID:      job.WorkflowID,
		WorkflowVersion: job.WorkflowVersion,
		NodeID:          job.NodeID,
		RuntimeID:       job.RuntimeID,
		BackendRunID:    job.BackendRunID,
		State:           job.State,
		ErrorSummary:    job.ErrorSummary,
		ObservedSeq:     job.ObservedSeq,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		TerminalAt:      job.TerminalAt,
	}
}

// renderJobArtifact converts a stored artifact to its wire form.
//
// renderJobArtifact 把存储的产物转换成线上形式。
func renderJobArtifact(a model.JobArtifact) types.JobArtifactResponse {
	return types.JobArtifactResponse{
		ArtifactID: a.ID,
		JobID:      a.JobID,
		TenantID:   a.TenantID,
		Filename:   a.Filename,
		Subfolder:  a.Subfolder,
		Type:       a.Type,
		CreatedAt:  a.CreatedAt,
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
