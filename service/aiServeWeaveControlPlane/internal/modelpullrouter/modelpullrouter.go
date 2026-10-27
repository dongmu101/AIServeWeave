// Package modelpullrouter forwards STATUS.md's P2 model distribution
// subtask two's per-node pull trigger and status calls to whichever Gateway
// replica currently holds that node_id's connection.
//
// Unlike internal/fleet.Aggregator, which merges a full inventory read from
// every replica into one document, one node_id's model-pull request belongs
// to at most a handful of replicas — the Agent maintains simultaneous
// connections to several for redundancy (see the root README's "多副本连接"),
// and each Gateway replica only knows the nodes connected to itself (the
// tunnel design's third constraint, restated in internal/fleet's own doc
// comment). This package does not try to look up which replicas those are
// in advance: it asks every configured replica's -model-pull-addr listener
// directly, node_id already in the request path, and lets each one answer
// "not connected here" (404) for itself in the same round trip. That avoids
// threading a second, model-pull-addr-keyed endpoint list through
// fleet.Aggregator's own admin-addr bookkeeping just to reconstruct a
// mapping this package can establish on its own.
//
// A trigger is fanned out to every configured replica concurrently rather
// than aimed at a single discovered one. The Agent-side Puller.Trigger is
// idempotent per name (STATUS.md's P2 subtask 2 design doc §四), so a node
// connected to more than one configured replica receiving the same trigger
// twice is harmless — and fanning out means this package never needs a
// separate lookup step before it can act.
//
// modelpullrouter 包把 STATUS.md P2 模型分发子任务二里针对单个节点的拉取触发
// 与状态查询，转发给当前持有该 node_id 连接的那个 Gateway 副本。
//
// 与 internal/fleet.Aggregator（把每个副本的完整清单合并成一份文档）不同，
// 一个 node_id 的模型拉取请求最多只属于少数几个副本——Agent 为了冗余会同时
// 维持到多个副本的连接（见根 README「多副本连接」），而每个 Gateway 副本只
// 知道连到它自己身上的节点（隧道设计的第三条约束，internal/fleet 自己的文档
// 注释里也重申过）。本包不会预先查找它们是谁：它直接向每个已配置副本的
// -model-pull-addr 监听器发问，node_id 已经在请求路径里，让每个副本在同一次
// 往返里各自回答"没有连到我这里"（404）。这样就不必再为了重建这份映射，把
// 第二份按 model-pull-addr 索引的 endpoint 列表，穿进 fleet.Aggregator 自己那套
// 按 admin-addr 索引的账本。
//
// 一次触发会并发地下发给每一个已配置副本，而不是瞄准某一个事先找到的副本。
// Agent 侧的 Puller.Trigger 对同一个名字是幂等的（STATUS.md P2 子任务二设计
// 文档§四），因此一个连到多个已配置副本的节点收到两次同样的触发是无害的——
// 并发下发也让本包完全不需要在动作之前先做一次单独的查找。
package modelpullrouter

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
)

// ErrDisabled is returned when model-pull forwarding was not configured. It
// is a distinct error because the answer to it is a deployment change, not
// a retry — the same reasoning as fleet.ErrDisabled.
//
// ErrDisabled 在模型拉取转发未配置时返回。它是一个独立的错误，因为对它的应对
// 是改部署，而不是重试——与 fleet.ErrDisabled 同一理由。
var ErrDisabled = errors.New("modelpullrouter: model pull forwarding is not configured")

// DefaultTimeout bounds one call to one replica.
//
// DefaultTimeout 限制对单个副本的单次调用。
const DefaultTimeout = 3 * time.Second

// maxResponseBytes bounds one replica's status response. The document is a
// handful of named pulls from one operator-authored manifest, comfortably
// under this.
//
// maxResponseBytes 限制单个副本状态响应的大小。这份文档只是一份运维手写清单
// 里少数几个命名拉取的状态，远在这个上限之内。
const maxResponseBytes = 64 << 10

// Config configures a Router.
//
// Config 配置一个 Router。
type Config struct {
	// Gateways are the base URLs of each Gateway replica's -model-pull-addr
	// listener, e.g. http://gateway-1:8092. A replica not listed here is
	// simply never asked.
	//
	// Gateways 是各 Gateway 副本 -model-pull-addr 监听器的基础 URL，例如
	// http://gateway-1:8092。没有列在这里的副本不会被询问。
	Gateways []string
	// Token authenticates this service to those listeners. It must match
	// each Gateway's AISW_GATEWAY_MODEL_PULL_TOKEN.
	//
	// Token 用于本服务向那些监听器表明身份。它必须与各 Gateway 的
	// AISW_GATEWAY_MODEL_PULL_TOKEN 一致。
	Token string
	// Timeout bounds one call to one replica. A slow replica must not hold
	// up the others, which is why every replica is asked concurrently.
	//
	// Timeout 限制对单个副本的单次调用。一个慢副本不能拖住其他副本，这正是
	// 每个副本都被并发询问的原因。
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
// object" rule fleet.New documents.
//
// New 构造一个 Router；未配置任何副本时返回 nil——nil *Router 上的每个方法都会
// 报告 ErrDisabled，与 fleet.New 记录的"没有半成品对象"规则相同。
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

// ReplicaStatus is what happened when one configured replica was asked.
//
// Error mirrors fleet.ReplicaStatus.Error's reasoning: a fixed code rather
// than the transport's own text, which would name internal hosts and ports
// on its way to a browser. Connected is false and Error is empty together
// when the replica answered honestly that node_id is not connected to it —
// that is an expected outcome, not a failure.
//
// ReplicaStatus 是询问某个已配置副本时发生的事。
//
// Error 与 fleet.ReplicaStatus.Error 同一理由：一个固定代号而不是传输层自己的
// 文本，那段文本会在前往浏览器的路上点出内部主机与端口。Connected 为 false 且
// Error 为空，表示该副本如实回答了 node_id 没有连到它——这是一个预期内的结果，
// 不是一次失败。
type ReplicaStatus struct {
	Endpoint  string `json:"endpoint"`
	Connected bool   `json:"connected"`
	// Error is one of "unreachable", "timeout", "unauthorized" or
	// "malformed" when Connected is false and this replica genuinely could
	// not be asked; empty when Connected is true or when the replica simply
	// does not have node_id.
	//
	// Error 在 Connected 为 false 且该副本确实无法被问及时，是
	// "unreachable"、"timeout"、"unauthorized" 或 "malformed" 之一；在
	// Connected 为 true、或该副本只是没有这个 node_id 时为空。
	Error string `json:"error,omitempty"`
}

// PullStatus is one named pull's status, as reported by whichever replica
// answered for it.
//
// PullStatus 是某一个命名拉取的状态，由为它作答的那个副本报告。
type PullStatus struct {
	Name            string
	State           string
	BytesDownloaded int64
	BytesTotal      int64
	Reason          string
	UpdatedAt       time.Time
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
	// Pulls is Status's payload: the reply from whichever connected replica
	// carries the most recently updated entry, the same "most recent
	// evidence wins" rule fleet.fresher applies to node liveness — more
	// than one replica's report can be legitimately correct at once here
	// (Trigger's fan-out means the node may be mid-pull on several at
	// once), and disagreement between them is a timing artifact of how
	// recently each replica's Control stream last heard from the Agent,
	// not a conflict to surface. Always nil for Trigger.
	//
	// Pulls 是 Status 的负载：取自已连接副本中，携带最近一次更新条目的那份
	// 回复——与 fleet.fresher 对节点活性采用的"最新证据胜出"是同一规则。这
	// 里可能确实同时存在一个以上合法正确的副本报告（Trigger 的并发下发意味
	// 着节点可能同时在好几个副本上处于拉取中），它们之间的分歧只是各副本
	// Control 流最近一次听到 Agent 消息的时间差，不是需要暴露出来的冲突。
	// Trigger 时恒为 nil。
	Pulls []PullStatus
	// Replicas is what happened at every configured replica, in configured
	// order.
	//
	// Replicas 是在每个已配置副本上发生的事，按配置顺序排列。
	Replicas []ReplicaStatus
}

// Trigger asks node_id, wherever it is connected among the configured
// replicas, to start pulling names from its local manifest (STATUS.md's P2
// model distribution subtask 1). It never carries a URL — only the names, a
// Gateway replica forwards verbatim to the Agent's own Control stream,
// which is the Gateway-side half of the "按名字触发" design in
// docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md.
//
// Trigger 要求 node_id（无论它连在哪个已配置副本上）开始从本地清单
// （STATUS.md 的 P2 模型分发子任务一）拉取 names。它从不携带 URL——只有名字，
// 由某个 Gateway 副本原样转发进 Agent 自己的 Control 流，是
// docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md
// 中"按名字触发"设计在 Gateway 一侧的落实。
func (r *Router) Trigger(ctx context.Context, nodeID string, names []string) (Result, error) {
	if r == nil {
		return Result{}, ErrDisabled
	}
	payload, err := json.Marshal(triggerRequestWire{Names: names})
	if err != nil {
		return Result{}, fmt.Errorf("modelpullrouter: encoding trigger request: %w", err)
	}
	outcomes := r.fanOut(ctx, func(ctx context.Context, endpoint string) replicaOutcome {
		return r.triggerOne(ctx, endpoint, nodeID, payload)
	})
	return buildResult(outcomes), nil
}

// Status returns node_id's Puller status, as reported by whichever
// configured replica currently holds its connection.
//
// Status 返回 node_id 的 Puller 状态，由当前持有其连接的已配置副本报告。
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
	pulls      []PullStatus
	freshestAt time.Time
}

// fanOut asks every configured replica concurrently via ask, preserving
// configured order in the returned slice regardless of which goroutine
// finishes first — each writes only its own index, the same race-free
// pattern fleet.Aggregator.Nodes uses.
//
// fanOut 通过 ask 并发询问每个已配置副本，无论哪个 goroutine 先完成，返回的
// slice 都保持配置顺序——每个 goroutine 只写自己的下标，与
// fleet.Aggregator.Nodes 同一种无竞争写法。
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

// triggerRequestWire mirrors modelpullapi's triggerRequest.
//
// triggerRequestWire 与 modelpullapi 的 triggerRequest 形状一致。
type triggerRequestWire struct {
	Names []string `json:"names"`
}

// triggerOne posts to one replica's model-pull listener.
//
// triggerOne 向单个副本的模型拉取监听器发起 POST。
func (r *Router) triggerOne(ctx context.Context, endpoint, nodeID string, payload []byte) replicaOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+modelPullPath(nodeID), bytes.NewReader(payload))
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

// wireStatusResponse mirrors modelpullapi's statusResponse.
//
// wireStatusResponse 与 modelpullapi 的 statusResponse 形状一致。
type wireStatusResponse struct {
	GeneratedAt string           `json:"generated_at"`
	Pulls       []wirePullStatus `json:"pulls"`
}

// wirePullStatus mirrors modelpullapi's pullStatusJSON.
//
// wirePullStatus 与 modelpullapi 的 pullStatusJSON 形状一致。
type wirePullStatus struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	BytesDownloaded int64  `json:"bytes_downloaded"`
	BytesTotal      int64  `json:"bytes_total"`
	Reason          string `json:"reason,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

// statusOne reads one replica's model-pull listener.
//
// statusOne 读取单个副本的模型拉取监听器。
func (r *Router) statusOne(ctx context.Context, endpoint, nodeID string) replicaOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+modelPullPath(nodeID), nil)
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
		pulls, freshestAt := renderPulls(wire)
		return replicaOutcome{endpoint: endpoint, connected: true, pulls: pulls, freshestAt: freshestAt}
	case http.StatusNotFound:
		drain(resp.Body)
		return replicaOutcome{endpoint: endpoint}
	case http.StatusUnauthorized:
		return replicaOutcome{endpoint: endpoint, failure: "unauthorized"}
	default:
		return replicaOutcome{endpoint: endpoint, failure: "unreachable"}
	}
}

// renderPulls converts wire's pulls into PullStatus, and reports the latest
// UpdatedAt among them — the zero time when there are none, which sorts
// before every real timestamp and so never wins a freshness comparison
// against a replica that did report something. A pull entry whose
// updated_at fails to parse contributes the zero time in its place: that
// only ever costs it a freshness tie-break, never a crash.
//
// renderPulls 把 wire 的 pulls 转换成 PullStatus，并报告其中最新的
// UpdatedAt——不存在任何条目时为零值时间，它排在所有真实时间戳之前，因此
// 永远不会在新鲜度比较中，胜过一个确实报告了内容的副本。一条 updated_at
// 解析失败的记录，会以零值时间代替——这只会让它在新鲜度平局判断中吃亏，
// 不会造成 panic。
func renderPulls(wire wireStatusResponse) ([]PullStatus, time.Time) {
	pulls := make([]PullStatus, len(wire.Pulls))
	var freshest time.Time
	for i, p := range wire.Pulls {
		updatedAt, _ := time.Parse(time.RFC3339, p.UpdatedAt)
		pulls[i] = PullStatus{
			Name:            p.Name,
			State:           p.State,
			BytesDownloaded: p.BytesDownloaded,
			BytesTotal:      p.BytesTotal,
			Reason:          p.Reason,
			UpdatedAt:       updatedAt,
		}
		if updatedAt.After(freshest) {
			freshest = updatedAt
		}
	}
	return pulls, freshest
}

// buildResult folds every replica's outcome into a Result: Connected is set
// by any replica that had node_id, and Pulls comes from whichever connected
// replica's freshestAt is latest — see Result.Pulls's doc comment for why
// more than one can legitimately disagree.
//
// buildResult 把每个副本的结果折进一个 Result：只要有副本拥有 node_id，
// Connected 就为 true；Pulls 取自已连接副本中 freshestAt 最新的那一份——为
// 什么不止一个副本可能合理地互相不一致，见 Result.Pulls 的文档注释。
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
			result.Pulls = o.pulls
			freshestAt = o.freshestAt
			haveFreshest = true
		}
	}
	return result
}

// modelPullPath builds the per-node path both triggerOne and statusOne hit,
// matching modelpullapi's mounted route exactly.
//
// modelPullPath 构造 triggerOne 与 statusOne 共用的按节点路径，与
// modelpullapi 挂载的路由完全一致。
func modelPullPath(nodeID string) string {
	return "/internal/v1/nodes/" + url.PathEscape(nodeID) + "/model-pulls"
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
// reused, the same courtesy fleet.fetchInto extends by always reading
// through http.MaxBytesReader.
//
// drain 丢弃响应体的剩余部分，好让连接可以被复用——与 fleet.fetchInto
// 始终通过 http.MaxBytesReader 读取，是同一种礼貌。
func drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseBytes))
}
