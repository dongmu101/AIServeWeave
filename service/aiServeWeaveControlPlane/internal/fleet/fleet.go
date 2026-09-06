// Package fleet aggregates what the Gateway replicas report: the node
// inventory, the workflow catalogue, and the runs currently in their job
// tables.
//
// It exists because no single replica knows the fleet. The tunnel design's
// third constraint is that a replica serves only the nodes connected to itself
// — there is no forwarding between replicas on the request path — so "which
// nodes exist" is a question with N partial answers and no authoritative one.
// This package asks all of them, merges what comes back, and reports which
// replicas answered.
//
// What it will not do is hide a gap. A replica that times out becomes a named
// failure in the response rather than a silently shorter node list: an
// operator looking at a console during an incident must be able to tell "that
// node is gone" from "the replica it was connected to did not answer".
//
// fleet 包聚合各 Gateway 副本所报告的内容：节点清单、工作流目录，以及它们 job 表中
// 当前的运行。
//
// 它之所以存在，是因为没有任何单个副本知道整个机群。隧道设计的第三条约束是「每个副本
// 只服务连到自己身上的节点」——请求路径上没有副本间转发——因此「有哪些节点」这个问题有
// N 个局部答案，没有一个权威答案。本包向全部副本发问，把回来的内容合并，并报告哪些副本
// 作了答。
//
// 它绝不做的一件事是掩盖缺口。超时的副本会在响应中成为一条具名的失败，而不是让节点列表
// 悄悄变短：事故期间盯着控制台的运维，必须能分辨「那个节点没了」与「它所连的副本没作答」。
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"AIServeWeave/common/nodeview"
	"AIServeWeave/common/runtime"
)

// ErrDisabled is returned when the inventory was not configured. It is a
// distinct error because the answer to it is a deployment change, not a retry.
//
// ErrDisabled 在清单未配置时返回。它是一个独立的错误，因为对它的应对是改部署，
// 而不是重试。
var ErrDisabled = errors.New("fleet: the operator inventory is not configured")

// DefaultTimeout bounds one call to one replica.
//
// DefaultTimeout 限制对单个副本的单次调用。
const DefaultTimeout = 3 * time.Second

// Config configures an Aggregator.
//
// Config 配置一个 Aggregator。
type Config struct {
	Gateways []string
	Token    string
	Timeout  time.Duration
	Client   *http.Client
	Clock    runtime.Clock
}

// Aggregator asks every configured replica and merges the answers.
//
// Aggregator 向每个已配置的副本发问，并合并它们的答案。
type Aggregator struct {
	gateways []string
	token    string
	client   *http.Client
	clock    runtime.Clock
}

// New builds an Aggregator. A configuration with no gateways yields nil,
// which every method reports as ErrDisabled — so a deployment without the
// feature has no half-initialized object to reason about.
//
// New 构造一个 Aggregator。没有 gateway 的配置返回 nil，而每个方法都把它报告为
// ErrDisabled —— 这样没有启用该功能的部署，就不存在一个「初始化了一半」的对象需要
// 去推敲。
func New(cfg Config) *Aggregator {
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
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &Aggregator{gateways: cfg.Gateways, token: cfg.Token, client: client, clock: clock}
}

// ReplicaStatus is what happened when one replica was asked.
//
// The error is a fixed code rather than the upstream's text: a transport
// message names hosts and ports of the internal network, and this document is
// on its way to a browser.
//
// ReplicaStatus 是询问某个副本时发生的事。
//
// 其中的错误是一个固定的代号而不是上游的文本：传输层的报错会点出内部网络的主机与端口，
// 而这份文档正在前往浏览器的路上。
type ReplicaStatus struct {
	// Endpoint is the configured base URL. It names which replica this is
	// for an operator who has the deployment in front of them.
	//
	// Endpoint 是配置的基础 URL。对着部署清单的运维，可以据此认出这是哪个副本。
	Endpoint string `json:"endpoint"`
	// ReplicaID is what the replica calls itself, absent when it did not
	// answer.
	//
	// ReplicaID 是副本对自己的称呼，未作答时缺席。
	ReplicaID string `json:"replica_id,omitempty"`
	// GeneratedAt is when that replica looked at its node table.
	//
	// GeneratedAt 是该副本查看自己节点表的时刻。
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	// Error is empty when the replica answered. Otherwise it is one of
	// "unreachable", "timeout", "unauthorized", "malformed" or
	// "unsupported" — the last meaning the replica does not serve this
	// document at all.
	//
	// Error 在副本作答时为空。否则它是 "unreachable"、"timeout"、"unauthorized"、
	// "malformed" 或 "unsupported" 之一，最后一个表示该副本根本不提供这份文档。
	Error string `json:"error,omitempty"`
	// NodeCount is how many nodes that replica reported.
	//
	// NodeCount 是该副本报告的节点数。
	NodeCount int `json:"node_count"`
}

// Node is one node as the fleet sees it: the merged view, plus which replicas
// reported it.
//
// A node connected to several replicas appears once. The reported view is the
// one from the replica with the most recent heartbeat, because that is the
// freshest evidence about it — the others are kept only as the list of
// replicas that can reach it, which is what an operator needs when deciding
// whether a node is at risk of being cut off.
//
// Node 是机群眼中的一个节点：合并后的视图，外加是哪些副本报告了它。
//
// 连到多个副本的节点只出现一次。所报告的视图取自心跳最新的那个副本，因为那是关于它最新
// 鲜的证据——其余副本只作为「能够到它的副本列表」保留下来，而那正是运维在判断某个节点是否
// 有被切断风险时所需要的。
type Node struct {
	nodeview.Node
	// Replicas are the replica ids that reported this node, sorted.
	//
	// Replicas 是报告了本节点的副本 id，已排序。
	Replicas []string `json:"replicas"`
	// ObservedAt is when the reporting replica looked.
	//
	// ObservedAt 是作出报告的那个副本查看的时刻。
	ObservedAt time.Time `json:"observed_at"`
}

// Snapshot is the whole answer: the merged nodes, and what each replica said.
//
// Snapshot 是完整的答案：合并后的节点，以及每个副本各自说了什么。
type Snapshot struct {
	// CollectedAt is when this service asked, which is not when any replica
	// looked. Both are in the document because the difference between them
	// is the age of the data.
	//
	// CollectedAt 是本服务发问的时刻，它不等于任何副本查看的时刻。两者都放进文档，
	// 因为它们之间的差值就是数据的新旧程度。
	CollectedAt time.Time       `json:"collected_at"`
	Replicas    []ReplicaStatus `json:"replicas"`
	Nodes       []Node          `json:"nodes"`
	// Partial is true when any replica failed to answer. It is a field
	// rather than something a caller derives, so a console cannot forget to
	// derive it.
	//
	// Partial 在任何副本未能作答时为 true。它是一个字段而不是留给调用方推导的东西，
	// 这样控制台就不会忘记去推导它。
	Partial bool `json:"partial"`
}

// Nodes asks every replica and merges what comes back.
//
// Nodes 向每个副本发问，并合并回来的内容。
func (a *Aggregator) Nodes(ctx context.Context) (Snapshot, error) {
	if a == nil {
		return Snapshot{}, ErrDisabled
	}

	type result struct {
		status  ReplicaStatus
		replica nodeview.Replica
	}
	results := make([]result, len(a.gateways))

	var wg sync.WaitGroup
	for i, endpoint := range a.gateways {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replica, failure := a.fetch(ctx, endpoint)
			status := ReplicaStatus{Endpoint: endpoint, Error: failure}
			if failure == "" {
				generated := replica.GeneratedAt
				status.ReplicaID = replica.ReplicaID
				status.GeneratedAt = &generated
				status.NodeCount = len(replica.Nodes)
			}
			results[i] = result{status: status, replica: replica}
		}()
	}
	wg.Wait()

	snapshot := Snapshot{
		CollectedAt: a.clock.Now().UTC(),
		Replicas:    make([]ReplicaStatus, 0, len(results)),
	}
	merged := make(map[string]Node)
	for _, item := range results {
		snapshot.Replicas = append(snapshot.Replicas, item.status)
		if item.status.Error != "" {
			snapshot.Partial = true
			continue
		}
		for _, node := range item.replica.Nodes {
			merge(merged, node, item.replica)
		}
	}

	snapshot.Nodes = make([]Node, 0, len(merged))
	for _, node := range merged {
		sort.Strings(node.Replicas)
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	sort.Slice(snapshot.Nodes, func(i, j int) bool {
		return snapshot.Nodes[i].NodeID < snapshot.Nodes[j].NodeID
	})
	return snapshot, nil
}

// merge folds one replica's view of a node into the accumulated one.
//
// merge 把某个副本对一个节点的视图折进已累积的视图。
func merge(into map[string]Node, node nodeview.Node, replica nodeview.Replica) {
	existing, seen := into[node.NodeID]
	if !seen {
		into[node.NodeID] = Node{
			Node:       node,
			Replicas:   []string{replica.ReplicaID},
			ObservedAt: replica.GeneratedAt,
		}
		return
	}

	existing.Replicas = append(existing.Replicas, replica.ReplicaID)
	if fresher(node, existing.Node) {
		// Keep the accumulated replica list: it is about the node, not about
		// whichever view won.
		//
		// 保留已累积的副本列表：它描述的是这个节点，而不是哪一份视图胜出。
		replicas := existing.Replicas
		existing.Node = node
		existing.ObservedAt = replica.GeneratedAt
		existing.Replicas = replicas
	}
	into[node.NodeID] = existing
}

// fresher reports whether candidate is better evidence than current. A node
// that some replica considers live beats one nobody does, and among equals the
// more recent heartbeat wins.
//
// fresher 报告 candidate 是否比 current 更有说服力。只要有副本认为某个节点是活的，
// 它就胜过所有副本都不这么认为的情形；在这一点相同时，心跳更新的胜出。
func fresher(candidate, current nodeview.Node) bool {
	if candidate.Live != current.Live {
		return candidate.Live
	}
	if candidate.LastHeartbeat == nil {
		return false
	}
	if current.LastHeartbeat == nil {
		return true
	}
	return candidate.LastHeartbeat.After(*current.LastHeartbeat)
}

// fetch reads one replica's inventory, classifying every failure into a fixed
// code.
//
// fetch 读取一个副本的清单，并把每一种失败归入一个固定的代号。
func (a *Aggregator) fetch(ctx context.Context, endpoint string) (nodeview.Replica, string) {
	var replica nodeview.Replica
	failure := fetchInto(ctx, a, endpoint, "/internal/v1/nodes", &replica, func(doc nodeview.Replica) bool {
		return doc.ReplicaID != "" && !doc.GeneratedAt.IsZero() && doc.Nodes != nil
	})
	return replica, failure
}

// fetchInto reads one document from one replica into out, and refuses one that
// does not carry what the contract requires.
//
// The validation is not belt and braces. JSON decoding succeeds on `{}`, on
// `null`, and on `{"error":"unavailable"}` — every one of them leaves a
// zero-valued document, and a zero-valued document is indistinguishable from a
// replica that answered honestly and has nothing. Without a check, a service
// erroring with a 200 is reported as a healthy, empty replica: exactly the
// wrong answer during an incident, and one that suppresses the partial flag
// that would otherwise say the picture is incomplete.
//
// So the caller supplies what a valid document looks like. It is a parameter
// rather than an interface method because these types live in common/ and are
// shared with the Gateway: what makes a document usable is this consumer's
// question, and making every call site answer it is what keeps a new endpoint
// from silently skipping the check.
//
// Every failure collapses into one of five codes. The transport's own message
// never travels with it: it names hosts and ports of the internal network, and
// these documents are on their way to a browser.
//
// fetchInto 从一个副本读取一份文档到 out，并拒绝那些没有携带契约所要求内容的文档。
//
// 这层校验不是多此一举。JSON 解码在 `{}`、`null`、`{"error":"unavailable"}` 上都会成功
// ——它们每一个留下的都是零值文档，而零值文档与「一个如实作答、且确实什么都没有的副本」
// 无法区分。没有这项检查，一个用 200 报错的服务会被报告成一个健康的空副本：那在事故中
// 恰恰是最错的答案，而且它还会压掉本该说明「这幅图并不完整」的 partial 标志。
//
// 因此由调用方给出「什么算一份有效文档」。它是参数而不是接口方法，因为这些类型位于
// common/ 且与 Gateway 共享：一份文档是否可用，是本消费方的问题，而让每个调用点都回答
// 一次，正是新端点不会悄悄跳过这项检查的原因。
//
// 每一种失败都收敛成五个代号之一。传输层自己的报错绝不随之传递：它会点出内部网络的
// 主机与端口，而这些文档正在前往浏览器的路上。
func fetchInto[T any](ctx context.Context, a *Aggregator, endpoint, path string, out *T, valid func(T) bool) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path, nil)
	if err != nil {
		return "unreachable"
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return "timeout"
		}
		return "unreachable"
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "unauthorized"
	case resp.StatusCode == http.StatusNotFound:
		// A replica that does not serve this document is not broken: the
		// endpoints are mounted per capability, so an older or differently
		// configured replica answers 404. It is still a gap in the answer,
		// and it is named rather than counted as an empty result.
		//
		// 一个不提供该文档的副本并没有坏：这些端点是按能力分别挂载的，因此更旧的或
		// 配置不同的副本会回 404。它依然是答案中的一处缺口，会被具名报出而不是被当作
		// 一个空结果。
		return "unsupported"
	case resp.StatusCode != http.StatusOK:
		return "unreachable"
	}

	// The body is bounded: this is a document from a service on the internal
	// network, but "internal" is not a size limit, and these responses are
	// exactly the kind that grow with the deployment.
	//
	// 响应体有上限：这是一份来自内部网络中某个服务的文档，但「内部」不构成一个大小
	// 限制，而这些响应恰恰是那种会随部署规模增长的。
	decoder := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, maxInventoryBytes))
	if err := decoder.Decode(out); err != nil {
		return "malformed"
	}
	if !valid(*out) {
		return "malformed"
	}
	return ""
}

// maxInventoryBytes bounds one replica's document. A thousand nodes with a
// full runtime inventory each is comfortably under this.
//
// maxInventoryBytes 限制单个副本文档的大小。一千个节点、每个都带完整运行时清单，也
// 远在这个上限之内。
const maxInventoryBytes = 32 << 20

// isTimeout reports whether an error is a deadline rather than a refusal.
//
// isTimeout 报告一个错误是超时而不是被拒绝。
func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// Deployment is one place a model is actually served: a runtime on a node.
//
// Deployment 是一个模型真正被提供的一处所在：某个节点上的某个运行时。
type Deployment struct {
	NodeID    string `json:"node_id"`
	RuntimeID string `json:"runtime_id"`
	// Backend is the runtime kind — vllm, sglang, ollama, comfyui — which is
	// what actually serves the model. Two deployments of one model on
	// different backends are not interchangeable in capability, which is why
	// this is on the deployment rather than on the model.
	//
	// Backend 是运行时种类——vllm、sglang、ollama、comfyui——真正提供该模型的东西。
	// 同一个模型在不同后端上的两处部署，在能力上并不等价，这正是它挂在部署上而不是
	// 挂在模型上的原因。
	Backend string `json:"backend,omitempty"`
	// State is the runtime's state, so a model listed on an unhealthy runtime
	// does not read as available.
	//
	// State 是运行时的状态，好让挂在不健康运行时上的模型不会被读成「可用」。
	State string `json:"state"`
	// NodeLive is the node's liveness. A model on a node with no Control
	// stream cannot be reached however healthy the runtime last looked.
	//
	// NodeLive 是节点的活性。一个节点若没有 Control 流，无论其运行时上次看起来多健康，
	// 它上面的模型都够不到。
	NodeLive     bool              `json:"node_live"`
	Capabilities map[string]string `json:"capabilities,omitempty"`
}

// Model is one model id, and everywhere it is served.
//
// This is the model as the fleet reports it, not as a caller addresses it: the
// Gateway's routing table maps caller-facing aliases onto these, and that
// table is the Gateway's file configuration, which this service does not hold.
// A console must not present this list as the set of names an API caller may
// use.
//
// Model 是一个模型 id，以及它被提供的所有位置。
//
// 这是机群所报告的模型，而不是调用方所寻址的模型：Gateway 的路由表把面向调用方的别名
// 映射到这些模型上，而那张表是 Gateway 的文件配置，本服务并不持有它。控制台不得把这份
// 列表当作「API 调用方可以使用的名字集合」来呈现。
type Model struct {
	ID          string       `json:"id"`
	Deployments []Deployment `json:"deployments"`
	// AvailableDeployments counts the deployments that are on a live node and
	// a healthy runtime right now.
	//
	// AvailableDeployments 统计此刻位于活跃节点、且运行时健康的部署数量。
	AvailableDeployments int `json:"available_deployments"`
}

// Catalog is the model view of the same snapshot.
//
// Catalog 是同一份快照的模型视角。
type Catalog struct {
	CollectedAt time.Time       `json:"collected_at"`
	Replicas    []ReplicaStatus `json:"replicas"`
	Partial     bool            `json:"partial"`
	Models      []Model         `json:"models"`
}

// Models derives the catalog from the node snapshot. It is the same read: a
// second round trip to build a second view of one fleet would let the two
// disagree.
//
// Models 由节点快照推导出目录。它就是同一次读取：为一个机群的第二个视图再跑一次往返，
// 只会让两者彼此矛盾。
func (a *Aggregator) Models(ctx context.Context) (Catalog, error) {
	snapshot, err := a.Nodes(ctx)
	if err != nil {
		return Catalog{}, err
	}

	byModel := make(map[string][]Deployment)
	for _, node := range snapshot.Nodes {
		for _, rt := range node.Runtimes {
			for _, model := range rt.Models {
				byModel[model.ID] = append(byModel[model.ID], Deployment{
					NodeID:       node.NodeID,
					RuntimeID:    rt.ID,
					Backend:      rt.Kind,
					State:        rt.State,
					NodeLive:     node.Live,
					Capabilities: model.Capabilities,
				})
			}
		}
	}

	catalog := Catalog{
		CollectedAt: snapshot.CollectedAt,
		Replicas:    snapshot.Replicas,
		Partial:     snapshot.Partial,
		Models:      make([]Model, 0, len(byModel)),
	}
	for id, deployments := range byModel {
		sort.Slice(deployments, func(i, j int) bool {
			if deployments[i].NodeID != deployments[j].NodeID {
				return deployments[i].NodeID < deployments[j].NodeID
			}
			return deployments[i].RuntimeID < deployments[j].RuntimeID
		})
		available := 0
		for _, deployment := range deployments {
			if deployment.NodeLive && deployment.State == string(runtime.StateHealthy) {
				available++
			}
		}
		catalog.Models = append(catalog.Models, Model{
			ID:                   id,
			Deployments:          deployments,
			AvailableDeployments: available,
		})
	}
	sort.Slice(catalog.Models, func(i, j int) bool { return catalog.Models[i].ID < catalog.Models[j].ID })
	return catalog, nil
}

// String renders an endpoint for a log line, without the token.
//
// String 为日志行渲染一个 endpoint，不含 token。
func (s ReplicaStatus) String() string {
	if s.Error == "" {
		return fmt.Sprintf("%s: %d nodes", s.Endpoint, s.NodeCount)
	}
	return fmt.Sprintf("%s: %s", s.Endpoint, s.Error)
}
