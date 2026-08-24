// Command aiserveweave-agent runs the node-side agent: it manages the local
// inference runtimes (Ollama, vLLM, SGLang, ComfyUI) and will dial out to the
// Gateway over the tunnel. The agent never listens on a public port.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/ollama"
	"AIServeWeave/common/runtime/sglang"
	"AIServeWeave/common/runtime/vllm"
	"AIServeWeave/common/runtime/workflow/comfyui"
	"AIServeWeave/service/aiServeWeaveAgent/localdiscovery"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

const (
	// agentVersion is reported in the tunnel handshake and to the Registry.
	// It is a constant until the build stamps a real version in.
	agentVersion = "dev"

	// shutdownTimeout bounds how long the agent waits for in-flight runtime
	// work to wind down before exiting anyway.
	shutdownTimeout = 15 * time.Second

	// dialTimeout and responseHeaderTimeout bound how long the agent waits to
	// reach a local backend and to see its response headers. They are set on
	// the transport rather than as http.Client.Timeout on purpose: a client
	// deadline also covers reading the body, which would truncate long-lived
	// SSE streams. Per-request deadlines come from the caller's context.
	dialTimeout           = 10 * time.Second
	responseHeaderTimeout = 60 * time.Second
)

func main() {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	opts := registerTunnelFlags()
	ollamaURL := flag.String("ollama-url", "",
		"base URL of a local Ollama instance to register, e.g. http://127.0.0.1:11434; empty registers no runtime")
	ollamaID := flag.String("ollama-id", "ollama", "runtime id to register the Ollama instance under")
	autoDiscover := flag.Bool("auto-discover", true,
		"probe 127.0.0.1's well-known Ollama/vLLM ports and register whatever answers; never touches any other host")
	autoDiscoverInterval := flag.Duration("auto-discover-interval", localdiscovery.DefaultInterval,
		"how often auto-discovery looks for a newly appeared local instance")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9091",
		"address the Prometheus /metrics listener binds; loopback by default because the agent never listens on a public port, empty disables it")
	flag.Parse()

	logger, err := newLogger(*logLevel)
	if err != nil {
		// The logger is not usable yet, so report the failure directly.
		os.Stderr.WriteString("agent: " + err.Error() + "\n")
		os.Exit(2)
	}

	if err := run(logger, opts, *ollamaURL, *ollamaID, *autoDiscover, *autoDiscoverInterval, *metricsAddr); err != nil {
		logger.Error("agent exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// tunnelOptions is the tunnel's configuration. It comes from flags because
// the agent has no configuration file yet; the `tunnel` section of the file
// described in tunnel/README.md replaces this wholesale once it lands, and
// the flag names deliberately mirror that section's keys.
type tunnelOptions struct {
	gateway         string
	registry        string
	nodeID          string
	certFile        string
	keyFile         string
	caFile          string
	bootstrapToken  string
	allowedRuntimes string
	labels          string
	maxGateways     int
}

func registerTunnelFlags() *tunnelOptions {
	opts := &tunnelOptions{}
	flag.StringVar(&opts.gateway, "gateway", "",
		"comma-separated seed list of gateway replicas as host:port; empty disables the tunnel. "+
			"Only one has to be reachable: the roster supplies the rest")
	flag.IntVar(&opts.maxGateways, "max-gateways", 0, "upper bound on simultaneous gateway tunnels (default 16)")
	flag.StringVar(&opts.registry, "registry", "", "registry endpoint for node certificate issuance, as host:port")
	flag.StringVar(&opts.nodeID, "node-id", "", "node identity; empty lets the registry assign one")
	flag.StringVar(&opts.certFile, "cert-file", "", "node certificate path, mode 0600")
	flag.StringVar(&opts.keyFile, "key-file", "", "node private key path, mode 0600")
	flag.StringVar(&opts.caFile, "ca-file", "", "registry CA bundle path")
	flag.StringVar(&opts.bootstrapToken, "bootstrap-token-file", "", "one-time registration token path, deleted after use")
	flag.StringVar(&opts.allowedRuntimes, "allowed-runtimes", "",
		"comma-separated runtime ids the gateway may dispatch to; empty means every configured runtime")
	flag.StringVar(&opts.labels, "labels", "",
		"comma-separated key=value facts about this node for the gateway's routing rules, e.g. region=local,gpu=4090")
	return opts
}

// nodeLabels parses -labels into the map Hello carries.
//
// A malformed entry is dropped rather than fatal: labels express a routing
// preference, and refusing to start over a typo in one would take a working
// node offline for something that only affects where requests prefer to go.
// The Agent logs what it parsed, so the typo is still visible.
//
// nodeLabels 把 -labels 解析成 Hello 所携带的映射。
//
// 格式错误的条目被丢弃而不是致命错误：标签表达的是路由偏好，为其中一个的笔误而拒绝
// 启动，会为「只影响请求偏好去哪」的事情让一个本来能工作的节点下线。Agent 会记录它
// 解析出了什么，因此那个笔误依然可见。
func (o *tunnelOptions) nodeLabels() map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(o.labels, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// enabled reports whether the operator asked for a tunnel at all.
func (o *tunnelOptions) enabled() bool { return len(o.seeds()) > 0 }

// seeds parses the seed endpoint list. The roster replaces it as soon as one
// replica answers, so it only has to contain one address that works.
func (o *tunnelOptions) seeds() []string {
	var endpoints []string
	for _, endpoint := range strings.Split(o.gateway, ",") {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

// runtimeIDs parses the allowlist. An empty list means no local narrowing,
// which is only safe because the agent currently configures no runtimes at
// all; once runtimes are loaded from the config file, main must pass every
// configured id here so the allowlist is a real allowlist.
func (o *tunnelOptions) runtimeIDs() []string {
	var ids []string
	for _, id := range strings.Split(o.allowedRuntimes, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// run wires the runtime registry and manager, then blocks until the process is
// signalled to stop. It returns the first error that prevents a clean start or
// a clean shutdown.
//
// ollamaURL and ollamaID are a stand-in for the runtime section of the agent
// config file described in tunnel/README.md: until that file lands, this is
// the only way to give the agent a real backend to dispatch to. An empty
// ollamaURL registers nothing, matching today's behavior.
func run(logger *slog.Logger, opts *tunnelOptions, ollamaURL, ollamaID string, autoDiscover bool, autoDiscoverInterval time.Duration, metricsAddr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry, err := newRegistry()
	if err != nil {
		return err
	}

	// One metrics registry for the node: the runtime layer and the tunnel
	// record into the same sink, which is what makes "the backend is slow"
	// and "the tunnel is slow" comparable numbers rather than two stories.
	//
	// 整个节点共用一个指标注册表：运行时层与隧道记录进同一个下沉端，这才让「后端慢」
	// 与「隧道慢」成为两个可以互相对照的数字，而不是两套说辞。
	metricsRegistry := metrics.New(tunnel.Descriptions())
	metricsServer := startMetricsEndpoint(logger, metricsRegistry, metricsAddr)

	deps := newDependencies(logger, metricsRegistry)
	manager := runtime.NewManager(registry, deps)

	configuredRuntimes := 0
	if ollamaURL != "" {
		if err := manager.Add(ctx, runtime.Config{ID: ollamaID, Kind: runtime.KindOllama, BaseURL: ollamaURL}); err != nil {
			return err
		}
		configuredRuntimes = 1
	}

	// Runtime configuration is not loaded from disk yet, so beyond the
	// Ollama instance above the manager starts with no instances until
	// auto-discovery (below) or the tunnel's config delivery adds one.
	// Declared runtimes will be added here once the agent config file lands.
	logger.Info("agent started",
		slog.Any("supported_kinds", registry.Kinds()),
		slog.Int("configured_runtimes", configuredRuntimes),
		slog.Bool("tunnel_enabled", opts.enabled()),
		slog.Bool("auto_discover", autoDiscover),
	)

	discoveryDone := startLocalDiscovery(ctx, logger, manager, autoDiscover, autoDiscoverInterval)

	tunnelErr, err := startTunnel(ctx, logger, manager, deps.Metrics, opts)
	if err != nil {
		return err
	}

	// Whichever comes first: the operator stopping the agent, or the tunnel
	// giving up for a reason no reconnect can fix. Either way the runtimes are
	// drained the same way, so a fatal tunnel error is carried past the
	// shutdown rather than short-circuiting it.
	var fatal error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining runtimes")
	case err := <-tunnelErr:
		stop()
		if err != nil && !errors.Is(err, tunnel.ErrShutdownRequested) {
			logger.Error("tunnel failed permanently", slog.Any("error", err))
			fatal = err
		} else {
			logger.Info("tunnel closed; shutting down")
		}
	}

	<-discoveryDone // wait for the last scan's Manager.Add calls to finish before Close starts tearing instances down

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := manager.Close(shutdownCtx); err != nil {
		return err
	}
	// Closed after the runtimes, so a scrape landing during the drain still
	// sees the tunnel's connection state fall to zero.
	//
	// 在运行时之后关闭，这样落在排空期间的一次抓取仍能看到隧道连接状态归零。
	if metricsServer != nil {
		_ = metricsServer.Close()
	}
	if fatal != nil {
		return fatal
	}

	logger.Info("agent stopped")
	return nil
}

// startMetricsEndpoint serves the registry on addr, or returns nil when the
// operator disabled it. A failure to listen is logged rather than returned:
// an agent that refuses to start because its metrics port is taken is an
// agent whose node is offline for a reason nobody would guess from the
// symptom.
//
// startMetricsEndpoint 在 addr 上提供注册表内容；运维关闭该端点时返回 nil。监听失败
// 只记录、不返回：一个因为指标端口被占用就拒绝启动的 Agent，会让整个节点因为一个
// 从症状根本猜不到的原因而离线。
func startMetricsEndpoint(logger *slog.Logger, registry *metrics.Registry, addr string) *http.Server {
	if addr == "" {
		logger.Warn("no -metrics-addr; this agent exports no metrics")
		return nil
	}
	server := registry.Server(addr)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics listener stopped", slog.Any("error", err))
		}
	}()
	logger.Info("metrics listening", slog.String("metrics_addr", addr))
	return server
}

// startLocalDiscovery starts localdiscovery.Scanner in the background and
// returns a channel that is closed once its goroutine has exited — after ctx
// is canceled, not before — so the caller can wait for any in-flight
// Manager.Add call to finish before Close starts tearing instances down.
// When autoDiscover is false the channel is returned already closed, so
// waiting on it is always safe regardless of whether discovery is enabled.
func startLocalDiscovery(ctx context.Context, logger *slog.Logger, manager runtime.Manager, autoDiscover bool, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	if !autoDiscover {
		close(done)
		return done
	}
	scanner, err := localdiscovery.New(localdiscovery.Config{
		Manager:  manager,
		Interval: interval,
		Logger:   logger,
	})
	if err != nil {
		// Config is entirely flag-derived and always valid, so this is not
		// reachable in practice; closing done rather than panicking keeps a
		// theoretical failure here from taking the rest of the agent down.
		logger.Error("local discovery did not start", slog.Any("error", err))
		close(done)
		return done
	}
	go func() {
		defer close(done)
		if err := scanner.Run(ctx); err != nil {
			logger.Error("local discovery stopped unexpectedly", slog.Any("error", err))
		}
	}()
	return done
}

// startTunnel wires the connection table and runs it in the background,
// returning the channel its outcome arrives on. A nil channel is returned
// when no gateway is configured, which blocks forever in the select above and
// leaves the agent running its runtimes only.
//
// The seed endpoints only have to get the agent to one reachable replica: the
// roster that comes back over that tunnel supplies the rest, so scaling the
// Gateway out never means restarting an agent.
//
// metrics is the same sink the runtime layer records against: a node has one
// metrics backend, not one per layer. Today it discards, so wiring a real one
// is a change in exactly one place.
func startTunnel(ctx context.Context, logger *slog.Logger, manager runtime.Manager, metrics runtime.Metrics, opts *tunnelOptions) (<-chan error, error) {
	if !opts.enabled() {
		return nil, nil
	}

	caPEM, err := os.ReadFile(opts.caFile)
	if err != nil {
		return nil, err
	}
	connector, err := tunnel.NewGRPCRegistryConnector(opts.registry, caPEM)
	if err != nil {
		return nil, err
	}
	identities, err := tunnel.NewIdentityManager(tunnel.IdentityConfig{
		NodeID:             opts.nodeID,
		RegistryEndpoint:   opts.registry,
		CertFile:           opts.certFile,
		KeyFile:            opts.keyFile,
		CAFile:             opts.caFile,
		BootstrapTokenFile: opts.bootstrapToken,
		AgentVersion:       agentVersion,
		Logger:             logger,
	}, connector)
	if err != nil {
		return nil, err
	}

	// The identity is obtained before the first dial: a node with no valid
	// certificate has nothing to say to a gateway, and failing here gives the
	// operator a clear error instead of an authentication loop. The
	// connection table keeps it rotated from then on.
	identity, err := identities.Ensure(ctx)
	if err != nil {
		return nil, err
	}

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         manager,
		NodeID:          identity.NodeID,
		AllowedRuntimes: opts.runtimeIDs(),
		Metrics:         metrics,
		Logger:          logger,
	})
	if err != nil {
		return nil, err
	}

	tunnels, err := tunnel.NewManager(tunnel.ManagerConfig{
		Client: tunnel.ClientConfig{
			NodeID:          identity.NodeID,
			AgentVersion:    agentVersion,
			Manager:         manager,
			AllowedRuntimes: opts.runtimeIDs(),
			Labels:          opts.nodeLabels(),
			Handler:         dispatcher,
			Metrics:         metrics,
			Logger:          logger,
		},
		SeedEndpoints: opts.seeds(),
		MaxGateways:   opts.maxGateways,
		Identities:    identities,
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- tunnels.Run(ctx) }()
	return done, nil
}

// newRegistry returns a runtime registry with every supported inference
// backend registered.
func newRegistry() (runtime.Registry, error) {
	registry := runtime.NewRegistry()
	factories := map[runtime.Kind]runtime.Factory{
		runtime.KindOllama:  ollama.New,
		runtime.KindVLLM:    vllm.New,
		runtime.KindSGLang:  sglang.New,
		runtime.KindComfyUI: comfyui.New,
	}
	for kind, factory := range factories {
		if err := registry.Register(kind, factory); err != nil {
			return nil, err
		}
	}
	return registry, nil
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

// newLogger returns a JSON logger at the requested level.
func newLogger(level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), nil
}
