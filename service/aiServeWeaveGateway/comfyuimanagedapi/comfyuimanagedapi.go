// Package comfyuimanagedapi is the Gateway's write entry point for
// STATUS.md's P2 ComfyUI Managed Docker deployment, subtask 2: apply a
// start/stop/restart lifecycle action to a node's one locally-declared
// Managed ComfyUI instance, and read back its last-reported
// container-lifecycle status.
//
// It is a separate listener from modelpullapi's trigger endpoint on
// purpose, even though both are writes: they control different
// capabilities with different blast radii if their token leaks —
// modelpullapi's leaks the ability to make a node download whatever its
// local manifest already permits, this one's leaks the ability to stop or
// restart a running ComfyUI container. Different blast radius, different
// token, different port — the same reasoning modelpullapi already applies
// against folding itself into adminapi's read-only listener.
//
// Every request here still respects the tunnel's own boundaries: a trigger
// only reaches a node connected to this replica (tunnelserver.Server's "no
// forwarding" rule), and it never carries an image name, mount path, or any
// other spec field — only a closed action against the one instance the
// Agent already declared locally. See
// docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-subtask2-design.md
// for why.
//
// A RESTART is refused with 409 while this replica is tracking a
// non-terminal job routed to the target node (STATUS.md's P2 "排空升级检
// 查"), so an in-progress run is not torn down by a container recycle. START
// and STOP are not gated. The check only covers this replica's own routing
// table — a node connected to several replicas can have a job known only to
// a different one — and it does not wait for a job to finish; the caller
// must retry once it has drained.
//
// 本副本仍在追踪一个路由到目标节点的非终态 job 时，RESTART 会被 409 拒绝
// （STATUS.md 的 P2「排空升级检查」），这样一次容器重建就不会把一个正在
// 进行的运行拆掉。START 和 STOP 不受此把关。该检查只覆盖本副本自己的路由
// 表——一个同时连到多个副本的节点，其 job 可能只被另一个副本知道——且它不
// 会等待 job 结束；调用方需要在排空之后自行重试。
//
// comfyuimanagedapi 包是 STATUS.md P2 ComfyUI Managed Docker 部署子任务二的
// Gateway 写入口：对一个节点本地已声明的那一个 Managed ComfyUI 实例施加
// start/stop/restart 生命周期动作，并读回它最后一次上报的容器生命周期状态。
//
// 它刻意与 modelpullapi 的触发端点分处不同的监听器，尽管两者都是写操作：它
// 们控制的是不同的能力，token 泄漏后果的爆炸半径不同——modelpullapi 泄漏的
// 是能让节点下载它本地清单已经批准的一切，这一个泄漏的是能停止或重启一个
// 正在运行的 ComfyUI 容器。不同的爆炸半径、不同的 token、不同的端口——与
// modelpullapi 自己不并入 adminapi 只读监听器的理由相同。
//
// 这里的每一次请求仍然遵守隧道自己的边界：触发只能到达连到本副本的节点
// （tunnelserver.Server 的"不转发"规则），且从不携带镜像名、挂载路径或任何
// 其他 spec 字段——只有一个针对 Agent 本地已声明实例的封闭动作。原因见
// docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-subtask2-design.md。
package comfyuimanagedapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/common/runtime"
)

// Config configures the ComfyUI Managed listener's handler.
//
// Config 配置 ComfyUI Managed 监听器的 handler。
type Config struct {
	// Token authenticates the caller. An empty token is refused at
	// construction, the same restraint modelpullapi.Config.Token documents.
	//
	// Token 认证调用方。空 token 在构造时就被拒绝，与
	// modelpullapi.Config.Token 同一种克制。
	Token string
	// Trigger asks nodeID to apply action to its one locally-declared
	// Managed instance. It is
	// tunnelserver.Server.TriggerComfyUIManagedAction, injected as a
	// function so this package cannot reach anything else on the Server.
	//
	// Trigger 请求 nodeID 对它本地已声明的那一个 Managed 实例施加 action。
	// 它就是 tunnelserver.Server.TriggerComfyUIManagedAction，以函数形式
	// 注入，这样本包够不到 Server 上的其他任何东西。
	Trigger func(nodeID string, action comfyuimanagedstatus.Action) error
	// Status reports this replica's last-known Managed ComfyUI status for
	// nodeID, and whether the node is known to this replica at all. It is
	// tunnelserver.Server.ComfyUIManagedStatus.
	//
	// Status 报告本副本对 nodeID 最后已知的 Managed ComfyUI 状态，以及本副
	// 本是否知道这个节点。它就是
	// tunnelserver.Server.ComfyUIManagedStatus。
	Status func(nodeID string) ([]comfyuimanagedstatus.Status, bool)
	// HasActiveJob reports whether this replica is tracking a non-terminal
	// job routed to nodeID. It gates RESTART only (STATUS.md's P2 "排空升级
	// 检查"): a node with a job still running refuses RESTART with 409 rather
	// than tearing the container down under it. It is
	// httpapi.Server.HasActiveJobOnNode, injected the same way Trigger and
	// Status are — this package still cannot reach anything else on Server
	// or on the job store. The check only sees this replica's own routing:
	// a node connected to several replicas can have a job known only to a
	// different one, which this cannot see.
	//
	// HasActiveJob 报告本副本是否在追踪任何路由到 nodeID 的非终态
	// job。它只把关 RESTART（STATUS.md 的 P2「排空升级检查」）：一个仍有
	// job 在跑的节点，RESTART 会被 409 拒绝，而不是把容器从它下面拆走。它
	// 就是 httpapi.Server.HasActiveJobOnNode，以与 Trigger、Status 相同的
	// 方式注入——本包仍够不到 Server 或 job 存储上的其他任何东西。该检查只
	// 看得到本副本自己的路由：一个同时连到多个副本的节点，其 job 可能只被
	// 另一个副本知道，本检查看不到那种情况。
	HasActiveJob func(nodeID string) bool
	// TriggerCustomNodeInstall asks nodeID to install name — its own local
	// allowlist decides whether it is known, never a URL this call carries
	// (STATUS.md's P2 ComfyUI Managed Docker deployment, subtask 4). It is
	// tunnelserver.Server.TriggerComfyUIManagedCustomNodeInstall, injected
	// the same way Trigger is.
	//
	// TriggerCustomNodeInstall 请求 nodeID 安装 name——是否已知由它自己本
	// 地的允许列表决定，本调用从不携带 URL（STATUS.md 的 P2 ComfyUI
	// Managed Docker 部署子任务四）。它就是
	// tunnelserver.Server.TriggerComfyUIManagedCustomNodeInstall，以与
	// Trigger 相同的方式注入。
	TriggerCustomNodeInstall func(nodeID, name string) error
	Clock                    runtime.Clock
}

// New builds the ComfyUI Managed handler, or reports why it cannot.
//
// New 构造 ComfyUI Managed handler，或说明为什么构造不了。
func New(cfg Config) (http.Handler, error) {
	if cfg.Token == "" {
		return nil, errNoToken
	}
	if cfg.Trigger == nil || cfg.Status == nil || cfg.HasActiveJob == nil || cfg.TriggerCustomNodeInstall == nil {
		return nil, errNoSource
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/nodes/{node_id}/comfyui-managed", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body triggerRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		action, ok := parseAction(body.Action)
		if !ok {
			writeError(w, http.StatusBadRequest, `action must be one of "start", "stop", "restart"`)
			return
		}
		nodeID := r.PathValue("node_id")
		if action == comfyuimanagedstatus.ActionRestart && cfg.HasActiveJob(nodeID) {
			// Only RESTART is gated: it tears the container down and brings
			// it back up under a job that may still be running against it.
			// START and STOP are left alone — STOP is already an explicit
			// "take it down" request, and START never disrupts anything
			// that's running.
			//
			// 只有 RESTART 被把关：它会把容器拆掉再带起来，而这个容器上可能
			// 还有一个 job 在跑。START 和 STOP 不受影响——STOP 本身就是明确
			// 的"把它停下来"请求，START 从不会打断任何正在运行的东西。
			writeError(w, http.StatusConflict, "node has an active job on this replica; drain it before restarting")
			return
		}
		if err := cfg.Trigger(nodeID, action); err != nil {
			// The only failure TriggerComfyUIManagedAction reports is "not
			// connected to this replica" — never a rejection of the action
			// itself, which is only ever observable through the status GET
			// below (subtask 2's "no dedicated ack" design, same as model
			// pull). 404 is the honest answer either way: this replica has
			// nothing to dispatch to.
			//
			// TriggerComfyUIManagedAction 唯一会报告的失败是"没有连到本
			// 副本"——从不是动作本身被拒绝，那只能通过下面的状态 GET 端点
			// 观察到（子任务二"不设专门 ack"的设计，与模型拉取相同）。
			// 404 在两种情况下都是诚实的答案：本副本没有可以下发的对象。
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, triggerResponse{Status: "dispatched"})
	})

	mux.HandleFunc("POST /internal/v1/nodes/{node_id}/comfyui-managed/custom-nodes", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body customNodeInstallRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		// Not gated by HasActiveJob: an install only writes files into the
		// container's custom-nodes directory, it does not touch the running
		// ComfyUI process — see this package's doc comment.
		if err := cfg.TriggerCustomNodeInstall(r.PathValue("node_id"), body.Name); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, triggerResponse{Status: "dispatched"})
	})

	mux.HandleFunc("GET /internal/v1/nodes/{node_id}/comfyui-managed", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		statuses, ok := cfg.Status(r.PathValue("node_id"))
		if !ok {
			writeError(w, http.StatusNotFound, "node is not connected to this replica")
			return
		}
		writeJSON(w, http.StatusOK, statusResponse{
			GeneratedAt: clock.Now().UTC().Format(rfc3339Millis),
			Instances:   renderStatuses(statuses),
		})
	})

	return mux, nil
}

// triggerRequest is POST /internal/v1/nodes/{node_id}/comfyui-managed's body.
//
// triggerRequest 是 POST /internal/v1/nodes/{node_id}/comfyui-managed 的请求体。
type triggerRequest struct {
	Action string `json:"action"`
}

// customNodeInstallRequest is POST
// /internal/v1/nodes/{node_id}/comfyui-managed/custom-nodes's body.
//
// customNodeInstallRequest 是 POST
// /internal/v1/nodes/{node_id}/comfyui-managed/custom-nodes 的请求体。
type customNodeInstallRequest struct {
	Name string `json:"name"`
}

// parseAction renders comfyuimanagedstatus.Action's closed vocabulary as the
// lowercase strings this API accepts on the wire, the same allowlist
// discipline renderStatuses applies in the other direction. An empty or
// unrecognized string is rejected rather than silently mapped to
// ActionUnspecified, so a typo in the request body is a 400, not a no-op
// that reports success.
func parseAction(s string) (comfyuimanagedstatus.Action, bool) {
	switch s {
	case "start":
		return comfyuimanagedstatus.ActionStart, true
	case "stop":
		return comfyuimanagedstatus.ActionStop, true
	case "restart":
		return comfyuimanagedstatus.ActionRestart, true
	default:
		return comfyuimanagedstatus.ActionUnspecified, false
	}
}

// triggerResponse only confirms the trigger reached the wire — never that
// the Agent actually applied it. See the handler's comment.
//
// triggerResponse 只确认触发已经发上隧道——从不确认 Agent 真的执行了它。见
// handler 里的注释。
type triggerResponse struct {
	Status string `json:"status"`
}

// rfc3339Millis is the timestamp format every field in this package's
// responses uses, matching modelpullapi's.
//
// rfc3339Millis 是本包响应里每一个时间字段使用的格式，与 modelpullapi 一致。
const rfc3339Millis = "2006-01-02T15:04:05.000Z07:00"

// statusResponse is GET /internal/v1/nodes/{node_id}/comfyui-managed's body.
//
// statusResponse 是 GET /internal/v1/nodes/{node_id}/comfyui-managed 的响应体。
type statusResponse struct {
	GeneratedAt string               `json:"generated_at"`
	Instances   []instanceStatusJSON `json:"instances"`
}

// instanceStatusJSON is one Managed instance's rendered status. It exists so
// comfyuimanagedstatus.State's Stringer output, not its bare int value,
// crosses this API — the same allowlist-rendering discipline
// modelpullapi.pullStatusJSON follows.
//
// instanceStatusJSON 是一个 Managed 实例的渲染后状态。它的存在是为了让
// comfyuimanagedstatus.State 的 Stringer 输出、而不是它的裸整数值，出现在
// 这个 API 上——与 modelpullapi.pullStatusJSON 同一种允许列表渲染纪律。
type instanceStatusJSON struct {
	ContainerName string           `json:"container_name"`
	State         string           `json:"state"`
	UpdatedAt     string           `json:"updated_at"`
	CustomNodes   []customNodeJSON `json:"custom_nodes,omitempty"`
}

// customNodeJSON is one custom node this Agent found installed, as read
// back from the container (STATUS.md's P2 ComfyUI Managed Docker
// deployment, subtask 4).
//
// customNodeJSON 是本 Agent 从容器里读回发现的一个已安装自定义节点
// （STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务四）。
type customNodeJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func renderStatuses(statuses []comfyuimanagedstatus.Status) []instanceStatusJSON {
	out := make([]instanceStatusJSON, len(statuses))
	for i, st := range statuses {
		out[i] = instanceStatusJSON{
			ContainerName: st.ContainerName,
			State:         st.State.String(),
			UpdatedAt:     st.UpdatedAt.UTC().Format(rfc3339Millis),
			CustomNodes:   renderCustomNodes(st.CustomNodes),
		}
	}
	return out
}

func renderCustomNodes(nodes []comfyuimanagedstatus.CustomNodeStatus) []customNodeJSON {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]customNodeJSON, len(nodes))
	for i, n := range nodes {
		out[i] = customNodeJSON{Name: n.Name, Version: n.Version}
	}
	return out
}

// authorized compares the presented token in constant time, the same check
// modelpullapi.authorized performs.
//
// authorized 以常数时间比较出示的 token，与 modelpullapi.authorized 同一检查。
func authorized(r *http.Request, expected string) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(expected)) == 1
}

// writeJSON writes one response, uncacheable: it answers about a live
// node's current state, and a cached copy of it is a wrong answer with a
// timestamp.
//
// writeJSON 写出一个响应，且不可缓存：它回答的是一个活动节点此刻的状态，而
// 它的缓存副本是一个带着时间戳的错误答案。
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
