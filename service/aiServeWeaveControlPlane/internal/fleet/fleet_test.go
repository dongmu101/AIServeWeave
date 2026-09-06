package fleet_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"testing"
	"time"

	"AIServeWeave/common/nodeview"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
)

// TestMain asserts no test in this package leaks a goroutine, per the README
// quality gate. The aggregator fans out to every replica, so a fan-out that
// forgot to wait would show up here rather than as a flake somewhere else.
//
// TestMain 断言本包没有测试泄漏协程，对应 README 的质量门禁。聚合器会向每个副本扇出，
// 因此一次忘了等待的扇出会在这里暴露，而不是在别处变成偶发失败。
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

// fixedClock is the injected clock, so collected_at in a failure message is
// recognizably the test's rather than today's.
//
// fixedClock 是注入的时钟，这样失败信息里的 collected_at 一望即知是测试的而不是今天的。
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
func (c fixedClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

const token = "operator-token-long-enough-for-a-test"

var (
	collectedAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	earlier     = collectedAt.Add(-2 * time.Minute)
	later       = collectedAt.Add(-1 * time.Minute)
)

// replica starts a stand-in Gateway serving one inventory document.
//
// replica 启动一个替身 Gateway，提供一份清单文档。
func replica(t *testing.T, doc nodeview.Replica) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/internal/v1/nodes" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func node(id string, live bool, heartbeat time.Time, models ...string) nodeview.Node {
	entry := nodeview.Node{NodeID: id, Live: live, LastHeartbeat: &heartbeat}
	if len(models) > 0 {
		runtimeModels := make([]nodeview.Model, 0, len(models))
		for _, model := range models {
			runtimeModels = append(runtimeModels, nodeview.Model{ID: model})
		}
		entry.Runtimes = []nodeview.Runtime{{
			ID:     "vllm-a",
			Kind:   "vllm",
			State:  "healthy",
			Models: runtimeModels,
		}}
	}
	return entry
}

func aggregate(t *testing.T, endpoints ...string) *fleet.Aggregator {
	t.Helper()
	return fleet.New(fleet.Config{
		Gateways: endpoints,
		Token:    token,
		Timeout:  2 * time.Second,
		Clock:    fixedClock{now: collectedAt},
	})
}

// TestNodesMergeAcrossReplicas is the property the aggregation exists for: one
// Agent connected to several replicas is one node, not three.
//
// TestNodesMergeAcrossReplicas 是本聚合存在的意义所在：一个连到多个副本的 Agent 是
// 一个节点，而不是三个。
func TestNodesMergeAcrossReplicas(t *testing.T) {
	first := replica(t, nodeview.Replica{
		ReplicaID:   "replica-1",
		GeneratedAt: earlier,
		Nodes:       []nodeview.Node{node("node-a", true, earlier), node("node-b", true, earlier)},
	})
	second := replica(t, nodeview.Replica{
		ReplicaID:   "replica-2",
		GeneratedAt: later,
		Nodes:       []nodeview.Node{node("node-a", true, later)},
	})

	snapshot, err := aggregate(t, first, second).Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}

	if len(snapshot.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2 (node-a merged)", len(snapshot.Nodes))
	}
	if snapshot.Nodes[0].NodeID != "node-a" || snapshot.Nodes[1].NodeID != "node-b" {
		t.Errorf("nodes = %q, %q, want them sorted by id",
			snapshot.Nodes[0].NodeID, snapshot.Nodes[1].NodeID)
	}
	// Both replicas reach node-a, and an operator deciding whether it is at
	// risk of being cut off needs to know that.
	//
	// 两个副本都能到达 node-a，而判断它是否有被切断风险的运维需要知道这一点。
	if len(snapshot.Nodes[0].Replicas) != 2 {
		t.Errorf("node-a replicas = %v, want both", snapshot.Nodes[0].Replicas)
	}
	// The fresher heartbeat won, so the view is the newer evidence.
	//
	// 更新的心跳胜出，因此展示的是更新的那份证据。
	if !snapshot.Nodes[0].ObservedAt.Equal(later) {
		t.Errorf("node-a observed at %v, want the fresher %v", snapshot.Nodes[0].ObservedAt, later)
	}
	if snapshot.Partial {
		t.Error("Partial is true although every replica answered")
	}
	if !snapshot.CollectedAt.Equal(collectedAt) {
		t.Errorf("CollectedAt = %v, want the injected %v", snapshot.CollectedAt, collectedAt)
	}
}

// TestALiveViewBeatsADeadOne asserts the merge prefers evidence that a node is
// reachable. A node whose tunnel to one replica died but is live on another is
// a live node, and a console that showed it as offline would send somebody to
// debug a machine that is working.
//
// TestALiveViewBeatsADeadOne 断言合并时优先采信「节点可达」的证据。一个到某副本的隧道
// 已断、但在另一副本上仍活着的节点，就是活的节点，而把它显示为离线的控制台，会把人派去
// 排查一台正常工作的机器。
func TestALiveViewBeatsADeadOne(t *testing.T) {
	dead := replica(t, nodeview.Replica{
		ReplicaID:   "replica-1",
		GeneratedAt: later,
		Nodes:       []nodeview.Node{node("node-a", false, later)},
	})
	alive := replica(t, nodeview.Replica{
		ReplicaID:   "replica-2",
		GeneratedAt: earlier,
		Nodes:       []nodeview.Node{node("node-a", true, earlier)},
	})

	snapshot, err := aggregate(t, dead, alive).Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(snapshot.Nodes) != 1 || !snapshot.Nodes[0].Live {
		t.Errorf("node-a = %+v, want one node reported live", snapshot.Nodes)
	}
}

// TestAReplicaThatDidNotAnswerIsNamed asserts a gap is reported rather than
// hidden. A shorter node list and a failed replica look identical to a
// console that is not told which happened.
//
// TestAReplicaThatDidNotAnswerIsNamed 断言缺口会被报告而不是被掩盖。对一个没有被告知
// 发生了哪一种情况的控制台来说，「节点列表变短」与「某个副本失败」长得一模一样。
func TestAReplicaThatDidNotAnswerIsNamed(t *testing.T) {
	working := replica(t, nodeview.Replica{
		ReplicaID:   "replica-1",
		GeneratedAt: later,
		Nodes:       []nodeview.Node{node("node-a", true, later)},
	})

	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(unauthorized.Close)

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{this is not json"))
	}))
	t.Cleanup(broken.Close)

	// A closed listener: the address is real and nothing answers on it.
	//
	// 一个已关闭的监听器：地址是真的，但那上面没有任何东西作答。
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	snapshot, err := aggregate(t, working, unauthorized.URL, broken.URL, goneURL).Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}

	if !snapshot.Partial {
		t.Error("Partial is false although three replicas failed")
	}
	if len(snapshot.Nodes) != 1 {
		t.Errorf("got %d nodes, want the one the working replica reported", len(snapshot.Nodes))
	}

	wantErrors := map[string]string{
		working:          "",
		unauthorized.URL: "unauthorized",
		broken.URL:       "malformed",
		goneURL:          "unreachable",
	}
	if len(snapshot.Replicas) != len(wantErrors) {
		t.Fatalf("got %d replica statuses, want %d", len(snapshot.Replicas), len(wantErrors))
	}
	for _, status := range snapshot.Replicas {
		want, known := wantErrors[status.Endpoint]
		if !known {
			t.Errorf("unexpected endpoint %q", status.Endpoint)
			continue
		}
		if status.Error != want {
			t.Errorf("%s: Error = %q, want %q", status.Endpoint, status.Error, want)
		}
	}
}

// TestFailuresNeverCarryTransportText asserts the error codes are a closed
// set. A transport message names hosts and ports of the internal network, and
// this document is on its way to a browser.
//
// TestFailuresNeverCarryTransportText 断言错误代号是一个封闭集合。传输层的报错会点出
// 内部网络的主机与端口，而这份文档正在前往浏览器的路上。
func TestFailuresNeverCarryTransportText(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	snapshot, err := aggregate(t, goneURL).Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshalling the snapshot: %v", err)
	}

	allowed := map[string]bool{"unreachable": true, "timeout": true, "unauthorized": true, "malformed": true}
	for _, status := range snapshot.Replicas {
		if !allowed[status.Error] {
			t.Errorf("Error = %q, want one of the four fixed codes", status.Error)
		}
	}
	for _, leak := range []string{"connection refused", "dial tcp", "EOF"} {
		if containsText(string(body), leak) {
			t.Errorf("the snapshot carried transport text %q:\n%s", leak, body)
		}
	}
}

// TestASlowReplicaDoesNotHoldTheAnswer asserts the timeout bounds one replica
// rather than the aggregation: the replicas that did answer are still served.
//
// TestASlowReplicaDoesNotHoldTheAnswer 断言超时限制的是单个副本而不是整次聚合：已经
// 作答的副本依然会被提供出来。
func TestASlowReplicaDoesNotHoldTheAnswer(t *testing.T) {
	working := replica(t, nodeview.Replica{
		ReplicaID:   "replica-1",
		GeneratedAt: later,
		Nodes:       []nodeview.Node{node("node-a", true, later)},
	})

	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(block)
		slow.Close()
	})

	aggregator := fleet.New(fleet.Config{
		Gateways: []string{working, slow.URL},
		Token:    token,
		Timeout:  100 * time.Millisecond,
		Clock:    fixedClock{now: collectedAt},
	})

	snapshot, err := aggregator.Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(snapshot.Nodes) != 1 {
		t.Errorf("got %d nodes, want the working replica's one", len(snapshot.Nodes))
	}
	if !snapshot.Partial {
		t.Error("Partial is false although one replica timed out")
	}
	for _, status := range snapshot.Replicas {
		if status.Endpoint == slow.URL && status.Error != "timeout" {
			t.Errorf("the slow replica reported %q, want timeout", status.Error)
		}
	}
}

// TestModelsAreDerivedFromTheSameRead covers the catalog: one model served in
// several places is one entry, and availability counts only what is reachable.
//
// TestModelsAreDerivedFromTheSameRead 覆盖目录：一个在多处提供的模型是一条记录，
// 而可用数只统计真正够得到的部分。
func TestModelsAreDerivedFromTheSameRead(t *testing.T) {
	first := replica(t, nodeview.Replica{
		ReplicaID:   "replica-1",
		GeneratedAt: later,
		Nodes: []nodeview.Node{
			node("node-a", true, later, "qwen-7b", "llama-3"),
			// node-b is not live, so its deployment of qwen-7b is listed but
			// not counted as available: a model on an unreachable node is not
			// capacity.
			//
			// node-b 不是活的，因此它上面 qwen-7b 的那处部署会被列出但不计入可用：
			// 位于不可达节点上的模型不构成容量。
			node("node-b", false, earlier, "qwen-7b"),
		},
	})

	catalog, err := aggregate(t, first).Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	if len(catalog.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(catalog.Models))
	}
	if catalog.Models[0].ID != "llama-3" || catalog.Models[1].ID != "qwen-7b" {
		t.Errorf("models = %q, %q, want them sorted", catalog.Models[0].ID, catalog.Models[1].ID)
	}

	qwen := catalog.Models[1]
	if len(qwen.Deployments) != 2 {
		t.Fatalf("qwen-7b has %d deployments, want 2", len(qwen.Deployments))
	}
	if qwen.AvailableDeployments != 1 {
		t.Errorf("qwen-7b available = %d, want 1 (node-b is not live)", qwen.AvailableDeployments)
	}
	if qwen.Deployments[0].Backend != "vllm" || qwen.Deployments[0].RuntimeID != "vllm-a" {
		t.Errorf("deployment = %+v, want the backend and runtime named", qwen.Deployments[0])
	}
	if !catalog.CollectedAt.Equal(collectedAt) {
		t.Errorf("CollectedAt = %v, want the injected %v", catalog.CollectedAt, collectedAt)
	}
}

// TestAnUnconfiguredAggregatorIsDisabled asserts the feature is absent rather
// than half present when no replica is configured.
//
// TestAnUnconfiguredAggregatorIsDisabled 断言在没有配置任何副本时，该功能是不存在的，
// 而不是半存在的。
func TestAnUnconfiguredAggregatorIsDisabled(t *testing.T) {
	aggregator := fleet.New(fleet.Config{})
	if aggregator != nil {
		t.Fatal("New() returned an aggregator for an empty configuration")
	}
	if _, err := aggregator.Nodes(context.Background()); err != fleet.ErrDisabled {
		t.Errorf("Nodes() error = %v, want ErrDisabled", err)
	}
	if _, err := aggregator.Models(context.Background()); err != fleet.ErrDisabled {
		t.Errorf("Models() error = %v, want ErrDisabled", err)
	}
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestAnErrorAnsweredWithTwoHundredIsNotAnEmptyReplica is the assertion that
// decoding is not validation.
//
// Every body here decodes without error and leaves a zero-valued document, so
// a check that only asked "did JSON parse" would report each of them as a
// healthy replica holding nothing — and would clear the partial flag that is
// the only thing telling an operator the picture is incomplete. During an
// incident that is the worst available answer: it says the fleet is empty
// rather than that the fleet cannot be seen.
//
// TestAnErrorAnsweredWithTwoHundredIsNotAnEmptyReplica 断言「能解码」不等于「已校验」。
//
// 这里每一种响应体都能无错解码，并留下一个零值文档，因此一项只问「JSON 解析通过了吗」
// 的检查，会把它们每一个都报告成一个健康且什么都没有的副本——还会清掉那个唯一能告诉运维
// 「这幅图不完整」的 partial 标志。在事故中那是最糟的答案：它说的是机群空了，而不是
// 机群看不见了。
func TestAnErrorAnsweredWithTwoHundredIsNotAnEmptyReplica(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "an empty object", body: `{}`},
		{name: "a JSON null", body: `null`},
		{name: "an error document served with 200", body: `{"error":"unavailable"}`},
		{name: "the right shape with no replica id", body: `{"generated_at":"2026-01-01T12:00:00Z","nodes":[]}`},
		{name: "the right shape with no timestamp", body: `{"replica_id":"replica-1","nodes":[]}`},
		{name: "no node list at all", body: `{"replica_id":"replica-1","generated_at":"2026-01-01T12:00:00Z"}`},
		{name: "a null node list", body: `{"replica_id":"replica-1","generated_at":"2026-01-01T12:00:00Z","nodes":null}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			snapshot, err := aggregate(t, server.URL).Nodes(context.Background())
			if err != nil {
				t.Fatalf("Nodes: %v", err)
			}
			if !snapshot.Partial {
				t.Error("Partial is false; this replica did not answer usefully and the answer says it did")
			}
			if len(snapshot.Replicas) != 1 || snapshot.Replicas[0].Error != "malformed" {
				t.Errorf("replicas = %+v, want the reply reported as malformed", snapshot.Replicas)
			}
			if snapshot.Replicas[0].NodeCount != 0 {
				t.Errorf("NodeCount = %d, want none counted from a refused reply", snapshot.Replicas[0].NodeCount)
			}
		})
	}
}

// TestAGenuinelyEmptyReplicaIsNotMalformed is the other half: a replica that
// answered honestly and holds nothing must still be a success. Validation that
// refused it would trade one wrong answer for another.
//
// TestAGenuinelyEmptyReplicaIsNotMalformed 是另一半：一个如实作答、确实什么都没有的
// 副本，仍然必须算成功。会拒绝它的校验，只是把一个错误答案换成了另一个。
func TestAGenuinelyEmptyReplicaIsNotMalformed(t *testing.T) {
	empty := replica(t, nodeview.Replica{
		ReplicaID:   "replica-1",
		GeneratedAt: later,
		Nodes:       []nodeview.Node{},
	})

	snapshot, err := aggregate(t, empty).Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if snapshot.Partial {
		t.Error("Partial is true although the replica answered")
	}
	if len(snapshot.Replicas) != 1 || snapshot.Replicas[0].Error != "" {
		t.Errorf("replicas = %+v, want one successful reply", snapshot.Replicas)
	}
	if len(snapshot.Nodes) != 0 {
		t.Errorf("got %d nodes, want none", len(snapshot.Nodes))
	}
}
