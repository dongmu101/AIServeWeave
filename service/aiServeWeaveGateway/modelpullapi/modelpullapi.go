// Package modelpullapi is the Gateway's write entry point for STATUS.md's P2
// model distribution subtask 2: trigger a node's local model pull by name,
// and read back its last-reported status.
//
// It is a separate listener from adminapi's operator inventory on purpose.
// adminapi's own doc comment states its listener "accepts no writes"; a
// route that makes a node start downloading is a write, and folding it into
// that listener would make that sentence false. The two also have different
// abuse consequences if their token leaks: adminapi's leaks a read of the
// fleet inventory, this one's leaks the ability to make any connected node
// start pulling whatever its local manifest already permits. Different
// blast radius, different token, different port — the same reasoning
// adminapi already applies to keeping itself off the public inference
// listener.
//
// Every request here still respects the tunnel's own boundary: a trigger
// only reaches a node connected to this replica (tunnelserver.Server's "no
// forwarding" rule), and it never carries a URL — only a name the Agent
// resolves against its own local manifest. See
// docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md
// for why.
//
// modelpullapi 包是 STATUS.md P2 模型分发子任务二的 Gateway 写入口：按名字触
// 发一个节点的本地模型拉取，并读回它最后一次上报的状态。
//
// 它刻意与 adminapi 的运维清单分处不同的监听器。adminapi 自己的文档写着它的
// 监听器"不接受写操作"；一个能让节点开始下载的路由是写操作，把它塞进那个监
// 听器会让那句话变成假话。两者的 token 泄漏后果也不同：adminapi 泄漏的是读
// 到机群清单，这一个泄漏的是能让任意已连接节点开始拉取它本地清单已经批准的
// 一切。不同的爆炸半径、不同的 token、不同的端口——与 adminapi 自己用来把自
// 己隔离在公开推理监听器之外的理由相同。
//
// 这里的每一次请求仍然遵守隧道自己的边界：触发只能到达连到本副本的节点
// （tunnelserver.Server 的"不转发"规则），且从不携带 URL——只有一个名字，由
// Agent 对着自己本地清单解析。原因见
// docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md。
package modelpullapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"AIServeWeave/common/modelpullstatus"
	"AIServeWeave/common/runtime"
)

// Config configures the model-pull listener's handler.
//
// Config 配置模型拉取监听器的 handler。
type Config struct {
	// Token authenticates the caller. An empty token is refused at
	// construction, the same restraint adminapi.Config.Token documents: an
	// endpoint that can make a node start downloading is not something to
	// serve to anybody who can reach the port.
	//
	// Token 认证调用方。空 token 在构造时就被拒绝，与 adminapi.Config.Token
	// 同一种克制：一个能让节点开始下载的端点，不该提供给谁连上端口就能用。
	Token string
	// Trigger asks nodeID to start pulling names. It is
	// tunnelserver.Server.TriggerModelPull, injected as a function so this
	// package cannot reach anything else on the Server.
	//
	// Trigger 请求 nodeID 开始拉取 names。它就是
	// tunnelserver.Server.TriggerModelPull，以函数形式注入，这样本包够不到
	// Server 上的其他任何东西。
	Trigger func(nodeID string, names []string) error
	// Status reports this replica's last-known model pull status for
	// nodeID, and whether the node is known to this replica at all. It is
	// tunnelserver.Server.ModelPullStatus.
	//
	// Status 报告本副本对 nodeID 最后已知的模型拉取状态，以及本副本是否知道
	// 这个节点。它就是 tunnelserver.Server.ModelPullStatus。
	Status func(nodeID string) ([]modelpullstatus.Status, bool)
	Clock  runtime.Clock
}

// New builds the model-pull handler, or reports why it cannot.
//
// New 构造模型拉取 handler，或说明为什么构造不了。
func New(cfg Config) (http.Handler, error) {
	if cfg.Token == "" {
		return nil, errNoToken
	}
	if cfg.Trigger == nil || cfg.Status == nil {
		return nil, errNoSource
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/nodes/{node_id}/model-pulls", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body triggerRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(body.Names) == 0 {
			writeError(w, http.StatusBadRequest, "names must not be empty")
			return
		}
		if err := cfg.Trigger(r.PathValue("node_id"), body.Names); err != nil {
			// The only failure TriggerModelPull reports is "not connected to
			// this replica" — never a name-level rejection, which the Agent
			// only ever answers through the status report GET below (P2
			// subtask 2's "no dedicated ack" design). 404 is the honest
			// answer either way: this replica has nothing to dispatch to.
			//
			// TriggerModelPull 唯一会报告的失败是"没有连到本副本"——从不是名
			// 字级别的拒绝，那只能通过下面的状态 GET 端点观察到（子任务二
			// "不设专门 ack" 的设计）。404 在两种情况下都是诚实的答案：本副
			// 本没有可以下发的对象。
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, triggerResponse{Status: "dispatched"})
	})

	mux.HandleFunc("GET /internal/v1/nodes/{node_id}/model-pulls", func(w http.ResponseWriter, r *http.Request) {
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
			Pulls:       renderStatuses(statuses),
		})
	})

	return mux, nil
}

// triggerRequest is POST /internal/v1/nodes/{node_id}/model-pulls's body.
//
// triggerRequest 是 POST /internal/v1/nodes/{node_id}/model-pulls 的请求体。
type triggerRequest struct {
	Names []string `json:"names"`
}

// triggerResponse only confirms the trigger reached the wire — never that any
// name was accepted by the Agent's manifest. See the handler's comment.
//
// triggerResponse 只确认触发已经发上隧道——从不确认任何名字被 Agent 的清单接
// 受。见 handler 里的注释。
type triggerResponse struct {
	Status string `json:"status"`
}

// rfc3339Millis is the timestamp format every field in this package's
// responses uses.
//
// rfc3339Millis 是本包响应里每一个时间字段使用的格式。
const rfc3339Millis = "2006-01-02T15:04:05.000Z07:00"

// statusResponse is GET /internal/v1/nodes/{node_id}/model-pulls's body.
// GeneratedAt exists for the same reason adminapi's nodeview.Replica carries
// one: this is a point-in-time view of one replica's last-known status, not
// a live subscription.
//
// statusResponse 是 GET /internal/v1/nodes/{node_id}/model-pulls 的响应体。
// GeneratedAt 的存在理由与 adminapi 的 nodeview.Replica 相同：这是某个副本
// 最后已知状态的一个时间点快照，不是一次实时订阅。
type statusResponse struct {
	GeneratedAt string           `json:"generated_at"`
	Pulls       []pullStatusJSON `json:"pulls"`
}

// pullStatusJSON is one name's rendered status. It exists so
// modelpullstatus.State/FailureReason's Stringer output, not their bare int
// values, crosses this API — the same allowlist-rendering discipline
// adminapi's renderNode follows.
//
// pullStatusJSON 是一个名字的渲染后状态。它的存在是为了让
// modelpullstatus.State/FailureReason 的 Stringer 输出、而不是它们的裸整数
// 值，出现在这个 API 上——与 adminapi 的 renderNode 同一种允许列表渲染纪律。
type pullStatusJSON struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	BytesDownloaded int64  `json:"bytes_downloaded"`
	BytesTotal      int64  `json:"bytes_total"`
	Reason          string `json:"reason,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

func renderStatuses(statuses []modelpullstatus.Status) []pullStatusJSON {
	out := make([]pullStatusJSON, len(statuses))
	for i, st := range statuses {
		var reason string
		if st.State == modelpullstatus.StateFailed {
			reason = st.Reason.String()
		}
		out[i] = pullStatusJSON{
			Name:            st.Name,
			State:           st.State.String(),
			BytesDownloaded: st.BytesDownloaded,
			BytesTotal:      st.BytesTotal,
			Reason:          reason,
			UpdatedAt:       st.UpdatedAt.UTC().Format(rfc3339Millis),
		}
	}
	return out
}

// authorized compares the presented token in constant time, the same check
// adminapi.authorized performs.
//
// authorized 以常数时间比较出示的 token，与 adminapi.authorized 同一检查。
func authorized(r *http.Request, expected string) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(expected)) == 1
}

// writeJSON writes one response, uncacheable: it answers about a live node's
// current activity, and a cached copy of it is a wrong answer with a
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
