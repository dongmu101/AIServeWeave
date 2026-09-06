package e2e_test

import (
	"context"
	"net/http"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
)

// gatewayJobsClient returns the Gateway's real Job persistence client,
// pointed at this control plane — the same "use the real other side"
// approach gatewayVerifier already takes in flow_test.go.
//
// gatewayJobsClient 返回 Gateway 真实的 Job 持久化客户端，指向本控制面——与
// flow_test.go 里 gatewayVerifier 已经采用的「用真实的另一侧」是同一种做法。
func gatewayJobsClient(h *harness) *controlplaneclient.JobsClient {
	h.t.Helper()
	client, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{
		Endpoint: h.base,
		Token:    internalToken,
	})
	if err != nil {
		h.t.Fatalf("controlplaneclient.NewJobsClient: %v", err)
	}
	return client
}

// TestJobLifecycleThroughTheRealGatewayClient drives create, get, and a
// sequence of state updates through controlplaneclient.JobsClient against a
// real control plane — the closed loop STATUS.md's J04 exists for, the same
// way TestKeyIssuedByTheConsoleAuthenticatesAtTheGateway closes the loop for
// key verification.
//
// TestJobLifecycleThroughTheRealGatewayClient 用 controlplaneclient.JobsClient
// 对着一个真实的控制面走完创建、读取与一连串状态更新——这正是 STATUS.md 的
// J04 存在的意义所在的那个闭环，与 TestKeyIssuedByTheConsoleAuthenticatesAtTheGateway
// 为 key 校验闭合的是同一种环路。
func TestJobLifecycleThroughTheRealGatewayClient(t *testing.T) {
	h := newHarness(t)
	client := gatewayJobsClient(h)
	ctx := context.Background()

	created, err := client.CreateJob(ctx, controlplaneclient.CreateJobRequest{
		JobID: "job_e2e_1", TenantID: "tenant-a", WorkflowID: "text-to-image",
		NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-1",
		State: "pending", ObservedSeq: 0,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if created.JobID != "job_e2e_1" || created.State != "pending" {
		t.Fatalf("CreateJob returned %+v, want job_id=job_e2e_1 state=pending", created)
	}

	// A retried create for the same tenant is idempotent.
	//
	// 同一租户下重试一次创建是幂等的。
	again, err := client.CreateJob(ctx, controlplaneclient.CreateJobRequest{
		JobID: "job_e2e_1", TenantID: "tenant-a", WorkflowID: "text-to-image",
		NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-1",
		State: "pending", ObservedSeq: 0,
	})
	if err != nil || again.JobID != created.JobID {
		t.Fatalf("retried CreateJob = %+v, %v, want the same job back with no error", again, err)
	}

	got, err := client.GetJob(ctx, "tenant-a", "job_e2e_1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.NodeID != "node-1" || got.BackendRunID != "prompt-1" {
		t.Errorf("GetJob route binding = {node_id: %q, backend_run_id: %q}, want node-1, prompt-1", got.NodeID, got.BackendRunID)
	}

	applied, updated, err := client.UpdateJobState(ctx, "tenant-a", "job_e2e_1", controlplaneclient.JobStateUpdate{
		State: "succeeded", ObservedSeq: 1,
	})
	if err != nil || !applied || updated.State != "succeeded" {
		t.Fatalf("UpdateJobState: applied=%v state=%v err=%v, want true, succeeded, nil", applied, updated.State, err)
	}

	// A stale update after the terminal state is a silent no-op, not an
	// error — the persistence contract's own vocabulary, exercised here
	// through the real HTTP round trip rather than a unit test double.
	//
	// 终态之后一次陈旧的更新是无声的空操作，不是错误——这正是持久化契约自己的
	// 词汇，这里通过真实的 HTTP 往返来验证，而不是单元测试的替身。
	applied, unchanged, err := client.UpdateJobState(ctx, "tenant-a", "job_e2e_1", controlplaneclient.JobStateUpdate{
		State: "failed", ObservedSeq: 1,
	})
	if err != nil {
		t.Fatalf("stale UpdateJobState returned an error: %v, want nil", err)
	}
	if applied || unchanged.State != "succeeded" {
		t.Errorf("stale UpdateJobState = applied:%v state:%v, want false, succeeded", applied, unchanged.State)
	}

	// An unknown tenant reading a real job id gets ErrNotFound, the same way
	// it would for any other resource in this service — a job belonging to
	// someone else must read exactly like one that does not exist.
	//
	// 一个未知租户读取一个真实存在的 job id 会得到 ErrNotFound，与本服务里
	// 其他任何资源相同——属于别人的 job，读起来必须与不存在的 job 完全一样。
	if _, err := client.GetJob(ctx, "tenant-b", "job_e2e_1"); err != controlplaneclient.ErrNotFound {
		t.Errorf("GetJob(wrong tenant) = %v, want ErrNotFound", err)
	}
}

// TestListActiveJobsForRouteRecoversAcrossTenants is STATUS.md's J06 closed
// loop: a Gateway replica asking "what do I owe this route binding" gets
// back jobs from every tenant bound to that node/runtime, and nothing bound
// to a different one.
//
// TestListActiveJobsForRouteRecoversAcrossTenants 是 STATUS.md J06 的闭环：
// 一个 Gateway 副本发问「我欠这个路由绑定什么」，得到的是绑定在那个
// 节点/runtime 上、来自每一个租户的 job，而不包含绑定在别的路由上的任何 job。
func TestListActiveJobsForRouteRecoversAcrossTenants(t *testing.T) {
	h := newHarness(t)
	client := gatewayJobsClient(h)
	ctx := context.Background()

	for _, req := range []controlplaneclient.CreateJobRequest{
		{JobID: "job_a", TenantID: "tenant-a", WorkflowID: "wf", NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-a", State: "running"},
		{JobID: "job_b", TenantID: "tenant-b", WorkflowID: "wf", NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-b", State: "pending"},
		{JobID: "job_c", TenantID: "tenant-a", WorkflowID: "wf", NodeID: "node-2", RuntimeID: "comfy-2", BackendRunID: "prompt-c", State: "running"},
	} {
		if _, err := client.CreateJob(ctx, req); err != nil {
			t.Fatalf("CreateJob(%s): %v", req.JobID, err)
		}
	}
	// A terminal job on the same route must not come back: recovery only
	// ever needs runs that are still in flight.
	//
	// 同一路由上的一个终态 job 不应被返回：恢复只需要仍在进行中的运行。
	if _, err := client.CreateJob(ctx, controlplaneclient.CreateJobRequest{
		JobID: "job_done", TenantID: "tenant-a", WorkflowID: "wf", NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-done", State: "pending",
	}); err != nil {
		t.Fatalf("CreateJob(job_done): %v", err)
	}
	if _, _, err := client.UpdateJobState(ctx, "tenant-a", "job_done", controlplaneclient.JobStateUpdate{State: "succeeded", ObservedSeq: 1}); err != nil {
		t.Fatalf("UpdateJobState(job_done): %v", err)
	}

	active, err := client.ListActiveJobsForRoute(ctx, "node-1", "comfy-1")
	if err != nil {
		t.Fatalf("ListActiveJobsForRoute: %v", err)
	}
	ids := map[string]bool{}
	for _, j := range active {
		ids[j.JobID] = true
	}
	if !ids["job_a"] || !ids["job_b"] {
		t.Errorf("ListActiveJobsForRoute(node-1, comfy-1) = %v, want job_a and job_b (both tenants)", ids)
	}
	if ids["job_c"] {
		t.Error("ListActiveJobsForRoute(node-1, comfy-1) included job_c, which is bound to a different route")
	}
	if ids["job_done"] {
		t.Error("ListActiveJobsForRoute(node-1, comfy-1) included job_done, which is already terminal")
	}
}

// TestJobHistoryIsIsolatedByTenantOverHTTP is STATUS.md's J07 closed loop
// for the tenant-facing side: a run persisted through the Gateway's
// internal API becomes visible to its own tenant's session through
// GET /admin/v1/jobs/history, and invisible to another tenant's — the same
// isolation TestTenantsAreIsolatedOverHTTP already asserts for API keys,
// exercised here for job history instead.
//
// TestJobHistoryIsIsolatedByTenantOverHTTP 是 STATUS.md J07 面向租户一侧的
// 闭环：一次经由 Gateway 内部 API 持久化的运行，能被自己租户的会话通过
// GET /admin/v1/jobs/history 看到，且对另一个租户的会话不可见——与
// TestTenantsAreIsolatedOverHTTP 已经为 API Key 断言过的是同一种隔离，这里
// 换成了 job 历史。
func TestJobHistoryIsIsolatedByTenantOverHTTP(t *testing.T) {
	h := newHarness(t)
	tenantA, sessionA := bootstrap(h, "Acme", "acme-owner@example.com")
	_, sessionB := bootstrap(h, "Beta", "beta-owner@example.com")

	client := gatewayJobsClient(h)
	if _, err := client.CreateJob(context.Background(), controlplaneclient.CreateJobRequest{
		JobID: "job_history_1", TenantID: tenantA.Tenant.ID, WorkflowID: "text-to-image",
		NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-1", State: "succeeded",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	var listedByA types.JobHistoryListResponse
	if status := h.call(http.MethodGet, "/admin/v1/jobs/history", sessionA, nil, &listedByA); status != http.StatusOK {
		t.Fatalf("listing job history as tenant A: status %d", status)
	}
	found := false
	for _, j := range listedByA.Items {
		if j.JobID == "job_history_1" {
			found = true
			if j.State != "succeeded" {
				t.Errorf("job history entry state = %q, want succeeded", j.State)
			}
		}
	}
	if !found {
		t.Fatal("tenant A's own job did not appear in its job history")
	}

	var listedByB types.JobHistoryListResponse
	if status := h.call(http.MethodGet, "/admin/v1/jobs/history", sessionB, nil, &listedByB); status != http.StatusOK {
		t.Fatalf("listing job history as tenant B: status %d", status)
	}
	for _, j := range listedByB.Items {
		if j.JobID == "job_history_1" {
			t.Error("tenant B can see tenant A's job in its own job history listing")
		}
	}

	var detail types.JobHistoryResponse
	if status := h.call(http.MethodGet, "/admin/v1/jobs/history/job_history_1", sessionA, nil, &detail); status != http.StatusOK {
		t.Fatalf("tenant A reading its own job's detail: status %d", status)
	}
	if detail.JobID != "job_history_1" {
		t.Errorf("detail.JobID = %q, want job_history_1", detail.JobID)
	}

	if status := h.call(http.MethodGet, "/admin/v1/jobs/history/job_history_1", sessionB, nil, nil); status != http.StatusNotFound {
		t.Errorf("tenant B reading tenant A's job detail: status = %d, want 404", status)
	}
}

// TestJobHistoryRejectsAMalformedTimeWindow asserts the since/until
// validation flow_test.go's audit tests already exercise for /admin/v1/audit
// holds for job history too, over the real HTTP path.
//
// TestJobHistoryRejectsAMalformedTimeWindow 断言 flow_test.go 的审计测试已经
// 为 /admin/v1/audit 验证过的 since/until 校验，在 job 历史上同样成立，走的
// 是真实 HTTP 路径。
func TestJobHistoryRejectsAMalformedTimeWindow(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "acme-owner2@example.com")

	status := h.call(http.MethodGet, "/admin/v1/jobs/history?since=not-a-timestamp", session, nil, nil)
	if status != http.StatusBadRequest {
		t.Errorf("since=not-a-timestamp: status = %d, want 400", status)
	}
}

// TestListActiveJobsForRouteRoutesAheadOfTheParameterizedGetJobRoute pins
// the routing precedence routes.go's own comment documents: a raw call to
// /internal/v1/jobs/active must dispatch to listActiveJobsForRoute, not be
// swallowed by /internal/v1/jobs/:id's :id capturing the literal "active".
// Calling it with no tenant_id and getting anything other than getJob's 400
// ("a job id and tenant_id are required") is how this test tells the two
// apart.
//
// TestListActiveJobsForRouteRoutesAheadOfTheParameterizedGetJobRoute 钉住了
// routes.go 自己注释里记录的路由优先级：一次对 /internal/v1/jobs/active 的
// 原始调用必须分派到 listActiveJobsForRoute，而不是被 /internal/v1/jobs/:id
// 的 :id 捕获成字面量 "active"。不带 tenant_id 调用它、且得到的不是 getJob
// 那个 400（"a job id and tenant_id are required"），就是本测试用来分辨两者
// 的方式。
func TestListActiveJobsForRouteRoutesAheadOfTheParameterizedGetJobRoute(t *testing.T) {
	h := newHarness(t)

	var body types.ListActiveJobsResponse
	status := h.call(http.MethodGet, "/internal/v1/jobs/active?node_id=node-1&runtime_id=comfy-1", internalToken, nil, &body)
	if status != http.StatusOK {
		t.Fatalf("GET /internal/v1/jobs/active status = %d, want 200 (routed to listActiveJobsForRoute) — a 400 here would mean it fell through to getJob's :id route instead", status)
	}
	if body.Items == nil && len(body.Items) != 0 {
		t.Errorf("body.Items = %v, want an empty (possibly nil) slice for a route with no active jobs", body.Items)
	}
}
