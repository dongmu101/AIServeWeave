// Command aiserveweave-agent runs the node-side agent: it manages the local
// inference runtimes (Ollama, vLLM, SGLang, TensorRT-LLM, TokenSpeed,
// ComfyUI) and will dial out to the Gateway over the tunnel. The agent never
// listens on a public port.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/workflow/comfyui"
	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
	"AIServeWeave/service/aiServeWeaveAgent/comfyuimanaged"
	"AIServeWeave/service/aiServeWeaveAgent/configui"
	"AIServeWeave/service/aiServeWeaveAgent/localdiscovery"
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

// agentUpgradePublicKeyHex is the hex-encoded Ed25519 public key (64 hex
// characters) that authenticates -agent-upgrade-manifest (STATUS.md's P2
// Agent auto-upgrade subtask 2). Stamped at build time via
// -ldflags="-X main.agentUpgradePublicKeyHex=...", the same mechanism as
// version; empty (the `go build` default) disables the feature entirely —
// agentupgrade.LoadManifest refuses to trust any manifest without a
// correctly sized public key, so a node built without this flag can never
// execute a downloaded binary no matter what a manifest file on disk says.
// The matching private key's custody is a maintainer decision this
// codebase does not make (design doc section 六): it must never be
// reachable from any Agent's runtime environment.
//
// agentUpgradePublicKeyHex 是为 -agent-upgrade-manifest 做身份验证的十六进
// 制编码 Ed25519 公钥（64 个十六进制字符，STATUS.md P2 Agent 自动升级子任
// 务二）。构建时通过 -ldflags="-X main.agentUpgradePublicKeyHex=..." 注
// 入，与 version 同一种机制；留空（`go build` 的默认值）会彻底关闭这个功
// 能——agentupgrade.LoadManifest 拒绝信任任何没有正确长度公钥的清单，因此
// 一个没带这个 flag 构建出来的节点，无论磁盘上的清单文件写了什么，都永远
// 不可能执行任何下载到的二进制。对应私钥归谁保管是本代码库不代为决定的维
// 护者裁决（设计文档第六节）：它绝不能被任何 Agent 的运行环境触及。
var agentUpgradePublicKeyHex = ""

const (
	// shutdownTimeout bounds how long the agent waits for in-flight runtime
	// work to wind down before exiting anyway.
	shutdownTimeout = 15 * time.Second

	// dialTimeout and responseHeaderTimeout bound how long the agent waits to
	// reach a local backend and to see its response headers. They are set on
	// the transport rather than as http.Client.Timeout on purpose: a client
	// deadline also covers reading the body, which would truncate long-lived
	// SSE streams. Per-request deadlines come from the caller's context.
	//
	// responseHeaderTimeout is 5 minutes because a large local model may queue
	// behind another request or load from disk before its first byte.
	// responseHeaderTimeout 取 5 分钟：大模型在本机可能要排队或从磁盘加载，
	// 之后才会吐出第一个字节。
	dialTimeout           = 10 * time.Second
	responseHeaderTimeout = 5 * time.Minute
)

func main() {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	opts := registerTunnelFlags()
	mpOpts := registerModelPullFlags()
	cmOpts := registerComfyUIManagedFlags()
	auOpts := registerAgentUpgradeFlags()
	ollamaURL := flag.String("ollama-url", "",
		"base URL of a local Ollama instance to register, e.g. http://127.0.0.1:11434; empty registers no runtime")
	ollamaID := flag.String("ollama-id", "ollama", "runtime id to register the Ollama instance under")
	autoDiscover := flag.Bool("auto-discover", true,
		"probe 127.0.0.1's well-known Ollama/vLLM/TensorRT-LLM/TokenSpeed/SGLang ports and register whatever answers; never touches any other host")
	autoDiscoverInterval := flag.Duration("auto-discover-interval", localdiscovery.DefaultInterval,
		"how often auto-discovery looks for a newly appeared local instance")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9091",
		"address the Prometheus /metrics listener binds; loopback by default because the agent never listens on a public port, empty disables it")
	showVersion := flag.Bool("version", false, "print the version and exit")
	configPath := flag.String("config", "",
		"path to a YAML config file for the gateway connection and local runtime declarations (see the agentconfig package doc); "+
			"a flag explicitly passed on the command line always overrides the same setting from this file; "+
			"if left empty, the agent looks for \""+defaultConfigPath+"\" in the working directory and loads it if present, otherwise runs on flags alone")
	configUI := flag.Bool("config-ui", false,
		"instead of starting the agent, serve a local setup page (see -config-ui-addr) for editing -config")
	configUIAddr := flag.String("config-ui-addr", defaultConfigUIAddr,
		"loopback address the local setup page listens on; the page starts alongside the agent by default, -config-ui runs only the page")
	flag.Parse()

	if *showVersion {
		os.Stdout.WriteString("aiserveweave-agent " + version + "\n")
		return
	}

	explicit := explicitFlagNames()
	resolvedConfigPath := resolveConfigPath(*configPath, explicit["config"], defaultConfigPath)
	uiConfigPath := *configPath
	if !explicit["config"] {
		uiConfigPath = defaultConfigPath
	}

	autoConfigUI := shouldAutoOpenConfigUI(*configUI, explicit, resolvedConfigPath)

	if *configUI || autoConfigUI {
		logger, err := newLogger(*logLevel)
		if err != nil {
			os.Stderr.WriteString("agent: " + err.Error() + "\n")
			os.Exit(2)
		}
		if autoConfigUI {
			logger.Info("no -gateway and no config file found; opening the local setup page instead of running with the tunnel disabled",
				slog.String("addr", *configUIAddr), slog.String("config_path", uiConfigPath))
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := configui.Serve(ctx, logger, *configUIAddr, uiConfigPath, configui.Sources{}); err != nil {
			logger.Error("config setup page exited with error", slog.Any("error", err))
			os.Exit(1)
		}
		return
	}

	var declaredRuntimes []runtime.Config
	if resolvedConfigPath != "" {
		cfg, err := agentconfig.Load(resolvedConfigPath)
		if err != nil {
			os.Stderr.WriteString("agent: config: " + err.Error() + "\n")
			os.Exit(2)
		}
		if err := applyConfig(cfg, explicit, opts, autoDiscover, autoDiscoverInterval, metricsAddr, logLevel); err != nil {
			os.Stderr.WriteString("agent: config: " + err.Error() + "\n")
			os.Exit(2)
		}
		declaredRuntimes = runtimesFromConfig(cfg.Runtimes)
	}
	if *ollamaURL != "" {
		declaredRuntimes = append(declaredRuntimes, runtime.Config{ID: *ollamaID, Kind: runtime.KindOllama, BaseURL: *ollamaURL})
	}

	logger, err := newLogger(*logLevel)
	if err != nil {
		// The logger is not usable yet, so report the failure directly.
		os.Stderr.WriteString("agent: " + err.Error() + "\n")
		os.Exit(2)
	}

	if err := run(logger, opts, mpOpts, cmOpts, auOpts, declaredRuntimes, *ollamaURL, *autoDiscover, *autoDiscoverInterval, *metricsAddr, *configUIAddr, uiConfigPath); err != nil {
		logger.Error("agent exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// run serves the local setup page alongside the runtime manager and tunnel
// until shutdown. An unavailable page or declared backend does not prevent
// startup; unhealthy backends are retried. Invalid configuration remains fatal.
// declaredRuntimes merges YAML declarations with -ollama-url/-ollama-id;
// model pulls use only ollamaURL, matching their local flag contract.
//
// run 同时运行本地设置页面、运行时管理器和隧道，直到关闭。
// 页面或已声明的后端暂时不可用不阻止启动；未就绪的后端会定期重试。
// 无效配置仍使启动失败。declaredRuntimes 合并 YAML 与 -ollama-url/-ollama-id
// 的声明；模型拉取仅使用 ollamaURL，保持其纯本地 flag 契约。
func run(logger *slog.Logger, opts *tunnelOptions, mpOpts *modelPullOptions, cmOpts *comfyuiManagedOptions, auOpts *agentUpgradeOptions, declaredRuntimes []runtime.Config, ollamaURL string, autoDiscover bool, autoDiscoverInterval time.Duration, metricsAddr, configUIAddr, configPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry, err := newRegistry(declaredRuntimes)
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
	defer func() {
		stop()
		_ = manager.Close(context.Background())
		if metricsServer != nil {
			_ = metricsServer.Close()
		}
	}()
	var activeTunnels atomic.Pointer[tunnel.Manager]
	cliUsage := newCLIUsageMonitor(declaredRuntimes, deps.Clock)
	if opts.enabled() {
		// Only a node with a tunnel has anywhere to report the quota to; the
		// settings page refreshes the cache on its own when it is viewed.
		//
		// 只有带隧道的节点才有地方上报额度；设置页被查看时自己会刷新缓存。
		go cliUsage.Run(ctx, cliUsageRefreshInterval)
	}
	uiSources := configui.Sources{
		Runtimes: manager.Snapshot,
		CLIUsage: cliUsage,
		Gateway: func() configui.GatewaySnapshot {
			current := activeTunnels.Load()
			snapshot := configui.GatewaySnapshot{Enabled: opts.enabled(), Started: current != nil}
			if current != nil {
				snapshot.States = make(map[string]string)
				for endpoint, state := range current.TunnelStates() {
					snapshot.States[endpoint] = state.String()
				}
			}
			return snapshot
		},
	}
	uiDone := make(chan struct{})
	go func() {
		defer close(uiDone)
		if err := configui.Serve(ctx, logger, configUIAddr, configPath, uiSources); err != nil {
			logger.Error("config setup page unavailable; Agent will keep running / 配置页面不可用，Agent 继续运行", slog.Any("error", err))
		}
	}()
	defer func() {
		stop()
		<-uiDone
	}()

	configuredRuntimes := 0
	for _, rc := range declaredRuntimes {
		rc.AllowUnavailable = true
		if err := manager.Add(ctx, rc); err != nil {
			return fmt.Errorf("declared runtime %q (%s): %w", rc.ID, rc.Kind, err)
		}
		configuredRuntimes++
	}
	for _, snapshot := range manager.Snapshot() {
		if snapshot.State != runtime.StateHealthy {
			logger.Warn("runtime not ready; Agent will keep running and retry / 运行时未就绪，Agent 继续运行并重试",
				slog.String("runtime_id", snapshot.Descriptor.ID), slog.String("kind", string(snapshot.Descriptor.Kind)),
				slog.String("state", string(snapshot.State)))
		}
	}

	// Managed ComfyUI (STATUS.md's P2 ComfyUI Managed Docker deployment,
	// subtask one boot path + subtask two's remote lifecycle actions): bring
	// the container up first, wait for its port to accept connections, then
	// register it exactly like an External instance so comfyui.Runtime's
	// existing Probe/Discover — not this block — perform the
	// ComfyUI-specific identity check. A configured Managed instance that
	// fails to start or never becomes reachable fails agent startup: the operator
	// explicitly opted into Managed mode, so a fast, visible failure beats silently
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
	// 从未可达时让 Agent 启动失败：运维显式选择了
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

	// Beyond declaredRuntimes above, the manager starts with no instances
	// until auto-discovery (below) or the tunnel's config delivery adds
	// one.
	logger.Info("agent started",
		slog.Any("supported_kinds", registry.Kinds()),
		slog.Int("configured_runtimes", configuredRuntimes),
		slog.Bool("tunnel_enabled", opts.enabled()),
		slog.Bool("auto_discover", autoDiscover),
	)

	discoveryDone := startLocalDiscovery(ctx, logger, manager, autoDiscover, autoDiscoverInterval)
	puller := newModelPuller(ctx, logger, mpOpts, ollamaURL)
	upgradeChecker := newAgentUpgradeChecker(logger, auOpts)

	tunnelErr, tunnelManager, err := startTunnel(ctx, logger, manager, deps.Metrics, opts, puller, comfyUIManagedSupervisor, upgradeChecker, cliUsage)
	if err != nil {
		return err
	}

	activeTunnels.Store(tunnelManager)

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

// newLogger returns a JSON logger at the requested level.
func newLogger(level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), nil
}
