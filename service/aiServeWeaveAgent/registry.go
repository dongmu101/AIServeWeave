package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/anthropic"
	"AIServeWeave/common/runtime/hosted"
	"AIServeWeave/common/runtime/ollama"
	"AIServeWeave/common/runtime/sglang"
	"AIServeWeave/common/runtime/tokenspeed"
	"AIServeWeave/common/runtime/trtllm"
	"AIServeWeave/common/runtime/vllm"
	"AIServeWeave/common/runtime/workflow/comfyui"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
	"AIServeWeave/service/aiServeWeaveAgent/cliagent"
	"AIServeWeave/service/aiServeWeaveAgent/codexcli"
	"AIServeWeave/service/aiServeWeaveAgent/geminicli"
)

// newRegistry returns a runtime registry with every supported inference
// backend registered. Local CLI and hosted provider IDs must be declared
// locally before they can be created. A hosted provider additionally keeps
// its local endpoint and credentials: a configuration pushed by a Gateway
// may tune timeouts but never redirect a local key to another URL.
//
// newRegistry 注册所有支持的后端。本机 CLI 与托管供应商的 ID 必须先在本地声明
// 才允许创建。托管供应商还始终保留本地声明的端点与凭据：Gateway 下发的配置
// 可以调整超时，但绝不能把本地 Key 转发到别的 URL。
func newRegistry(declared []runtime.Config) (runtime.Registry, error) {
	return newReloadableRegistry(newLocalDeclarations(declared))
}

// localDeclarations is the set of locally declared CLI and hosted runtimes
// the registry's factories admit. It is shared rather than copied so a
// runtime reload (see runtimeReloader) can declare a new CLI account or
// provider without rebuilding the registry.
//
// localDeclarations 是注册表工厂所接受的、本地声明的 CLI 与托管运行时集合。它被共享
// 而不是复制，使运行时重新加载（见 runtimeReloader）无需重建注册表就能声明新的 CLI 账户
// 或供应商。
type localDeclarations struct {
	mu   sync.RWMutex
	byID map[string]runtime.Config
}

// newLocalDeclarations returns the declarations among declared that need one.
// newLocalDeclarations 返回 declared 中需要本地声明的那些。
func newLocalDeclarations(declared []runtime.Config) *localDeclarations {
	d := &localDeclarations{}
	d.set(declared)
	return d
}

// set replaces the declarations with those among declared.
// set 用 declared 中需要本地声明的那些替换当前声明。
func (d *localDeclarations) set(declared []runtime.Config) {
	byID := make(map[string]runtime.Config)
	for _, cfg := range declared {
		if cfg.Kind.IsLocalCLI() || cfg.Kind.IsHosted() {
			byID[cfg.ID] = cfg
		}
	}
	d.mu.Lock()
	d.byID = byID
	d.mu.Unlock()
}

// get returns the local declaration for id.
// get 返回 id 的本地声明。
func (d *localDeclarations) get(id string) (runtime.Config, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cfg, ok := d.byID[id]
	return cfg, ok
}

// newReloadableRegistry is newRegistry over a declaration set the caller keeps
// and may later replace.
// newReloadableRegistry 是基于调用方持有、之后可替换的声明集合的 newRegistry。
func newReloadableRegistry(local *localDeclarations) (runtime.Registry, error) {
	registry := runtime.NewRegistry()
	// localOnly admits only IDs declared locally with the same kind, and
	// always takes the account directory from that declaration: it is local
	// only, so a configuration pushed by a Gateway can neither set nor move it.
	// localOnly 只接受在本地以相同种类声明过的 ID，并始终从该声明取账户目录：它仅限
	// 本地，Gateway 下发的配置既不能设置也不能改动它。
	localOnly := func(build runtime.Factory) runtime.Factory {
		return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
			own, ok := local.get(cfg.ID)
			if !ok || own.Kind != cfg.Kind {
				return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, RuntimeID: cfg.ID, Kind: cfg.Kind,
					Message: "runtime kind must be declared in the local Agent configuration"}
			}
			cfg.AccountDir = own.AccountDir
			return build(cfg, deps)
		}
	}
	// hostedLocal also pins endpoint, key, headers, TLS and the model
	// catalogue to the local declaration; a pushed configuration may still
	// tune timeouts, concurrency and capability overrides, which cannot move
	// a request off the declared host.
	// hostedLocal 还把端点、Key、Headers、TLS 与模型目录固定为本地声明的值；
	// 下发的配置仍可调整超时、并发与能力覆盖，这些都无法让请求离开已声明的主机。
	hostedLocal := func(build runtime.Factory) runtime.Factory {
		return localOnly(func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
			own, _ := local.get(cfg.ID)
			if !sameEndpoint(cfg.Normalize().BaseURL, own.Normalize().BaseURL) {
				return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, RuntimeID: cfg.ID, Kind: cfg.Kind,
					Message: "hosted runtime base_url must match the local Agent configuration"}
			}
			cfg.BaseURL, cfg.APIKey, cfg.Headers, cfg.TLS, cfg.Models = own.BaseURL, own.APIKey, own.Headers, own.TLS, own.Models
			return build(cfg, deps)
		})
	}
	factories := map[runtime.Kind]runtime.Factory{
		runtime.KindOllama:     ollama.New,
		runtime.KindVLLM:       vllm.New,
		runtime.KindSGLang:     sglang.New,
		runtime.KindTRTLLM:     trtllm.New,
		runtime.KindTokenSpeed: tokenspeed.New,
		runtime.KindComfyUI:    comfyui.New,
		runtime.KindClaude:     localOnly(claudecode.New),
		runtime.KindCodex:      localOnly(codexcli.New),
		runtime.KindGeminiCLI:  localOnly(geminicli.New),
	}
	for _, kind := range cliagent.Kinds() {
		factories[kind] = localOnly(cliagent.New)
	}
	for _, kind := range hosted.Kinds() {
		factories[kind] = hostedLocal(hosted.New)
	}
	for _, kind := range []runtime.Kind{runtime.KindAnthropic, runtime.KindAnthropicCompatible} {
		factories[kind] = hostedLocal(anthropic.New)
	}
	for kind, factory := range factories {
		if err := registry.Register(kind, factory); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// sameEndpoint compares two base URLs ignoring scheme and host case, a
// default port and a trailing slash. Unparsable URLs never match. The result
// only decides whether a pushed configuration is refused; the URL actually
// used is always the local one.
//
// sameEndpoint 比较两个 base URL，忽略协议与主机名的大小写、默认端口以及末尾
// 斜杠。无法解析的 URL 永不相等。结果只决定是否拒绝下发的配置；实际使用的 URL
// 始终是本地声明的那一个。
func sameEndpoint(a, b string) bool {
	canonical := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", false
		}
		scheme, port := strings.ToLower(u.Scheme), u.Port()
		if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
			port = ""
		}
		host := strings.ToLower(u.Hostname())
		if port != "" {
			host = net.JoinHostPort(host, port)
		}
		return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), true
	}
	ca, okA := canonical(a)
	cb, okB := canonical(b)
	return okA && okB && ca == cb
}

// newDependencies builds the production dependency set: the real wall clock,
// a shared HTTP client, the WebSocket dialer the ComfyUI adapter needs, and
// the metrics sink shared with the tunnel.
//
// newDependencies 构建生产用的依赖集合：真实墙钟、共享的 HTTP 客户端、ComfyUI 适配器
// 所需的 WebSocket 拨号器，以及与隧道共用的指标下沉端。
func newDependencies(logger *slog.Logger, sink runtime.Metrics) runtime.Dependencies {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: dialTimeout}).DialContext
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	httpClient := &http.Client{Transport: transport}
	return runtime.Dependencies{
		HTTPClient: httpClient,
		WSDialer:   comfyui.NewDialer(httpClient),
		Clock:      runtime.NewSystemClock(),
		Logger:     logger,
		Metrics:    sink,
	}
}
