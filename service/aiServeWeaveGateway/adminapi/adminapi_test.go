package adminapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeview"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/workflowview"
	"AIServeWeave/service/aiServeWeaveGateway/adminapi"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// TestMain asserts no test in this package leaks a goroutine, per the
// repository quality gate.
//
// TestMain 断言本包没有测试泄漏协程，对应仓库的质量门禁。
func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !settles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func settles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fixedClock is the injected clock, so a generated_at in a failure message is
// recognizably the test's rather than today's.
//
// fixedClock 是注入的时钟，这样失败信息里的 generated_at 一望即知是测试的而不是今天的。
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
func (c fixedClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

const token = "operator-token-long-enough-for-a-test"

var probedAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// connectedNode is one node with a full inventory, for the rendering tests.
//
// connectedNode 是一个清单完整的节点，供渲染测试使用。
func connectedNode() tunnelserver.NodeInfo {
	return tunnelserver.NodeInfo{
		NodeID:       "node-a",
		AgentVersion: "1.2.3",
		Live:         true,
		Labels:       map[string]string{"zone": "rack-1"},
		Resources:    &tunnelv1.NodeResources{CpuCores: 32, GpuCount: 2, Os: "linux", Arch: "amd64"},
		RuntimeIDs:   []string{"vllm-a", "declared-but-unreported"},
		IdleSlots: map[tunnelv1.SlotClass]int{
			tunnelv1.SlotClass_SLOT_CLASS_INFERENCE: 4,
			tunnelv1.SlotClass_SLOT_CLASS_BULK:      1,
		},
		InflightRequests: 2,
		LastHeartbeat:    probedAt,
		Runtimes: []runtime.Snapshot{{
			Descriptor: runtime.Descriptor{
				ID:      "vllm-a",
				Kind:    runtime.KindVLLM,
				BaseURL: "http://127.0.0.1:8000",
			},
			State: runtime.StateHealthy,
			Probe: runtime.ProbeResult{Version: "0.6.0", IdentityVerified: true, ProbedAt: probedAt},
			Discovery: runtime.Discovery{
				Models:       []runtime.Model{{ID: "qwen-7b"}, {ID: "llama-3"}},
				DiscoveredAt: probedAt,
			},
			UpdatedAt: probedAt,
		}},
	}
}

func serve(t *testing.T, nodes []tunnelserver.NodeInfo) http.Handler {
	t.Helper()
	handler, err := adminapi.New(adminapi.Config{
		Token:     token,
		ReplicaID: "replica-1",
		Clock:     fixedClock{now: probedAt},
		Nodes:     func() []tunnelserver.NodeInfo { return nodes },
	})
	if err != nil {
		t.Fatalf("adminapi.New: %v", err)
	}
	return handler
}

// TestInventoryRequiresTheOperatorToken asserts the listener authenticates,
// which is the whole reason it may exist on its own port.
//
// TestInventoryRequiresTheOperatorToken 断言该监听器会做认证，而这正是它可以独占一个
// 端口的全部理由。
func TestInventoryRequiresTheOperatorToken(t *testing.T) {
	handler := serve(t, []tunnelserver.NodeInfo{connectedNode()})

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{name: "the operator token", authHeader: "Bearer " + token, wantStatus: http.StatusOK},
		{name: "no header at all", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "the wrong token", authHeader: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "a token with no scheme", authHeader: token, wantStatus: http.StatusUnauthorized},
		{name: "the right token, wrong scheme", authHeader: "Basic " + token, wantStatus: http.StatusUnauthorized},
		{name: "a prefix of the token", authHeader: "Bearer " + token[:10], wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// TestInventoryRendersAnAllowlist asserts the document holds the fields this
// view names and nothing else.
//
// A runtime's credentials are not in a Snapshot today: they live in
// runtime.Config on the Agent, and common/tunnelwire drops the API key before
// anything crosses. This test is what keeps that true from this side — if a
// future change carried a key or a header into a Snapshot, the rendering here
// would not pick it up, and this assertion says so out loud.
//
// TestInventoryRendersAnAllowlist 断言这份文档只包含本视图点名的字段，别无其他。
//
// 今天的 Snapshot 里没有运行时凭据：它们在 Agent 的 runtime.Config 中，而
// common/tunnelwire 在任何东西过隧道之前就丢弃了 API key。本测试是从这一侧保证这件事
// 继续成立的东西——如果将来某次改动把 key 或请求头带进了 Snapshot，这里的渲染也不会把
// 它取出来，而这条断言正是把这一点明说出来。
func TestInventoryRendersAnAllowlist(t *testing.T) {
	handler := serve(t, []tunnelserver.NodeInfo{connectedNode()})
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, forbidden := range []string{"api_key", "apikey", "headers", "authorization", "token", "secret"} {
		if contains(body, forbidden) {
			t.Errorf("the inventory rendered %q:\n%s", forbidden, body)
		}
	}
	// The base URL does cross, and that is a decision rather than an
	// oversight: an operator diagnosing a node needs it, and it is not a
	// credential. Asserting it here means removing it is also a decision.
	//
	// base URL 确实会外传，这是一个决定而不是疏忽：排查节点的运维需要它，而它不是
	// 凭据。在这里断言它，意味着「拿掉它」同样是一个决定。
	if !contains(body, "http://127.0.0.1:8000") {
		t.Errorf("the inventory dropped the base URL, which operators need:\n%s", body)
	}
}

// TestInventoryRendersWhatAConsoleNeeds covers the fields a fleet view is
// built from, including the two that must stay distinguishable from zero.
//
// TestInventoryRendersWhatAConsoleNeeds 覆盖机群视图所依赖的那些字段，包括必须与零值
// 保持可区分的那两个。
func TestInventoryRendersWhatAConsoleNeeds(t *testing.T) {
	handler := serve(t, []tunnelserver.NodeInfo{connectedNode()})
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var got nodeview.Replica
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the inventory: %v", err)
	}

	if got.ReplicaID != "replica-1" {
		t.Errorf("ReplicaID = %q, want %q", got.ReplicaID, "replica-1")
	}
	if !got.GeneratedAt.Equal(probedAt) {
		t.Errorf("GeneratedAt = %v, want the injected %v", got.GeneratedAt, probedAt)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(got.Nodes))
	}

	node := got.Nodes[0]
	if node.NodeID != "node-a" || !node.Live || node.Draining {
		t.Errorf("node = %+v, want node-a live and not draining", node)
	}
	if node.Labels["zone"] != "rack-1" {
		t.Errorf("Labels = %v, want zone=rack-1", node.Labels)
	}
	if node.Resources == nil || node.Resources.GPUCount != 2 {
		t.Errorf("Resources = %+v, want 2 GPUs", node.Resources)
	}
	if node.IdleSlots["inference"] != 4 || node.IdleSlots["bulk"] != 1 {
		t.Errorf("IdleSlots = %v, want inference=4 bulk=1", node.IdleSlots)
	}
	if len(node.DeclaredRuntimeIDs) != 2 {
		t.Errorf("DeclaredRuntimeIDs = %v, want both declared ids", node.DeclaredRuntimeIDs)
	}
	if len(node.Runtimes) != 1 {
		t.Fatalf("got %d runtimes, want 1", len(node.Runtimes))
	}
	rt := node.Runtimes[0]
	if rt.ID != "vllm-a" || rt.Kind != "vllm" || rt.State != "healthy" {
		t.Errorf("runtime = %+v, want vllm-a/vllm/healthy", rt)
	}
	// Models are sorted, so two reads of an unchanged node compare equal.
	//
	// 模型是排序的，因此对一个未变化的节点做两次读取会比较相等。
	if len(rt.Models) != 2 || rt.Models[0].ID != "llama-3" || rt.Models[1].ID != "qwen-7b" {
		t.Errorf("Models = %+v, want sorted llama-3 then qwen-7b", rt.Models)
	}
	// No health check has completed, so latency is absent rather than zero:
	// zero milliseconds is a measurement and would be a lie.
	//
	// 尚未完成过健康检查，因此耗时是缺席而不是零：零毫秒是一个测量结果，在这里会是谎言。
	if rt.LatencyMillis != nil || rt.CheckedAt != nil {
		t.Errorf("latency = %v checked = %v, want both absent", rt.LatencyMillis, rt.CheckedAt)
	}
}

// TestAnEmptyReplicaSaysSoWithATimestamp asserts a replica with no tunnels
// answers an empty list and still says when it looked. Without the timestamp a
// console cannot tell "no nodes" from "this replica stopped answering".
//
// TestAnEmptyReplicaSaysSoWithATimestamp 断言一个没有隧道的副本会回答空列表，并且仍然
// 说明它是何时看的。没有这个时间戳，控制台就无法分辨「没有节点」与「这个副本不再作答」。
func TestAnEmptyReplicaSaysSoWithATimestamp(t *testing.T) {
	handler := serve(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var got nodeview.Replica
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the inventory: %v", err)
	}
	if got.Nodes == nil {
		t.Error("Nodes is null; an empty fleet must be an empty list, not a missing field")
	}
	if len(got.Nodes) != 0 {
		t.Errorf("got %d nodes, want none", len(got.Nodes))
	}
	if got.GeneratedAt.IsZero() {
		t.Error("GeneratedAt is zero on an empty reply")
	}
}

// TestTheListenerRefusesToStartUnconfigured asserts the two construction
// failures, both of which would otherwise serve something worse than nothing.
//
// TestTheListenerRefusesToStartUnconfigured 断言那两种构造失败，否则它们都会提供出比
// 「什么都不提供」更糟的东西。
func TestTheListenerRefusesToStartUnconfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  adminapi.Config
	}{
		{name: "no token", cfg: adminapi.Config{Nodes: func() []tunnelserver.NodeInfo { return nil }}},
		{name: "no node source", cfg: adminapi.Config{Token: token}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := adminapi.New(tt.cfg); err == nil {
				t.Error("New() error = nil, want a refusal")
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestTheJobEndpointRequiresATenant is the isolation assertion for the one
// endpoint here that answers about tenant data. This replica's table holds
// every tenant's runs, so an unnamed tenant must be refused rather than read
// as "all of them".
//
// TestTheJobEndpointRequiresATenant 是对这里唯一回答租户数据的端点所做的隔离断言。
// 本副本的表持有每个租户的运行，因此未点名租户必须被拒绝，而不是被读作「全部」。
func TestTheJobEndpointRequiresATenant(t *testing.T) {
	asked := make([]string, 0, 2)
	handler, err := adminapi.New(adminapi.Config{
		Token:     token,
		ReplicaID: "replica-1",
		Clock:     fixedClock{now: probedAt},
		Nodes:     func() []tunnelserver.NodeInfo { return nil },
		Jobs: func(tenantID string) ([]workflowview.Job, bool) {
			asked = append(asked, tenantID)
			return []workflowview.Job{{
				ID:         "job-1",
				WorkflowID: "portrait",
				State:      "running",
				CreatedAt:  probedAt,
				UpdatedAt:  probedAt,
			}}, true
		},
	})
	if err != nil {
		t.Fatalf("adminapi.New: %v", err)
	}

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{name: "a named tenant", query: "?tenant_id=tnt_1", wantStatus: http.StatusOK},
		{name: "no tenant at all", query: "", wantStatus: http.StatusBadRequest},
		{name: "an empty tenant", query: "?tenant_id=", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/jobs"+tt.query, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}

	if len(asked) != 1 || asked[0] != "tnt_1" {
		t.Errorf("the job source was asked for %v, want exactly [tnt_1]", asked)
	}
}

// TestAJobPageSaysWhenTheTableHasDroppedRuns asserts the eviction flag
// survives to the wire. Without it a short list reads as a quiet period, which
// is the one conclusion a bounded in-memory table cannot support.
//
// TestAJobPageSaysWhenTheTableHasDroppedRuns 断言逐出标志会传到线上。没有它，一份很短
// 的列表会被读成「这段时间很清闲」——而那恰恰是一张有上限的内存表最无法支撑的结论。
func TestAJobPageSaysWhenTheTableHasDroppedRuns(t *testing.T) {
	handler, err := adminapi.New(adminapi.Config{
		Token:     token,
		ReplicaID: "replica-1",
		Clock:     fixedClock{now: probedAt},
		Nodes:     func() []tunnelserver.NodeInfo { return nil },
		Jobs: func(string) ([]workflowview.Job, bool) {
			return []workflowview.Job{{ID: "job-1", WorkflowID: "portrait", State: "succeeded"}}, true
		},
	})
	if err != nil {
		t.Fatalf("adminapi.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/jobs?tenant_id=tnt_1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var page workflowview.JobPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decoding the job page: %v", err)
	}
	if !page.Truncated {
		t.Error("Truncated is false although the table reported an eviction")
	}
	if page.TenantID != "tnt_1" {
		t.Errorf("TenantID = %q, want the tenant that was asked for", page.TenantID)
	}
	if page.ReplicaID != "replica-1" || page.GeneratedAt.IsZero() {
		t.Errorf("page = %+v, want the replica and the instant named", page)
	}
}

// TestTheTemplateCatalogueNeverCarriesTheGraph is the security assertion for
// C25: the repository puts a full workflow JSON in the same class as an API
// key, and a catalogue exists to name the menu, not to hand out the recipes.
//
// TestTheTemplateCatalogueNeverCarriesTheGraph 是 C25 的安全断言：仓库把完整的工作流
// JSON 与 API key 归为同一类，而目录的存在是为了点出菜单，不是为了发放菜谱。
func TestTheTemplateCatalogueNeverCarriesTheGraph(t *testing.T) {
	handler, err := adminapi.New(adminapi.Config{
		Token:     token,
		ReplicaID: "replica-1",
		Clock:     fixedClock{now: probedAt},
		Nodes:     func() []tunnelserver.NodeInfo { return nil },
		Templates: func() []workflowview.Template {
			return []workflowview.Template{{
				ID:          "portrait",
				Description: "A portrait workflow",
				Valid:       true,
				Inputs: []workflowview.Input{
					{Name: "prompt", Type: "string", Required: true, MaxLength: 4096},
					{Name: "steps", Type: "integer"},
				},
			}}
		},
	})
	if err != nil {
		t.Fatalf("adminapi.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/workflows", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	// The view type has no field for a graph, a node or a field, so these are
	// assertions that the type has not grown one.
	//
	// 视图类型里没有图、节点或字段的位置，因此这些断言检验的是「该类型没有长出来一个」。
	for _, forbidden := range []string{"graph", "class_type", "\"node\"", "\"field\""} {
		if contains(body, forbidden) {
			t.Errorf("the catalogue rendered %q:\n%s", forbidden, body)
		}
	}

	var catalog workflowview.TemplateCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decoding the catalogue: %v", err)
	}
	if len(catalog.Templates) != 1 || catalog.Templates[0].ID != "portrait" {
		t.Fatalf("templates = %+v, want the one registered", catalog.Templates)
	}
	if len(catalog.Templates[0].Inputs) != 2 {
		t.Errorf("inputs = %+v, want both declared inputs", catalog.Templates[0].Inputs)
	}
}

// TestTheOptionalEndpointsAreAbsentWhenUnconfigured asserts a replica with no
// front door does not serve an empty job list. Reporting no runs and having no
// way to know are different answers.
//
// TestTheOptionalEndpointsAreAbsentWhenUnconfigured 断言一个没有前门的副本不会提供一份
// 空的 job 列表。「报告没有运行」与「根本无从知晓」是两个不同的答案。
func TestTheOptionalEndpointsAreAbsentWhenUnconfigured(t *testing.T) {
	handler := serve(t, nil)
	for _, path := range []string{"/internal/v1/jobs?tenant_id=tnt_1", "/internal/v1/workflows"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 when the source is not configured", path, rec.Code)
		}
	}
}
