// Package adminapi is the Gateway's read-only fleet inventory: the nodes this
// replica terminates tunnels for, rendered for the control plane to aggregate.
//
// It is a separate listener from the inference API on purpose, and it is off
// unless an address is configured. The public listener answers callers holding
// a tenant's API key; this one answers an operator's control plane holding a
// deployment secret. Putting them on one port would mean one misconfigured
// route away from a tenant reading the fleet, and the two have nothing else in
// common — no shared middleware, no shared rate limiting, no shared audience.
//
// Nothing here writes. Node approval, disabling and maintenance are a
// different feature with a persistence and propagation design of their own;
// this package cannot be extended into them by adding a handler, because it
// holds no state and takes no writer.
//
// The job endpoint is the one that answers about tenant data, and it requires
// the tenant to be named. That is not a convenience for the caller: this
// replica's job table holds every tenant's runs, and an endpoint that could
// return all of them would make the control plane's filtering the only thing
// standing between two tenants.
//
// adminapi 包是 Gateway 只读的机群清单：本副本为哪些节点终结隧道，渲染成供控制面聚合
// 的形式。
//
// 它刻意与推理 API 分处不同的监听器，且未配置地址时不启用。公开监听器面对的是持有某个
// 租户 API Key 的调用方，而这一个面对的是持有部署密钥的运维控制面。把它们放在同一个
// 端口上，意味着距离「某个租户读到整个机群」只差一条配错的路由，而两者别无共同之处
// ——没有共享的中间件、没有共享的限流、没有共同的受众。
//
// 这里没有任何写操作。节点的审批、禁用与维护是另一项功能，有它自己的持久化与下发设计；
// 本包无法通过「加一个 handler」被扩展成那样，因为它不持有状态，也不接受写入方。
//
// job 端点是这里唯一回答租户数据的端点，而它要求点名租户。这不是为调用方提供的便利：
// 本副本的 job 表持有每个租户的运行，一个能把它们全部返回的端点，会让控制面的过滤成为
// 横在两个租户之间的唯一一道东西。
package adminapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeview"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/workflowview"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// Config configures the admin listener's handler.
//
// Config 配置运维监听器的 handler。
type Config struct {
	// Token authenticates the caller. An empty token is refused at
	// construction rather than accepted as "no authentication": a fleet
	// inventory served to anybody who can reach the port is not a degraded
	// mode worth having.
	//
	// Token 认证调用方。空 token 在构造时就被拒绝，而不是被当作「不需要认证」：一份
	// 谁能连上端口就能读到的机群清单，不是一种值得保留的降级模式。
	Token string
	// Nodes reports what this replica currently sees. It is a function
	// rather than the Server itself so a test can drive the handler without
	// a tunnel, and so this package cannot reach anything else on the
	// Server.
	//
	// Nodes 报告本副本当前看到的东西。它是一个函数而不是 Server 本身，这样测试无需
	// 隧道即可驱动 handler，也让本包够不到 Server 上的其他任何东西。
	Nodes func() []tunnelserver.NodeInfo
	// Jobs reports one tenant's runs on this replica, and whether the job
	// table has evicted anything. Nil leaves the job endpoint unmounted,
	// which is how a replica with no front door says it has no runs to
	// report rather than reporting none.
	//
	// Jobs 报告本副本上某一个租户的运行，以及 job 表是否逐出过内容。为 nil 时不挂载
	// job 端点——一个没有前门的副本以此表明「它没有可报告的运行」，而不是报告「没有」。
	Jobs func(tenantID string) ([]workflowview.Job, bool)
	// Templates reports the workflow catalogue this replica registered.
	//
	// Templates 报告本副本注册的工作流目录。
	Templates func() []workflowview.Template
	// ReplicaID identifies which replica produced a document. The control
	// plane needs it to tell one replica's partial view from another's.
	//
	// ReplicaID 标识某份文档由哪个副本产生。控制面需要它来区分不同副本的局部视图。
	ReplicaID string
	Clock     runtime.Clock
}

// New builds the admin handler, or reports why it cannot.
//
// New 构造运维 handler，或说明为什么构造不了。
func New(cfg Config) (http.Handler, error) {
	if cfg.Token == "" {
		return nil, errNoToken
	}
	if cfg.Nodes == nil {
		return nil, errNoSource
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeJSON(w, http.StatusOK, snapshot(cfg.ReplicaID, clock.Now(), cfg.Nodes()))
	})

	if cfg.Templates != nil {
		mux.HandleFunc("GET /internal/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
			if !authorized(r, cfg.Token) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			writeJSON(w, http.StatusOK, workflowview.TemplateCatalog{
				ReplicaID:   cfg.ReplicaID,
				GeneratedAt: clock.Now().UTC(),
				Templates:   cfg.Templates(),
			})
		})
	}

	if cfg.Jobs != nil {
		mux.HandleFunc("GET /internal/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
			if !authorized(r, cfg.Token) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			tenantID := r.URL.Query().Get("tenant_id")
			if tenantID == "" {
				// Refused rather than answered with every tenant's runs. The
				// caller forgot which tenant it was asking about, and the
				// helpful reading of that question is the dangerous one.
				//
				// 拒绝，而不是用所有租户的运行来作答。调用方忘了自己在问哪个租户，
				// 而对这个问题「乐于助人」的那种理解，恰恰是危险的那种。
				writeError(w, http.StatusBadRequest, "tenant_id is required")
				return
			}
			jobs, truncated := cfg.Jobs(tenantID)
			writeJSON(w, http.StatusOK, workflowview.JobPage{
				ReplicaID:   cfg.ReplicaID,
				GeneratedAt: clock.Now().UTC(),
				TenantID:    tenantID,
				Jobs:        jobs,
				Truncated:   truncated,
			})
		})
	}
	return mux, nil
}

// snapshot renders one replica's answer.
//
// snapshot 渲染一个副本的回答。
func snapshot(replicaID string, now time.Time, nodes []tunnelserver.NodeInfo) nodeview.Replica {
	out := nodeview.Replica{
		ReplicaID:   replicaID,
		GeneratedAt: now.UTC(),
		Nodes:       make([]nodeview.Node, 0, len(nodes)),
	}
	for _, info := range nodes {
		out.Nodes = append(out.Nodes, renderNode(info))
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].NodeID < out.Nodes[j].NodeID })
	return out
}

// renderNode converts one NodeInfo, naming every field that may cross. It
// never marshals a NodeInfo directly: that struct is the scheduler's working
// view and grows with the scheduler's needs, not with what an operator should
// see.
//
// renderNode 转换一条 NodeInfo，逐一点名可以外传的每个字段。它绝不直接序列化
// NodeInfo：那个结构体是调度器的工作视图，它会随调度器的需要增长，而不是随「运维应当
// 看到什么」增长。
func renderNode(info tunnelserver.NodeInfo) nodeview.Node {
	node := nodeview.Node{
		NodeID:             info.NodeID,
		AgentVersion:       info.AgentVersion,
		Live:               info.Live,
		Draining:           info.Draining,
		Maintenance:        info.Maintenance,
		Labels:             copyLabels(info.Labels),
		InflightRequests:   info.InflightRequests,
		DeclaredRuntimeIDs: append([]string(nil), info.RuntimeIDs...),
	}
	if !info.LastHeartbeat.IsZero() {
		heartbeat := info.LastHeartbeat.UTC()
		node.LastHeartbeat = &heartbeat
	}
	if res := info.Resources; res != nil {
		node.Resources = &nodeview.Resources{
			CPUCores:       res.GetCpuCores(),
			MemoryBytes:    res.GetMemoryBytes(),
			GPUCount:       res.GetGpuCount(),
			GPUMemoryBytes: res.GetGpuMemoryBytes(),
			OS:             res.GetOs(),
			Arch:           res.GetArch(),
		}
	}
	if len(info.IdleSlots) > 0 {
		node.IdleSlots = make(map[string]int, len(info.IdleSlots))
		for class, count := range info.IdleSlots {
			node.IdleSlots[slotClassName(class)] = count
		}
	}
	node.Runtimes = make([]nodeview.Runtime, 0, len(info.Runtimes))
	for _, snap := range info.Runtimes {
		node.Runtimes = append(node.Runtimes, nodeview.FromSnapshot(snap))
	}
	return node
}

// slotClassName renders a slot class without the proto's enum prefix, which
// is noise in a console.
//
// slotClassName 渲染槽位 class，去掉 proto 的枚举前缀——那在控制台里只是噪音。
func slotClassName(class tunnelv1.SlotClass) string {
	switch class {
	case tunnelv1.SlotClass_SLOT_CLASS_INFERENCE:
		return "inference"
	case tunnelv1.SlotClass_SLOT_CLASS_BULK:
		return "bulk"
	default:
		return "unspecified"
	}
}

// copyLabels copies the Agent's declared labels, so the response cannot alias
// the node table's map.
//
// copyLabels 复制 Agent 声明的标签，好让响应不会与节点表里的 map 共用同一份。
func copyLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for name, value := range labels {
		out[name] = value
	}
	return out
}

// authorized compares the presented token in constant time.
//
// authorized 以常数时间比较出示的 token。
func authorized(r *http.Request, expected string) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(expected)) == 1
}

// writeJSON writes one response, uncacheable: it is a point-in-time view of a
// live fleet, and a cached copy of it is a wrong answer with a timestamp.
//
// writeJSON 写出一个响应，且不可缓存：它是一个活动机群某一时刻的视图，而它的缓存副本
// 是一个带着时间戳的错误答案。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError writes the failure shape, with a fixed message.
//
// writeError 写出失败时的形状，文案固定。
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
