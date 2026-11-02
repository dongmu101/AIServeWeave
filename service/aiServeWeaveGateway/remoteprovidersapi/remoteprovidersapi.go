// Package remoteprovidersapi is the Gateway's operator entry point for hosted
// provider runtimes the control plane provisions on a node: relay a sealed
// bundle to a connected node, and read back the node's certificate key and
// its last report.
//
// It is a separate listener with its own token, for the reason modelpullapi
// gives: a leaked token here lets the holder push bundles at nodes, which is a
// different blast radius from reading the inventory or triggering a model
// pull. The bundle itself is opaque to this package — an HPKE ciphertext only
// the target Agent can open (common/remoteprovider) — so this listener never
// sees an API key, and a caller cannot make it alter one.
//
// remoteprovidersapi 包是 Gateway 面向控制面下发托管供应商运行时的运维入口：把已封装的
// 打包转发给已连接的节点，并读回该节点的证书公钥与最后一次回报。
//
// 它是带独立 token 的独立监听器，理由与 modelpullapi 相同：这里的 token 泄漏后，持有者
// 能向节点推送打包，爆炸半径不同于读清单或触发模型拉取。打包本身对本包不透明——是只有
// 目标 Agent 能打开的 HPKE 密文（common/remoteprovider）——因此这个监听器从不接触 API
// Key，调用方也无法让它改动 Key。
package remoteprovidersapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"AIServeWeave/common/remoteprovider"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// maxRequestBytes bounds a push body: a sealed bundle, base64-encoded, plus
// its version.
//
// maxRequestBytes 限制一次推送的请求体：base64 编码后的密文加上版本号。
const maxRequestBytes = remoteprovider.MaxBundleBytes*2 + 4096

// rfc3339Millis is the timestamp format of every response field.
//
// rfc3339Millis 是所有响应字段使用的时间格式。
const rfc3339Millis = "2006-01-02T15:04:05.000Z07:00"

var (
	errNoToken  = errors.New("remoteprovidersapi: a token is required; set AISW_GATEWAY_REMOTE_PROVIDERS_TOKEN")
	errNoSource = errors.New("remoteprovidersapi: Send and Status are both required")
)

// Config configures the remote providers listener's handler.
//
// Config 配置远程供应商监听器的 handler。
type Config struct {
	// Token authenticates the control plane. An empty token is refused at
	// construction.
	//
	// Token 认证控制面。空 token 在构造时就被拒绝。
	Token string
	// Send is tunnelserver.Server.SendRemoteProviders.
	// Send 即 tunnelserver.Server.SendRemoteProviders。
	Send func(nodeID string, version uint64, sealed []byte) error
	// Status is tunnelserver.Server.RemoteProvidersStatus.
	// Status 即 tunnelserver.Server.RemoteProvidersStatus。
	Status func(nodeID string) (tunnelserver.RemoteProvidersState, bool)
	Clock  runtime.Clock
}

// New builds the handler, or reports why it cannot.
//
// New 构造 handler，或说明为什么构造不了。
func New(cfg Config) (http.Handler, error) {
	if cfg.Token == "" {
		return nil, errNoToken
	}
	if cfg.Send == nil || cfg.Status == nil {
		return nil, errNoSource
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/nodes/{node_id}/remote-providers", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body PushRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Version == 0 || len(body.Sealed) == 0 {
			writeError(w, http.StatusBadRequest, "version and sealed are required")
			return
		}
		if err := cfg.Send(r.PathValue("node_id"), body.Version, body.Sealed); err != nil {
			writeError(w, http.StatusNotFound, "node is not connected to this replica")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "dispatched"})
	})

	mux.HandleFunc("GET /internal/v1/nodes/{node_id}/remote-providers", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.Token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		state, ok := cfg.Status(r.PathValue("node_id"))
		if !ok {
			writeError(w, http.StatusNotFound, "node is not connected to this replica")
			return
		}
		resp := StatusResponse{GeneratedAt: clock.Now().UTC().Format(rfc3339Millis), NodeKey: state.NodeKey}
		if state.HasReport {
			resp.Report = renderReport(state.Report)
		}
		writeJSON(w, http.StatusOK, resp)
	})
	return mux, nil
}

// PushRequest is the POST body. Sealed is base64 in JSON.
//
// PushRequest 是 POST 请求体。Sealed 在 JSON 中为 base64。
type PushRequest struct {
	Version uint64 `json:"version"`
	Sealed  []byte `json:"sealed"`
}

// StatusResponse is the GET body. NodeKey is the DER SubjectPublicKeyInfo of
// the node's newest Control stream certificate, base64 in JSON; Report is
// absent until the Agent has reported.
//
// StatusResponse 是 GET 响应体。NodeKey 是节点最新 Control 流证书的 DER
// SubjectPublicKeyInfo，在 JSON 中为 base64；Agent 回报之前 Report 缺席。
type StatusResponse struct {
	GeneratedAt string      `json:"generated_at"`
	NodeKey     []byte      `json:"node_key,omitempty"`
	Report      *ReportJSON `json:"report,omitempty"`
}

// ReportJSON renders remoteprovider.Report with string enums.
//
// ReportJSON 以字符串枚举渲染 remoteprovider.Report。
type ReportJSON struct {
	AppliedVersion   uint64        `json:"applied_version"`
	AttemptedVersion uint64        `json:"attempted_version"`
	Outcome          string        `json:"outcome"`
	Runtimes         []RuntimeJSON `json:"runtimes"`
	UpdatedAt        string        `json:"updated_at,omitempty"`
}

// RuntimeJSON is one provisioned runtime's id and state.
//
// RuntimeJSON 是一个下发运行时的 id 与状态。
type RuntimeJSON struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func renderReport(r remoteprovider.Report) *ReportJSON {
	out := &ReportJSON{
		AppliedVersion:   r.AppliedVersion,
		AttemptedVersion: r.AttemptedVersion,
		Outcome:          r.Outcome.String(),
		Runtimes:         make([]RuntimeJSON, 0, len(r.Runtimes)),
	}
	if !r.UpdatedAt.IsZero() {
		out.UpdatedAt = r.UpdatedAt.UTC().Format(rfc3339Millis)
	}
	for _, rt := range r.Runtimes {
		out.Runtimes = append(out.Runtimes, RuntimeJSON{ID: rt.ID, State: rt.State.String()})
	}
	return out
}

// authorized compares the bearer token in constant time.
//
// authorized 以常数时间比较 bearer token。
func authorized(r *http.Request, expected string) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(expected)) == 1
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
