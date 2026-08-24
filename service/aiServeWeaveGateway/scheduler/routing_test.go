package scheduler_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// routes writes a routing table and loads it, the same way the Gateway loads
// -model-routes at startup.
//
// routes 写出一份路由表并加载它，与 Gateway 启动时加载 -model-routes 的路径一致。
func routes(t *testing.T, rs ...routing.Route) *routing.Table {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "r.json"), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	table, err := routing.Load(dir)
	if err != nil {
		t.Fatalf("routing.Load: %v", err)
	}
	return table
}

// connectLabelledNode is connectNode plus the operator-assigned labels the
// routing rules select on.
//
// connectLabelledNode 是 connectNode 再加上路由规则据以选择的、由运维赋予的标签。
func connectLabelledNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID string, labels map[string]string, snap runtime.Snapshot, handlers ...gatewaytest.SlotHandler) {
	t.Helper()
	h.ConnectWithLabels(t, nodeID, runtimeID, labels, snap, handlers...)
}

// echoModelHandler answers Chat with the model id it actually received, which
// is how these tests assert the alias was rewritten before it left the
// Gateway.
//
// echoModelHandler 用它实际收到的模型 id 应答 Chat，这正是这些测试断言「别名在离开
// Gateway 之前已被改写」的方式。
func echoModelHandler(source string, count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload())
		if err != nil {
			return err
		}
		payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
			ID:      "chat-1",
			Model:   in.Model,
			Message: runtime.ChatMessage{Role: "assistant", Content: source + " served " + in.Model},
		})
		if err != nil {
			return err
		}
		return reply(gatewaytest.DataFrame(payload))
	}
}

// TestAliasIsRewrittenToTheRuntimeModel is the whole point of the abstraction:
// the client never learns which real model answered it, so the operator can
// change that without the client changing anything.
//
// TestAliasIsRewrittenToTheRuntimeModel 正是这层抽象的全部意义：客户端始终不知道是
// 哪个真实模型回答了它，因此运维可以改动那一点而客户端无需改动任何东西。
func TestAliasIsRewrittenToTheRuntimeModel(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3-coder:30b"), echoModelHandler("node-a", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock,
		Routes: routes(t, routing.Route{
			Model:   "qwen-coder",
			Targets: []routing.Target{{RuntimeModel: "qwen3-coder:30b"}},
		}),
	})

	resp, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen-coder"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "node-a served qwen3-coder:30b" {
		t.Errorf("the node saw %q, want the alias rewritten to qwen3-coder:30b", resp.Message.Content)
	}
}

// TestAnUnroutedModelIsUsedAsIs keeps the routing table optional. A deployment
// that never writes one must go on serving the model ids its nodes advertise.
//
// TestAnUnroutedModelIsUsedAsIs 让路由表保持可选。从不写路由表的部署，必须继续服务
// 它的节点所声明的那些模型 id。
func TestAnUnroutedModelIsUsedAsIs(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoModelHandler("node-a", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, Routes: routes(t)})
	resp, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "node-a served qwen3:8b" {
		t.Errorf("content = %q, want the model passed through untouched", resp.Message.Content)
	}
}

// TestNodeSelectorPicksTheLabelledNode is README's 节点标签路由, e.g. region=local.
//
// TestNodeSelectorPicksTheLabelledNode 是 README 的「节点标签路由，例如 region=local」。
func TestNodeSelectorPicksTheLabelledNode(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var localCount, remoteCount atomic.Int32
	// The remote node has more idle slots, so load-based ordering alone would
	// pick it. The selector is what keeps the request local.
	//
	// 远端节点空闲槽更多，因此单靠基于负载的排序会选中它。是选择器把请求留在了本地。
	connectLabelledNode(t, h, "node-remote", "backend-1", map[string]string{"region": "remote"},
		chatCapableSnapshot("backend-1", "qwen3:8b"),
		echoModelHandler("node-remote", &remoteCount), echoModelHandler("node-remote", &remoteCount))
	connectLabelledNode(t, h, "node-local", "backend-1", map[string]string{"region": "local"},
		chatCapableSnapshot("backend-1", "qwen3:8b"), echoModelHandler("node-local", &localCount))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock,
		Routes: routes(t, routing.Route{
			Model: "qwen-coder",
			Targets: []routing.Target{
				{RuntimeModel: "qwen3:8b", NodeSelector: map[string]string{"region": "local"}},
			},
		}),
	})

	resp, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen-coder"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if candidate.NodeID != "node-local" {
		t.Errorf("candidate = %q, want node-local despite it having fewer idle slots", candidate.NodeID)
	}
	if remoteCount.Load() != 0 {
		t.Errorf("the remote node was contacted %d times, want 0", remoteCount.Load())
	}
	_ = resp
}

// TestPriorityIsTriedBeforeLoad covers the operator's stated preference
// beating the load heuristic: "the local Mac before the rented GPU" has to
// mean that even when the rented GPU is more idle.
//
// TestPriorityIsTriedBeforeLoad 覆盖「运维声明的偏好压过负载启发式」：「先用本地那台
// Mac，再用租来的 GPU」必须在租来的 GPU 更空闲时依然成立。
func TestPriorityIsTriedBeforeLoad(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var firstCount, secondCount atomic.Int32
	connectLabelledNode(t, h, "node-big", "backend-1", map[string]string{"tier": "big"},
		chatCapableSnapshot("backend-1", "qwen3:8b"),
		echoModelHandler("node-big", &secondCount), echoModelHandler("node-big", &secondCount))
	connectLabelledNode(t, h, "node-small", "backend-1", map[string]string{"tier": "small"},
		chatCapableSnapshot("backend-1", "qwen3:8b"), echoModelHandler("node-small", &firstCount))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock,
		Routes: routes(t, routing.Route{
			Model: "alias",
			Targets: []routing.Target{
				{RuntimeModel: "qwen3:8b", Priority: 1, NodeSelector: map[string]string{"tier": "small"}},
				{RuntimeModel: "qwen3:8b", Priority: 2, NodeSelector: map[string]string{"tier": "big"}},
			},
		}),
	})

	_, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "alias"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if candidate.NodeID != "node-small" {
		t.Errorf("candidate = %q, want node-small (priority 1) even though node-big is more idle", candidate.NodeID)
	}
	if secondCount.Load() != 0 {
		t.Errorf("the lower-priority node was contacted %d times, want 0", secondCount.Load())
	}
}

// TestLowerPriorityIsTheFallback confirms priority orders rather than
// excludes: a first choice that cannot serve the request must not strand it.
//
// TestLowerPriorityIsTheFallback 确认优先级是排序而不是排除：无法服务该请求的首选，
// 不能把请求困死在那里。
func TestLowerPriorityIsTheFallback(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	// Only the big tier is connected; the priority-1 selector matches nothing.
	//
	// 只有 big 这一档连着；优先级 1 的选择器什么都匹配不到。
	connectLabelledNode(t, h, "node-big", "backend-1", map[string]string{"tier": "big"},
		chatCapableSnapshot("backend-1", "qwen3:8b"), echoModelHandler("node-big", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock,
		Routes: routes(t, routing.Route{
			Model: "alias",
			Targets: []routing.Target{
				{RuntimeModel: "qwen3:8b", Priority: 1, NodeSelector: map[string]string{"tier": "small"}},
				{RuntimeModel: "qwen3:8b", Priority: 2, NodeSelector: map[string]string{"tier": "big"}},
			},
		}),
	})

	_, candidate, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "alias"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if candidate.NodeID != "node-big" {
		t.Errorf("candidate = %q, want the priority-2 fallback", candidate.NodeID)
	}
}

// TestModelsListsAliasesInsteadOfRuntimeModels keeps the public catalogue in
// the vocabulary clients are told to use. Advertising the real model ids
// alongside would invite clients to bind to them, which is exactly what the
// alias exists to prevent.
//
// TestModelsListsAliasesInsteadOfRuntimeModels 让公开目录停留在「告诉客户端使用的」
// 那套词汇里。把真实模型 id 一并公布，等于邀请客户端绑定到它们，而那正是别名存在所要
// 防止的事。
func TestModelsListsAliasesInsteadOfRuntimeModels(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3-coder:30b"), echoModelHandler("node-a", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock,
		Routes: routes(t, routing.Route{
			Model:   "qwen-coder",
			Targets: []routing.Target{{RuntimeModel: "qwen3-coder:30b"}},
		}),
	})

	models := sched.Models(context.Background())
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if len(ids) != 1 || ids[0] != "qwen-coder" {
		t.Errorf("Models() = %v, want just the alias qwen-coder", ids)
	}
}
