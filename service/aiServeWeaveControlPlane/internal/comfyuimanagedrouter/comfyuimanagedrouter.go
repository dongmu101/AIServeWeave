// Package comfyuimanagedrouter forwards STATUS.md's P2 ComfyUI Managed
// Docker deployment subtask two's per-node lifecycle action trigger and
// status calls to whichever Gateway replica currently holds that node_id's
// connection.
//
// It is the same shape as internal/modelpullrouter, deliberately not shared
// code with it: a node's Managed instance is reached through a different
// Gateway listener (-comfyui-managed-addr, not -model-pull-addr) with its
// own token, and the wire request/response bodies differ (a single closed
// Action instead of a list of names, container instances instead of named
// pulls). Duplicating the small, symmetric fan-out shape costs less than
// threading a generic version of it through both call sites' differing wire
// formats.
//
// comfyuimanagedrouter 包把 STATUS.md P2 ComfyUI Managed Docker 部署子任务二
// 里针对单个节点的生命周期动作触发与状态查询，转发给当前持有该 node_id 连接
// 的那个 Gateway 副本。
//
// 它与 internal/modelpullrouter 形状相同，但刻意不与它共用代码：一个节点的
// Managed 实例经由另一个 Gateway 监听器（-comfyui-managed-addr，不是
// -model-pull-addr）到达，用的是自己的 token，且请求/响应的线上格式不同（一个
// 封闭的 Action 而不是一组名字，容器实例而不是命名拉取）。为这套小巧、对称的
// 扇出形状穿一层泛化，代价比在两处不同的线上格式之间各自复制一份更高。
package comfyuimanagedrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"AIServeWeave/common/comfyuimanagedstatus"
)

// ErrDisabled is returned when ComfyUI Managed forwarding was not
// configured — the answer is a deployment change, not a retry, the same
// reasoning modelpullrouter.ErrDisabled documents.
//
// ErrDisabled 在 ComfyUI Managed 转发未配置时返回——对它的应对是改部署，而
// 不是重试，与 modelpullrouter.ErrDisabled 同一理由。
var ErrDisabled = errors.New("comfyuimanagedrouter: comfyui managed forwarding is not configured")

// DefaultTimeout bounds one call to one replica.
//
// DefaultTimeout 限制对单个副本的单次调用。
const DefaultTimeout = 3 * time.Second

// maxResponseBytes bounds one replica's status response: an Agent runs at
// most one Managed instance, so this document is a single small object.
//
// maxResponseBytes 限制单个副本状态响应的大小：一个 Agent 最多运行一个
// Managed 实例，因此这份文档只是一个很小的单一对象。
const maxResponseBytes = 8 << 10

// Config configures a Router.
//
// Config 配置一个 Router。
type Config struct {
	// Gateways are the base URLs of each Gateway replica's
	// -comfyui-managed-addr listener, e.g. http://gateway-1:8093. A replica
	// not listed here is simply never asked.
	//
	// Gateways 是各 Gateway 副本 -comfyui-managed-addr 监听器的基础 URL，
	// 例如 http://gateway-1:8093。没有列在这里的副本不会被询问。
	Gateways []string
	// Token authenticates this service to those listeners. It must match
	// each Gateway's AISW_GATEWAY_COMFYUI_MANAGED_TOKEN.
	//
	// Token 用于本服务向那些监听器表明身份。它必须与各 Gateway 的
	// AISW_GATEWAY_COMFYUI_MANAGED_TOKEN 一致。
	Token string
	// Timeout bounds one call to one replica. Every configured replica is
	// asked concurrently, so a slow one delays only itself.
	//
	// Timeout 限制对单个副本的单次调用。每个已配置副本都被并发询问，因此
	// 一个慢副本只会拖延它自己。
	Timeout time.Duration
	Client  *http.Client
}

// Router asks every configured replica and reports what each one said.
//
// Router 向每个已配置的副本发问，并报告每一个副本各自说了什么。
type Router struct {
	gateways []string
	token    string
	client   *http.Client
}

// New builds a Router, or nil when no replicas were configured — every
// method on a nil *Router reports ErrDisabled, the same "no half-built
// object" rule modelpullrouter.New documents.
//
// New 构造一个 Router；未配置任何副本时返回 nil——nil *Router 上的每个方法
// 都会报告 ErrDisabled，与 modelpullrouter.New 记录的"没有半成品对象"规则
// 相同。
func New(cfg Config) *Router {
	if len(cfg.Gateways) == 0 {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Router{gateways: cfg.Gateways, token: cfg.Token, client: client}
}

// ReplicaStatus is what happened when one configured replica was asked, the
// same shape and error vocabulary as modelpullrouter.ReplicaStatus.
//
// ReplicaStatus 是询问某个已配置副本时发生的事，形状与错误代号词表与
// modelpullrouter.ReplicaStatus 相同。
type ReplicaStatus struct {
	Endpoint  string `json:"endpoint"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

// InstanceStatus is the one Managed instance's status, as reported by
// whichever replica answered for it.
//
// InstanceStatus 是那一个 Managed 实例的状态，由为它作答的那个副本报告。
type InstanceStatus struct {
	ContainerName string
	State         string
	UpdatedAt     time.Time
	// CustomNodes is every custom node this instance reports installed
	// (STATUS.md's P2 ComfyUI Managed Docker deployment, subtask 4).
	//
	// CustomNodes 是该实例上报的每一个已安装自定义节点（STATUS.md 的 P2
	// ComfyUI Managed Docker 部署子任务四）。
	CustomNodes []CustomNodeStatus
}

// CustomNodeStatus mirrors comfyuimanagedstatus.CustomNodeStatus on the wire
// this router speaks — its own type rather than a direct reuse, the same
// "each layer decodes into its own shape" precedent ReplicaStatus already
// follows relative to comfyuimanagedapi's wire types.
//
// CustomNodeStatus 与 comfyuimanagedstatus.CustomNodeStatus 在本 router 所讲
// 的线上格式里同构——是自己的类型而不是直接复用，与 ReplicaStatus 相对
// comfyuimanagedapi 线上类型已有的"每一层解码成自己的形状"先例相同。
type CustomNodeStatus struct {
	Name    string
	Version string
}

// Result is the answer to Trigger or Status.
//
// Result 是 Trigger 或 Status 的答案。
type Result struct {
	// Connected is true when at least one configured replica reported
	// node_id connected to it.
	//
	// Connected 在至少一个已配置副本报告 node_id 连到它时为 true。
	Connected bool
	// Instances is Status's payload: the reply from whichever connected
	// replica carries the most recently updated entry — the same "most
	// recent evidence wins" rule modelpullrouter.Result.Pulls documents.
	// Always nil for Trigger.
	//
	// Instances 是 Status 的负载：取自已连接副本中，携带最近一次更新条目
	// 的那份回复——与 modelpullrouter.Result.Pulls 同一条"最新证据胜出"
	// 规则。Trigger 时恒为 nil。
	Instances []InstanceStatus
	// Replicas is what happened at every configured replica, in configured
	// order.
	//
	// Replicas 是在每个已配置副本上发生的事，按配置顺序排列。
	Replicas []ReplicaStatus
}

// Trigger asks node_id, wherever it is connected among the configured
// replicas, to apply action to its one locally-declared Managed instance.
// It never carries an image, mount path, or any other Spec field — only the
// closed Action, a Gateway replica forwards verbatim to the Agent's own
// Control stream (tunnelserver.Server.TriggerComfyUIManagedAction).
//
// Trigger 要求 node_id（无论它连在哪个已配置副本上）对它本地已声明的那一个
// Managed 实例施加 action。它从不携带镜像、挂载路径或任何其他 Spec
// 字段——只有这个封闭的 Action，由某个 Gateway 副本原样转发进 Agent 自己的
// Control 流（tunnelserver.Server.TriggerComfyUIManagedAction）。
func (r *Router) Trigger(ctx context.Context, nodeID string, action comfyuimanagedstatus.Action) (Result, error) {
	if r == nil {
		return Result{}, ErrDisabled
	}
	payload, err := json.Marshal(triggerRequestWire{Action: action.String()})
	if err != nil {
		return Result{}, fmt.Errorf("comfyuimanagedrouter: encoding trigger request: %w", err)
	}
	outcomes := r.fanOut(ctx, func(ctx context.Context, endpoint string) replicaOutcome {
		return r.triggerOne(ctx, endpoint, nodeID, payload)
	})
	return buildResult(outcomes), nil
}

// InstallCustomNode asks node_id, wherever it is connected among the
// configured replicas, to install name into its one locally-declared
// Managed instance's custom-nodes directory (STATUS.md's P2 ComfyUI Managed
// Docker deployment, subtask 4). It never carries a repository URL — only
// name, which a Gateway replica forwards verbatim to the Agent's own
// Control stream, where the Agent's own local allowlist decides whether it
// is known.
//
// InstallCustomNode 要求 node_id（无论它连在哪个已配置副本上）把 name 安装
// 进它本地已声明的那一个 Managed 实例的自定义节点目录（STATUS.md 的 P2
// ComfyUI Managed Docker 部署子任务四）。它从不携带仓库 URL——只有
// name，由某个 Gateway 副本原样转发进 Agent 自己的 Control 流，是否已知由
// Agent 自己本地的允许列表决定。
func (r *Router) InstallCustomNode(ctx context.Context, nodeID, name string) (Result, error) {
	if r == nil {
		return Result{}, ErrDisabled
	}
	payload, err := json.Marshal(customNodeInstallRequestWire{Name: name})
	if err != nil {
		return Result{}, fmt.Errorf("comfyuimanagedrouter: encoding custom node install request: %w", err)
	}
	outcomes := r.fanOut(ctx, func(ctx context.Context, endpoint string) replicaOutcome {
		return r.installCustomNodeOne(ctx, endpoint, nodeID, payload)
	})
	return buildResult(outcomes), nil
}

// Status returns node_id's Managed instance status, as reported by
// whichever configured replica currently holds its connection.
//
// Status 返回 node_id 的 Managed 实例状态，由当前持有其连接的已配置副本
// 报告。
func (r *Router) Status(ctx context.Context, nodeID string) (Result, error) {
	if r == nil {
		return Result{}, ErrDisabled
	}
	outcomes := r.fanOut(ctx, func(ctx context.Context, endpoint string) replicaOutcome {
		return r.statusOne(ctx, endpoint, nodeID)
	})
	return buildResult(outcomes), nil
}

// replicaOutcome is one replica's answer to either Trigger or Status,
// before it is folded into a Result.
//
// replicaOutcome 是单个副本对 Trigger 或 Status 的作答，在被折进 Result
// 之前的形态。
type replicaOutcome struct {
	endpoint   string
	connected  bool
	failure    string
	instances  []InstanceStatus
	freshestAt time.Time
}

// fanOut asks every configured replica concurrently via ask, preserving
// configured order in the returned slice regardless of which goroutine
// finishes first, the same race-free pattern modelpullrouter.Router.fanOut
// uses.
//
// fanOut 通过 ask 并发询问每个已配置副本，无论哪个 goroutine 先完成，返回的
// slice 都保持配置顺序，与 modelpullrouter.Router.fanOut 同一种无竞争写法。
func (r *Router) fanOut(ctx context.Context, ask func(ctx context.Context, endpoint string) replicaOutcome) []replicaOutcome {
	outcomes := make([]replicaOutcome, len(r.gateways))
	var wg sync.WaitGroup
	for i, endpoint := range r.gateways {
		wg.Add(1)
		go func(i int, endpoint string) {
			defer wg.Done()
			outcomes[i] = ask(ctx, endpoint)
		}(i, endpoint)
	}
	wg.Wait()
	return outcomes
}

// triggerRequestWire mirrors comfyuimanagedapi's triggerRequest.
//
// triggerRequestWire 与 comfyuimanagedapi 的 triggerRequest 形状一致。
type triggerRequestWire struct {
	Action string `json:"action"`
}

// triggerOne posts to one replica's ComfyUI Managed listener.
//
// triggerOne 向单个副本的 ComfyUI Managed 监听器发起 POST。
func (r *Router) triggerOne(ctx context.Context, endpoint, nodeID string, payload []byte) replicaOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+comfyUIManagedPath(nodeID), bytes.NewReader(payload))
	if err != nil {
		return replicaOutcome{endpoint: endpoint, failure: "unreachable"}
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return replicaOutcome{endpoint: endpoint, failure: classifyTransportErr(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	drain(resp.Body)

	switch resp.StatusCode {
	case http.StatusAccepted:
		return replicaOutcome{endpoint: endpoint, connected: true}
	case http.StatusNotFound:
		return replicaOutcome{endpoint: endpoint}
	case http.StatusUnauthorized:
		return replicaOutcome{endpoint: endpoint, failure: "unauthorized"}
	default:
		return replicaOutcome{endpoint: endpoint, failure: "unreachable"}
	}
}

// customNodeInstallRequestWire mirrors comfyuimanagedapi's
// customNodeInstallRequest.
//
// customNodeInstallRequestWire 与 comfyuimanagedapi 的 customNodeInstallRequest
// 形状一致。
type customNodeInstallRequestWire struct {
	Name string `json:"name"`
}

// installCustomNodeOne posts to one replica's ComfyUI Managed custom-node
// install endpoint, logically identical to triggerOne except for the path
// and payload shape.
//
// installCustomNodeOne 向单个副本的 ComfyUI Managed 自定义节点安装端点发起
// POST，除了路径与负载形状之外，与 triggerOne 逻辑相同。
func (r *Router) installCustomNodeOne(ctx context.Context, endpoint, nodeID string, payload []byte) replicaOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+comfyUIManagedPath(nodeID)+"/custom-nodes", bytes.NewReader(payload))
	if err != nil {
		return replicaOutcome{endpoint: endpoint, failure: "unreachable"}
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return replicaOutcome{endpoint: endpoint, failure: classifyTransportErr(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	drain(resp.Body)

	switch resp.StatusCode {
	case http.StatusAccepted:
		return replicaOutcome{endpoint: endpoint, connected: true}
	case http.StatusNotFound:
		return replicaOutcome{endpoint: endpoint}
	case http.StatusUnauthorized:
		return replicaOutcome{endpoint: endpoint, failure: "unauthorized"}
	default:
		return replicaOutcome{endpoint: endpoint, failure: "unreachable"}
	}
}

// wireStatusResponse mirrors comfyuimanagedapi's statusResponse.
//
// wireStatusResponse 与 comfyuimanagedapi 的 statusResponse 形状一致。
type wireStatusResponse struct {
	GeneratedAt string               `json:"generated_at"`
	Instances   []wireInstanceStatus `json:"instances"`
}

// wireInstanceStatus mirrors comfyuimanagedapi's instanceStatusJSON.
//
// wireInstanceStatus 与 comfyuimanagedapi 的 instanceStatusJSON 形状一致。
type wireInstanceStatus struct {
	ContainerName string           `json:"container_name"`
	State         string           `json:"state"`
	UpdatedAt     string           `json:"updated_at"`
	CustomNodes   []wireCustomNode `json:"custom_nodes"`
}

// wireCustomNode mirrors comfyuimanagedapi's customNodeJSON.
//
// wireCustomNode 与 comfyuimanagedapi 的 customNodeJSON 形状一致。
type wireCustomNode struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// statusOne reads one replica's ComfyUI Managed listener.
//
// statusOne 读取单个副本的 ComfyUI Managed 监听器。
func (r *Router) statusOne(ctx context.Context, endpoint, nodeID string) replicaOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+comfyUIManagedPath(nodeID), nil)
	if err != nil {
		return replicaOutcome{endpoint: endpoint, failure: "unreachable"}
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return replicaOutcome{endpoint: endpoint, failure: classifyTransportErr(err)}
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		var wire wireStatusResponse
		if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, maxResponseBytes)).Decode(&wire); err != nil {
			return replicaOutcome{endpoint: endpoint, failure: "malformed"}
		}
		instances, freshestAt := renderInstances(wire)
		return replicaOutcome{endpoint: endpoint, connected: true, instances: instances, freshestAt: freshestAt}
	case http.StatusNotFound:
		drain(resp.Body)
		return replicaOutcome{endpoint: endpoint}
	case http.StatusUnauthorized:
		return replicaOutcome{endpoint: endpoint, failure: "unauthorized"}
	default:
		return replicaOutcome{endpoint: endpoint, failure: "unreachable"}
	}
}

// renderInstances converts wire's instances into InstanceStatus, and
// reports the latest UpdatedAt among them — the zero time when there are
// none, which never wins a freshness comparison against a replica that did
// report something, the same reasoning modelpullrouter.renderPulls
// documents.
//
// renderInstances 把 wire 的 instances 转换成 InstanceStatus，并报告其中
// 最新的 UpdatedAt——不存在任何条目时为零值时间，永远不会在新鲜度比较中
// 胜过一个确实报告了内容的副本，与 modelpullrouter.renderPulls 同一理由。
func renderInstances(wire wireStatusResponse) ([]InstanceStatus, time.Time) {
	instances := make([]InstanceStatus, len(wire.Instances))
	var freshest time.Time
	for i, inst := range wire.Instances {
		updatedAt, _ := time.Parse(time.RFC3339, inst.UpdatedAt)
		customNodes := make([]CustomNodeStatus, len(inst.CustomNodes))
		for j, cn := range inst.CustomNodes {
			customNodes[j] = CustomNodeStatus{Name: cn.Name, Version: cn.Version}
		}
		instances[i] = InstanceStatus{ContainerName: inst.ContainerName, State: inst.State, UpdatedAt: updatedAt, CustomNodes: customNodes}
		if updatedAt.After(freshest) {
			freshest = updatedAt
		}
	}
	return instances, freshest
}

// buildResult folds every replica's outcome into a Result, the same
// reasoning modelpullrouter.buildResult documents.
//
// buildResult 把每个副本的结果折进一个 Result，与 modelpullrouter.buildResult
// 同一理由。
func buildResult(outcomes []replicaOutcome) Result {
	result := Result{Replicas: make([]ReplicaStatus, len(outcomes))}
	var freshestAt time.Time
	haveFreshest := false
	for i, o := range outcomes {
		result.Replicas[i] = ReplicaStatus{Endpoint: o.endpoint, Connected: o.connected, Error: o.failure}
		if !o.connected {
			continue
		}
		result.Connected = true
		if !haveFreshest || o.freshestAt.After(freshestAt) {
			result.Instances = o.instances
			freshestAt = o.freshestAt
			haveFreshest = true
		}
	}
	return result
}

// comfyUIManagedPath builds the per-node path both triggerOne and statusOne
// hit, matching comfyuimanagedapi's mounted route exactly.
//
// comfyUIManagedPath 构造 triggerOne 与 statusOne 共用的按节点路径，与
// comfyuimanagedapi 挂载的路由完全一致。
func comfyUIManagedPath(nodeID string) string {
	return "/internal/v1/nodes/" + url.PathEscape(nodeID) + "/comfyui-managed"
}

// classifyTransportErr turns a client-side error into one of the fixed
// codes ReplicaStatus.Error documents.
//
// classifyTransportErr 把一个客户端错误归入 ReplicaStatus.Error 文档所列的
// 某个固定代号。
func classifyTransportErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return "timeout"
	}
	return "unreachable"
}

// isTimeout reports whether err is a deadline rather than a refusal.
//
// isTimeout 报告 err 是超时而不是被拒绝。
func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// drain discards the rest of a response body so its connection can be
// reused.
//
// drain 丢弃响应体的剩余部分，好让连接可以被复用。
func drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseBytes))
}
