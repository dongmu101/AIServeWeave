// Command aiserveweave-agent runs the node-side agent: it manages the local
// inference runtimes (Ollama, vLLM, SGLang, ComfyUI) and will dial out to the
// Gateway over the tunnel. The agent never listens on a public port.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"AIServeWeave/service/aiServeWeaveAgent/comfyuimanaged"
	"AIServeWeave/service/aiServeWeaveAgent/hostresources"
	"AIServeWeave/service/aiServeWeaveAgent/localdiscovery"
	"AIServeWeave/service/aiServeWeaveAgent/modelpull"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// version is reported in the tunnel handshake and to the Registry, and
// printed by -version. It is stamped at build time via
// -ldflags="-X main.version=...", see the root Dockerfile and
// scripts/build-release.sh; "dev" is what a plain `go build` produces.
//
// version 在隧道握手中上报给 Registry，也是 -version 打印的内容。构建时通过
// -ldflags="-X main.version=..." 注入，见根 Dockerfile 与
// scripts/build-release.sh；直接 `go build` 得到的就是 "dev"。
var version = "dev"

const (
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
	mpOpts := registerModelPullFlags()
	cmOpts := registerComfyUIManagedFlags()
	ollamaURL := flag.String("ollama-url", "",
		"base URL of a local Ollama instance to register, e.g. http://127.0.0.1:11434; empty registers no runtime")
	ollamaID := flag.String("ollama-id", "ollama", "runtime id to register the Ollama instance under")
	autoDiscover := flag.Bool("auto-discover", true,
		"probe 127.0.0.1's well-known Ollama/vLLM ports and register whatever answers; never touches any other host")
	autoDiscoverInterval := flag.Duration("auto-discover-interval", localdiscovery.DefaultInterval,
		"how often auto-discovery looks for a newly appeared local instance")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9091",
		"address the Prometheus /metrics listener binds; loopback by default because the agent never listens on a public port, empty disables it")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		os.Stdout.WriteString("aiserveweave-agent " + version + "\n")
		return
	}

	logger, err := newLogger(*logLevel)
	if err != nil {
		// The logger is not usable yet, so report the failure directly.
		os.Stderr.WriteString("agent: " + err.Error() + "\n")
		os.Exit(2)
	}

	if err := run(logger, opts, mpOpts, cmOpts, *ollamaURL, *ollamaID, *autoDiscover, *autoDiscoverInterval, *metricsAddr); err != nil {
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

// modelPullOptions is the configuration for modelpull (STATUS.md's P2 model
// distribution, subtask 1: see
// docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md). It
// comes from flags, is entirely local to this node, and is never accepted
// from the Gateway or control plane. An empty manifest disables the feature.
//
// modelPullOptions 是 modelpull 的配置（STATUS.md P2 模型分发子任务一，见
// docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md）。它来自
// flag，完全是本节点本地的，从不接受 Gateway 或控制面下发。清单为空时功能关闭。
type modelPullOptions struct {
	manifest   string
	allowlist  string
	quotaBytes int64
}

func registerModelPullFlags() *modelPullOptions {
	opts := &modelPullOptions{}
	flag.StringVar(&opts.manifest, "model-pull-manifest", "",
		"path to a JSON manifest of models to fetch and verify at startup; empty disables model pulling")
	flag.StringVar(&opts.allowlist, "model-pull-allowlist", "",
		"comma-separated URL prefixes models may be pulled from; empty rejects every pull")
	flag.Int64Var(&opts.quotaBytes, "model-pull-quota-bytes", 0,
		"byte budget for this run's model pulls; <=0 means unlimited")
	return opts
}

// allowlistPrefixes parses -model-pull-allowlist the same way
// tunnelOptions.runtimeIDs parses -allowed-runtimes.
func (o *modelPullOptions) allowlistPrefixes() []string {
	var prefixes []string
	for _, p := range strings.Split(o.allowlist, ",") {
		if p = strings.TrimSpace(p); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	return prefixes
}

// comfyuiManagedOptions is the configuration for comfyuimanaged (STATUS.md's
// P2 ComfyUI Managed Docker deployment, subtask one: see
// docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-design.md). It
// comes from flags, is entirely local to this node, and is never accepted
// from the Gateway or control plane. An empty image disables Managed mode
// entirely, leaving today's External-only behavior unchanged.
//
// comfyuiManagedOptions 是 comfyuimanaged 的配置（STATUS.md 的 P2 ComfyUI Managed
// Docker 部署子任务一，见
// docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-design.md）。它来自
// flag，完全是本节点本地的，从不接受 Gateway 或控制面下发。镜像为空时 Managed 模式
// 整体关闭，不影响今天的 External-only 行为。
type comfyuiManagedOptions struct {
	image        string
	container    string
	port         int
	gpuDevices   string
	modelPaths   string
	storagePaths string
	memoryLimit  string
	startTimeout time.Duration
	// customNodes and customNodesDir configure InstallCustomNode (STATUS.
	// md's P2 ComfyUI Managed Docker deployment, subtask 4): a local
	// allowlist of custom node install sources, never accepted from the
	// Gateway or control plane — see comfyuimanaged.NodeSpec's doc comment.
	//
	// customNodes 与 customNodesDir 配置 InstallCustomNode（STATUS.md 的 P2
	// ComfyUI Managed Docker 部署子任务四）：一个本地的自定义节点安装源允许
	// 列表，从不接受 Gateway 或控制面下发——见 comfyuimanaged.NodeSpec 的文
	// 档注释。
	customNodes    string
	customNodesDir string
}

func registerComfyUIManagedFlags() *comfyuiManagedOptions {
	opts := &comfyuiManagedOptions{}
	flag.StringVar(&opts.image, "comfyui-managed-image", "",
		"pinned image:tag for a ComfyUI container this agent starts and manages via docker; empty disables Managed mode entirely")
	flag.StringVar(&opts.container, "comfyui-managed-container", "aiserveweave-comfyui",
		"docker container name for the Managed ComfyUI instance")
	flag.IntVar(&opts.port, "comfyui-managed-port", 18188,
		"host port bound to 127.0.0.1 and mapped to the container's ComfyUI port")
	flag.StringVar(&opts.gpuDevices, "comfyui-managed-gpu-devices", "",
		"comma-separated GPU device ids passed to docker run --gpus; empty omits GPU passthrough entirely")
	flag.StringVar(&opts.modelPaths, "comfyui-managed-model-paths", "",
		"comma-separated container=hostpath pairs mounted read-only, e.g. checkpoints=/models/checkpoints,loras=/models/loras")
	flag.StringVar(&opts.storagePaths, "comfyui-managed-storage-paths", "",
		"comma-separated container=hostpath pairs mounted read-write, e.g. input=/data/input,output=/data/output")
	flag.StringVar(&opts.memoryLimit, "comfyui-managed-memory-limit", "",
		"docker --memory value for the container, e.g. 32g; empty is unlimited")
	flag.DurationVar(&opts.startTimeout, "comfyui-managed-start-timeout", 5*time.Minute,
		"how long Start waits for the Managed container's port to accept connections before failing")
	flag.StringVar(&opts.customNodes, "comfyui-managed-custom-nodes", "",
		"comma-separated name=repourl@ref allowlist entries the Gateway may trigger installing by name, e.g. my-node=https://example.com/my-node.git@v1.0.0")
	flag.StringVar(&opts.customNodesDir, "comfyui-managed-custom-nodes-dir", "/comfyui/custom_nodes",
		"the Managed container's custom_nodes directory; the default assumes a common ComfyUI image layout and may need overriding for others")
	return opts
}

// enabled reports whether the operator asked for a Managed ComfyUI instance.
func (o *comfyuiManagedOptions) enabled() bool { return o.image != "" }

// spec builds the comfyuimanaged.Spec this node's flags describe.
func (o *comfyuiManagedOptions) spec() comfyuimanaged.Spec {
	return comfyuimanaged.Spec{
		ContainerName: o.container,
		Image:         o.image,
		Port:          o.port,
		GPUDevices:    splitNonEmpty(o.gpuDevices),
		ModelPaths:    parseComfyUIManagedMounts(o.modelPaths),
		StoragePaths:  parseComfyUIManagedMounts(o.storagePaths),
		MemoryLimit:   o.memoryLimit,
	}
}

// splitNonEmpty parses a comma-separated list, the same way
// modelPullOptions.allowlistPrefixes parses -model-pull-allowlist.
func splitNonEmpty(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// customNodeAllowlist parses -comfyui-managed-custom-nodes into the
// allowlist comfyuimanaged.Supervisor.TriggerCustomNodeInstall consults
// (STATUS.md's P2 ComfyUI Managed Docker deployment, subtask 4). Each entry
// is "name=repourl@ref"; a malformed entry is dropped rather than fatal,
// the same tolerance parseComfyUIManagedMounts applies to a mount typo.
//
// customNodeAllowlist 解析 -comfyui-managed-custom-nodes 成
// comfyuimanaged.Supervisor.TriggerCustomNodeInstall 要查阅的允许列表
// （STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务四）。每条形如
// "name=repourl@ref"；格式错误的条目被丢弃而不是致命错误，与
// parseComfyUIManagedMounts 对一处挂载配置排版失误的容忍度相同。
func (o *comfyuiManagedOptions) customNodeAllowlist() map[string]comfyuimanaged.NodeSpec {
	out := make(map[string]comfyuimanaged.NodeSpec)
	for _, entry := range strings.Split(o.customNodes, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, source, ok := strings.Cut(entry, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		repoURL, ref, ok := strings.Cut(source, "@")
		repoURL, ref = strings.TrimSpace(repoURL), strings.TrimSpace(ref)
		if !ok || repoURL == "" || ref == "" {
			continue
		}
		out[name] = comfyuimanaged.NodeSpec{RepoURL: repoURL, Ref: ref}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseComfyUIManagedMounts parses -comfyui-managed-model-paths and
// -comfyui-managed-storage-paths the same way tunnelOptions.nodeLabels
// parses -labels: a malformed entry is dropped rather than fatal, since a
// typo in one mount should not be a reason to refuse starting Managed mode
// altogether.
func parseComfyUIManagedMounts(raw string) map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		containerPath, hostPath, ok := strings.Cut(pair, "=")
		containerPath, hostPath = strings.TrimSpace(containerPath), strings.TrimSpace(hostPath)
		if !ok || containerPath == "" || hostPath == "" {
			continue
		}
		out[containerPath] = hostPath
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// run wires the runtime registry and manager, then blocks until the process is
// signalled to stop. It returns the first error that prevents a clean start or
// a clean shutdown.
//
// ollamaURL and ollamaID are a stand-in for the runtime section of the agent
// config file described in tunnel/README.md: until that file lands, this is
// the only way to give the agent a real backend to dispatch to. An empty
// ollamaURL registers nothing, matching today's behavior.
func run(logger *slog.Logger, opts *tunnelOptions, mpOpts *modelPullOptions, cmOpts *comfyuiManagedOptions, ollamaURL, ollamaID string, autoDiscover bool, autoDiscoverInterval time.Duration, metricsAddr string) error {
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
	metricsRegistry := metrics.New(tunnel.Descriptions(), comfyui.Descriptions())
	metricsServer := startMetricsEndpoint(logger, metricsRegistry, metricsAddr)

	deps := newDependencies(logger, metricsRegistry)
	manager := runtime.NewManager(registry, deps)

	configuredRuntimes := 0
	if ollamaURL != "" {
		if err := manager.Add(ctx, runtime.Config{ID: ollamaID, Kind: runtime.KindOllama, BaseURL: ollamaURL}); err != nil {
			return err
		}
		configuredRuntimes++
	}

	// Managed ComfyUI (STATUS.md's P2 ComfyUI Managed Docker deployment,
	// subtask one boot path + subtask two's remote lifecycle actions): bring
	// the container up first, wait for its port to accept connections, then
	// register it exactly like an External instance so comfyui.Runtime's
	// existing Probe/Discover — not this block — perform the
	// ComfyUI-specific identity check. A configured Managed instance that
	// fails to start or never becomes reachable fails agent startup, the
	// same failure semantics as ollamaURL above: the operator explicitly
	// opted into Managed mode, so a fast, visible failure beats silently
	// running a node that never serves requests. The Supervisor built here
	// is reused after boot to react to a Gateway-triggered
	// start/stop/restart (subtask two) — see startTunnel's ComfyUIManaged
	// wiring — so this sequence exists exactly once instead of once for
	// boot and once inside Supervisor.Start.
	//
	// Managed ComfyUI（STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务一的启动
	// 路径 + 子任务二的远程生命周期动作）：先把容器带起来，等它的端口能接受连接，
	// 再按 External 实例同样的方式注册——真正的 ComfyUI 身份校验交给既有的
	// comfyui.Runtime 的 Probe/Discover，不是这段代码。配置了 Managed 但启动失败或
	// 从未可达时让 Agent 启动失败，与上面 ollamaURL 同样的失败语义：运维显式选择了
	// Managed 模式，快速可见的失败好过悄悄跑一个从不服务请求的节点。这里构造的
	// Supervisor 在启动之后会被复用来响应 Gateway 触发的 start/stop/restart（子任务
	// 二，见 startTunnel 的 ComfyUIManaged 接线），因此这段顺序只存在一份，而不是启动
	// 时一份、Supervisor.Start 内部再一份。
	var comfyUIManagedSupervisor *comfyuimanaged.Supervisor
	if cmOpts.enabled() {
		spec := cmOpts.spec()
		launcher := comfyuimanaged.NewLauncher(deps.Clock, logger)
		comfyUIManagedSupervisor = comfyuimanaged.NewSupervisorWithCustomNodes(ctx, launcher, manager, spec, cmOpts.startTimeout, deps.Clock, logger,
			cmOpts.customNodesDir, cmOpts.customNodeAllowlist())
		if err := comfyUIManagedSupervisor.Start(ctx); err != nil {
			return fmt.Errorf("comfyui managed: %w", err)
		}
		configuredRuntimes++
		logger.Info("comfyui managed instance registered", slog.String("container", spec.ContainerName),
			slog.String("base_url", fmt.Sprintf("http://127.0.0.1:%d", spec.Port)))
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
	puller := newModelPuller(ctx, logger, mpOpts, ollamaURL)

	tunnelErr, err := startTunnel(ctx, logger, manager, deps.Metrics, opts, puller, comfyUIManagedSupervisor)
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

// newModelPuller builds the Agent-local Puller from opts (STATUS.md's P2
// 「模型分发」子任务一：manifest/allowlist/quota) and ollamaURL (子任务三：
// KindOllama specs pull from the same Ollama instance -ollama-url already
// registers as a runtime; there is no separate flag for it), and, when a
// manifest is configured, kicks off pulling every entry in the background —
// the same non-blocking behavior startModelPull had before Puller existed. It never
// returns nil: an empty or unloadable manifest yields a Puller that knows no
// names, so a Gateway-triggered pull for any name is simply rejected as
// unknown (子任务二, tunnel.ClientConfig.ModelPuller) instead of needing a
// separate "feature disabled" code path. A manifest that fails to load is
// logged, not fatal — the same restraint hostresources uses for its own
// probing.
//
// Unlike the old startModelPull, this does not return a "done" channel to
// wait on at shutdown: RunManifest was a single bounded operation, so
// waiting for it kept shutdown logging orderly. Puller's work now spans the
// tunnel connection's whole lifetime and can be re-armed by a Gateway
// trigger at any moment, so "wait until idle" would be racing a Trigger that
// could arrive during the wait. ctx is still what every download runs
// under, so cancelling it still unwinds any in-flight fetch quickly — a
// partial ".part" file left behind by that is the same fully-supported
// resumable state a killed process already leaves today.
//
// newModelPuller 基于 opts（STATUS.md P2「模型分发」子任务一：manifest/
// allowlist/quota）与 ollamaURL（子任务三：KindOllama 的 spec 从
// -ollama-url 已经注册为运行时的同一个 Ollama 实例拉取，没有为此单独开一
// 个 flag）构造 Agent 本地的 Puller，清单配置了的话，会在后台开始拉
// 取每一条——与 Puller 出现之前 startModelPull 同样的非阻塞行为。它从不返回
// nil：清单为空或加载失败时，返回的 Puller 就是一个不认识任何名字的
// Puller，因此 Gateway 触发任意名字的拉取都会被直接拒绝为未知（子任务二，
// tunnel.ClientConfig.ModelPuller），不需要一条单独的"功能关闭"代码路径。
// 清单加载失败只记日志，不算致命——与 hostresources 自己探测时同一种克制。
//
// 与旧的 startModelPull 不同，这里不再返回一个供关闭时等待的 channel：
// RunManifest 曾经是一次有界操作，等待它能让关闭时的日志顺序整洁。Puller
// 的工作现在跨越隧道连接的整个生命周期，随时可能被 Gateway 的一次触发重新
// 激活，"等到空闲"天然是在和一个可能随时到达的 Trigger 竞态。ctx 仍然是每
// 一次下载运行所在的 context，取消它依然能让任何进行中的获取很快 unwind
// ——由此留下的部分 ".part" 文件，与今天进程被杀死留下的完全是同一种、被
// 完整支持的可续传状态。
func newModelPuller(ctx context.Context, logger *slog.Logger, opts *modelPullOptions, ollamaURL string) *modelpull.Puller {
	var specs []modelpull.Spec
	if opts.manifest != "" {
		var err error
		specs, err = modelpull.LoadManifest(opts.manifest)
		if err != nil {
			logger.Error("model pull manifest not loaded", slog.Any("error", err))
			specs = nil
		}
	}
	cfg := modelpull.Config{Allowlist: opts.allowlistPrefixes(), QuotaBytes: opts.quotaBytes, OllamaBaseURL: ollamaURL}
	puller := modelpull.NewPuller(ctx, cfg, specs, runtime.NewSystemClock())
	if len(specs) > 0 {
		names := make([]string, len(specs))
		for i, spec := range specs {
			names[i] = spec.Name
		}
		puller.Trigger(names)
		logger.Info("model pull started at startup", slog.Int("count", len(names)))
	}
	return puller
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
//
// puller drives STATUS.md's P2 model distribution subtask two: it lets the
// tunnel's Control stream honor a Gateway-triggered pull and report status
// back. newModelPuller never returns nil, so this is never nil either.
//
// comfyUIManaged drives STATUS.md's P2 ComfyUI Managed Docker deployment
// subtask two: it lets the tunnel's Control stream honor a
// Gateway-triggered start/stop/restart and report container-lifecycle
// status back. Unlike puller, this is nil whenever Managed mode is not
// configured on this node (run's cmOpts.enabled() was false) — wrapping a
// nil *comfyuimanaged.Supervisor in the tunnel.ComfyUIManager interface
// here would produce a non-nil interface value holding a nil pointer, so
// the interface field itself is only ever set when comfyUIManaged is
// actually non-nil.
func startTunnel(ctx context.Context, logger *slog.Logger, manager runtime.Manager, metrics runtime.Metrics, opts *tunnelOptions, puller *modelpull.Puller, comfyUIManaged *comfyuimanaged.Supervisor) (<-chan error, error) {
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
		AgentVersion:       version,
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

	// See startTunnel's doc comment: only assign the interface field when
	// comfyUIManaged is actually non-nil, so a disabled Managed mode leaves
	// ClientConfig.ComfyUIManaged as a true nil interface rather than a
	// non-nil interface wrapping a nil *comfyuimanaged.Supervisor.
	var comfyUIManagedClient tunnel.ComfyUIManager
	if comfyUIManaged != nil {
		comfyUIManagedClient = comfyUIManaged
	}

	tunnels, err := tunnel.NewManager(tunnel.ManagerConfig{
		Client: tunnel.ClientConfig{
			NodeID:          identity.NodeID,
			AgentVersion:    version,
			Manager:         manager,
			AllowedRuntimes: opts.runtimeIDs(),
			Labels:          opts.nodeLabels(),
			Resources:       hostresources.Detect(ctx, logger),
			Handler:         dispatcher,
			ModelPuller:     puller,
			ComfyUIManaged:  comfyUIManagedClient,
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
