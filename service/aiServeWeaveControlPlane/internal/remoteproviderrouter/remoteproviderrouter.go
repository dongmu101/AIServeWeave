// Package remoteproviderrouter forwards sealed remote provider bundles, and
// reads of each node's certificate key and report, to every configured
// Gateway replica's -remote-providers-addr listener, the same fan-out
// modelpullrouter uses: each replica answers for the nodes connected to it
// and says 404 for the rest.
//
// The bundle is already sealed to the node key when it reaches this package,
// so neither this package nor the Gateway ever handles an API key in the
// clear. A node connected to several replicas receives the same version from
// each; the Agent applies it once.
//
// remoteproviderrouter 包把已封装的远程供应商打包，以及对节点证书公钥与回报的读取，转发
// 给每个已配置 Gateway 副本的 -remote-providers-addr 监听器，扇出方式与 modelpullrouter
// 相同：每个副本为连到自己的节点作答，其余回答 404。
//
// 打包到达本包时已按节点密钥封装，因此本包与 Gateway 都从不接触明文 API Key。连到多个副本
// 的节点会从每个副本收到同一版本；Agent 只应用一次。
package remoteproviderrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

// ErrDisabled is returned when remote provider forwarding is not configured.
//
// ErrDisabled 在远程供应商转发未配置时返回。
var ErrDisabled = errors.New("remoteproviderrouter: remote provider forwarding is not configured")

// DefaultTimeout bounds one call to one replica.
//
// DefaultTimeout 限制对单个副本的单次调用。
const DefaultTimeout = 3 * time.Second

// maxResponseBytes bounds one replica's status response: a certificate key
// and at most remoteprovider.MaxProviders runtime results.
//
// maxResponseBytes 限制单个副本的状态响应：一个证书公钥加上至多
// remoteprovider.MaxProviders 条运行时结果。
const maxResponseBytes = 64 << 10

// Config configures a Router.
//
// Config 配置一个 Router。
type Config struct {
	// Gateways are the base URLs of each replica's -remote-providers-addr
	// listener, e.g. http://gateway-1:8094.
	//
	// Gateways 是各副本 -remote-providers-addr 监听器的基础 URL，例如
	// http://gateway-1:8094。
	Gateways []string
	// Token must match each Gateway's AISW_GATEWAY_REMOTE_PROVIDERS_TOKEN.
	//
	// Token 必须与各 Gateway 的 AISW_GATEWAY_REMOTE_PROVIDERS_TOKEN 一致。
	Token   string
	Timeout time.Duration
	Client  *http.Client
}

// Router asks every configured replica.
//
// Router 询问每个已配置的副本。
type Router struct {
	gateways []string
	token    string
	client   *http.Client
}

// New builds a Router, or nil when no replicas are configured; every method
// on a nil *Router returns ErrDisabled.
//
// New 构造一个 Router；未配置副本时返回 nil，nil *Router 上的每个方法都返回 ErrDisabled。
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

// ReplicaStatus is one replica's answer. Error is "unreachable", "timeout",
// "unauthorized" or "malformed" when the replica could not be asked, and
// empty when it answered, including "not connected here".
//
// ReplicaStatus 是单个副本的作答。副本无法被询问时 Error 为 "unreachable"、"timeout"、
// "unauthorized" 或 "malformed"；副本作答时（包括"没有连到这里"）为空。
type ReplicaStatus struct {
	Endpoint  string
	Connected bool
	Error     string
}

// RuntimeResult is one provisioned runtime's state as the Agent reported it.
//
// RuntimeResult 是 Agent 回报的一个下发运行时的状态。
type RuntimeResult struct {
	ID    string
	State string
}

// Report is an Agent's last remote provider report, as relayed by one
// replica. Outcome and State use remoteprovider's String forms.
//
// Report 是 Agent 最后一次远程供应商回报，由某个副本转述。Outcome 与 State 使用
// remoteprovider 的 String 形式。
type Report struct {
	AppliedVersion   uint64
	AttemptedVersion uint64
	Outcome          string
	Runtimes         []RuntimeResult
	UpdatedAt        time.Time
}

// Result is the answer to Push or Status.
//
// Result 是 Push 或 Status 的答案。
type Result struct {
	// Connected is true when at least one replica has the node.
	// Connected 在至少一个副本持有该节点时为 true。
	Connected bool
	// NodeKeys are the distinct certificate keys (DER SubjectPublicKeyInfo)
	// the connected replicas saw, freshest report's replica first. More than
	// one appears while a renewed certificate has not reached every replica;
	// the caller picks the one the Registry vouches for. Nil for Push.
	//
	// NodeKeys 是已连接副本看到的不同证书公钥（DER SubjectPublicKeyInfo），回报最新的副本
	// 排在最前。续期后的证书还没到达每个副本时会出现多个；由调用方挑出 Registry 认可的那个。
	// Push 时为 nil。
	NodeKeys [][]byte
	// Report is the freshest report among the connected replicas; nil when
	// none has one, and always nil for Push.
	//
	// Report 是已连接副本中最新的回报；都没有时为 nil，Push 时恒为 nil。
	Report   *Report
	Replicas []ReplicaStatus
}

// Push sends a sealed bundle to nodeID through every replica that has it.
//
// Push 经每个持有 nodeID 的副本发送一份已封装的打包。
func (r *Router) Push(ctx context.Context, nodeID string, version uint64, sealed []byte) (Result, error) {
	if r == nil {
		return Result{}, ErrDisabled
	}
	payload, err := json.Marshal(pushWire{Version: version, Sealed: sealed})
	if err != nil {
		return Result{}, fmt.Errorf("remoteproviderrouter: encoding push: %w", err)
	}
	outcomes := r.fanOut(ctx, func(ctx context.Context, endpoint string) outcome {
		return r.pushOne(ctx, endpoint, nodeID, payload)
	})
	return build(outcomes), nil
}

// Status reads nodeID's certificate keys and freshest report.
//
// Status 读取 nodeID 的证书公钥与最新回报。
func (r *Router) Status(ctx context.Context, nodeID string) (Result, error) {
	if r == nil {
		return Result{}, ErrDisabled
	}
	outcomes := r.fanOut(ctx, func(ctx context.Context, endpoint string) outcome {
		return r.statusOne(ctx, endpoint, nodeID)
	})
	return build(outcomes), nil
}

type outcome struct {
	endpoint  string
	connected bool
	failure   string
	nodeKey   []byte
	report    *Report
}

func (r *Router) fanOut(ctx context.Context, ask func(ctx context.Context, endpoint string) outcome) []outcome {
	outcomes := make([]outcome, len(r.gateways))
	var wg sync.WaitGroup
	for i, endpoint := range r.gateways {
		wg.Go(func() { outcomes[i] = ask(ctx, endpoint) })
	}
	wg.Wait()
	return outcomes
}

// pushWire mirrors remoteprovidersapi.PushRequest.
// pushWire 与 remoteprovidersapi.PushRequest 形状一致。
type pushWire struct {
	Version uint64 `json:"version"`
	Sealed  []byte `json:"sealed"`
}

// statusWire mirrors remoteprovidersapi.StatusResponse.
// statusWire 与 remoteprovidersapi.StatusResponse 形状一致。
type statusWire struct {
	NodeKey []byte `json:"node_key"`
	Report  *struct {
		AppliedVersion   uint64 `json:"applied_version"`
		AttemptedVersion uint64 `json:"attempted_version"`
		Outcome          string `json:"outcome"`
		Runtimes         []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"runtimes"`
		UpdatedAt string `json:"updated_at"`
	} `json:"report"`
}

func (r *Router) pushOne(ctx context.Context, endpoint, nodeID string, payload []byte) outcome {
	resp, failure := r.do(ctx, http.MethodPost, endpoint, nodeID, payload)
	if failure != "" {
		return outcome{endpoint: endpoint, failure: failure}
	}
	defer func() { _ = resp.Body.Close() }()
	drain(resp.Body)
	switch resp.StatusCode {
	case http.StatusAccepted:
		return outcome{endpoint: endpoint, connected: true}
	case http.StatusNotFound:
		return outcome{endpoint: endpoint}
	case http.StatusUnauthorized:
		return outcome{endpoint: endpoint, failure: "unauthorized"}
	default:
		return outcome{endpoint: endpoint, failure: "unreachable"}
	}
}

func (r *Router) statusOne(ctx context.Context, endpoint, nodeID string) outcome {
	resp, failure := r.do(ctx, http.MethodGet, endpoint, nodeID, nil)
	if failure != "" {
		return outcome{endpoint: endpoint, failure: failure}
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		drain(resp.Body)
		return outcome{endpoint: endpoint}
	case http.StatusUnauthorized:
		return outcome{endpoint: endpoint, failure: "unauthorized"}
	default:
		return outcome{endpoint: endpoint, failure: "unreachable"}
	}
	var wire statusWire
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&wire); err != nil {
		return outcome{endpoint: endpoint, failure: "malformed"}
	}
	o := outcome{endpoint: endpoint, connected: true, nodeKey: wire.NodeKey}
	if w := wire.Report; w != nil {
		rep := &Report{AppliedVersion: w.AppliedVersion, AttemptedVersion: w.AttemptedVersion, Outcome: w.Outcome}
		rep.UpdatedAt, _ = time.Parse(time.RFC3339Nano, w.UpdatedAt)
		for _, rt := range w.Runtimes {
			rep.Runtimes = append(rep.Runtimes, RuntimeResult{ID: rt.ID, State: rt.State})
		}
		o.report = rep
	}
	return o
}

// do sends one request, or returns the failure code for a transport error.
// do 发送一次请求，传输出错时返回失败代号。
func (r *Router) do(ctx context.Context, method, endpoint, nodeID string, payload []byte) (*http.Response, string) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path(nodeID), body)
	if err != nil {
		return nil, "unreachable"
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		var timeout interface{ Timeout() bool }
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			return nil, "timeout"
		}
		return nil, "unreachable"
	}
	return resp, ""
}

// build folds the replicas' answers: the freshest report wins, and its
// replica's key leads NodeKeys.
//
// build 折叠各副本的作答：最新的回报胜出，其副本的公钥排在 NodeKeys 最前。
func build(outcomes []outcome) Result {
	result := Result{Replicas: make([]ReplicaStatus, len(outcomes))}
	connected := make([]outcome, 0, len(outcomes))
	for i, o := range outcomes {
		result.Replicas[i] = ReplicaStatus{Endpoint: o.endpoint, Connected: o.connected, Error: o.failure}
		if o.connected {
			result.Connected = true
			connected = append(connected, o)
		}
	}
	slices.SortStableFunc(connected, func(a, b outcome) int {
		return reportTime(b).Compare(reportTime(a))
	})
	for _, o := range connected {
		if result.Report == nil && o.report != nil {
			result.Report = o.report
		}
		if len(o.nodeKey) > 0 && !slices.ContainsFunc(result.NodeKeys, func(k []byte) bool { return bytes.Equal(k, o.nodeKey) }) {
			result.NodeKeys = append(result.NodeKeys, o.nodeKey)
		}
	}
	return result
}

func reportTime(o outcome) time.Time {
	if o.report == nil {
		return time.Time{}
	}
	return o.report.UpdatedAt
}

// path matches remoteprovidersapi's route.
// path 与 remoteprovidersapi 的路由一致。
func path(nodeID string) string {
	return "/internal/v1/nodes/" + url.PathEscape(nodeID) + "/remote-providers"
}

func drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseBytes))
}
