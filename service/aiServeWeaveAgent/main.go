// Command aiserveweave-agent runs the node-side agent: it manages the local
// inference runtimes (Ollama, vLLM, SGLang, ComfyUI) and will dial out to the
// Gateway over the tunnel. The agent never listens on a public port.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/ollama"
	"AIServeWeave/common/runtime/sglang"
	"AIServeWeave/common/runtime/vllm"
	"AIServeWeave/common/runtime/workflow/comfyui"
	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
	"AIServeWeave/service/aiServeWeaveAgent/agentupgrade"
	"AIServeWeave/service/aiServeWeaveAgent/comfyuimanaged"
	"AIServeWeave/service/aiServeWeaveAgent/configui"
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
	dialTimeout           = 10 * time.Second
	responseHeaderTimeout = 60 * time.Second
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
		"probe 127.0.0.1's well-known Ollama/vLLM ports and register whatever answers; never touches any other host")
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
		"loopback address the local setup page listens on when -config-ui is set; must be a loopback address, only takes effect together with -config-ui")
	flag.Parse()

	if *showVersion {
		os.Stdout.WriteString("aiserveweave-agent " + version + "\n")
		return
	}

	explicit := explicitFlagNames()
	resolvedConfigPath := resolveConfigPath(*configPath, explicit["config"], defaultConfigPath)

	autoConfigUI := shouldAutoOpenConfigUI(*configUI, explicit, resolvedConfigPath)

	if *configUI || autoConfigUI {
		logger, err := newLogger(*logLevel)
		if err != nil {
			os.Stderr.WriteString("agent: " + err.Error() + "\n")
			os.Exit(2)
		}
		uiConfigPath := *configPath
		if !explicit["config"] {
			uiConfigPath = defaultConfigPath
		}
		if autoConfigUI {
			logger.Info("no -gateway and no config file found; opening the local setup page instead of running with the tunnel disabled",
				slog.String("addr", *configUIAddr), slog.String("config_path", uiConfigPath))
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := configui.Serve(ctx, logger, *configUIAddr, uiConfigPath); err != nil {
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

	if err := run(logger, opts, mpOpts, cmOpts, auOpts, declaredRuntimes, *ollamaURL, *autoDiscover, *autoDiscoverInterval, *metricsAddr); err != nil {
		logger.Error("agent exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// explicitFlagNames is the flag names the operator actually passed on the
// command line, as opposed to every flag's own default. applyConfig
// consults it so a flag explicitly given always wins over the same setting
// from -config, while a flag left at its default lets the file speak.
//
// explicitFlagNames 是操作者在命令行上实际传入的 flag 名字集合，与每个
// flag 自己的默认值区分开。applyConfig 靠它保证显式传入的 flag 永远优先于
// -config 里的同名设置，而留在默认值的 flag 则由配置文件说了算。
func explicitFlagNames() map[string]bool {
	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	return explicit
}

// defaultConfigPath is what resolveConfigPath falls back to when -config is
// never mentioned on the command line: "config.yaml" in the process's
// current working directory. This is the same name and location an
// operator gets by using -config-ui-addr without also passing -config (see
// main's configUIAddr branch), so the two features agree on where a config
// file lives without the operator having to say so twice.
//
// defaultConfigPath 是命令行上完全没提 -config 时 resolveConfigPath 落回的
// 默认值：进程当前工作目录下的 "config.yaml"。这与只传 -config-ui-addr、不
// 传 -config 时（见 main 里 configUIAddr 分支）落地的文件同名同地，两个功
// 能因此在"配置文件放哪"这件事上达成一致，不需要运维说两遍。
const defaultConfigPath = "config.yaml"

// defaultConfigUIAddr is -config-ui-addr's default value: a loopback
// address the operator does not have to remember or type out. It only ever
// takes effect when -config-ui is also set — unlike -metrics-addr, this
// flag's default must never by itself decide whether a listener opens,
// because doing so would make a plain, flagless run of the agent silently
// start a page able to repoint it at a different Gateway. See -config-ui's
// help text and the configUI branch below for the actual gate.
//
// defaultConfigUIAddr 是 -config-ui-addr 的默认值：一个不用运维记住或敲出
// 来的回环地址。它只在同时设置了 -config-ui 时才生效——与 -metrics-addr 不
// 同，这个 flag 的默认值绝不能单独决定要不要开监听，因为那会让一次什么
// flag 都没传的普通启动，悄悄带起一个能把 Agent 改去连接不同 Gateway 的页
// 面。真正的开关见 -config-ui 的帮助文本与下面的 configUI 分支。
const defaultConfigUIAddr = "127.0.0.1:8899"

// shouldAutoOpenConfigUI decides whether main should drop a first-time
// operator into the local setup page instead of running with the tunnel
// disabled: no -gateway on the command line and no config file on disk
// means there is nothing this run could usefully do besides local runtime
// bookkeeping, and a brand-new checkout with nothing configured yet is
// exactly who this page is for (see the configui package doc). Any of
// -config-ui, -config-ui-addr, -config or -gateway being explicitly
// passed — even -gateway="" — opts out: that is the operator deliberately
// saying what they want, not having wandered in with nothing set up.
//
// shouldAutoOpenConfigUI 决定 main 该不该把一次全新的启动带进本地设置页
// 面，而不是悄悄以隧道关闭的状态跑起来：命令行没有 -gateway，磁盘上也没
// 有配置文件，这次运行除了本地运行时记账之外做不了任何有用的事，而一次
// 全新、什么都没配置过的检出，正是这个页面（见 configui 包文档）存在的理
// 由。-config-ui、-config-ui-addr、-config 或 -gateway 里任何一个被显式传
// 入——哪怕是 -gateway=""——都会退出这条自动路径：那是操作者在明确表达自
// 己想要什么，而不是两手空空地走进来。
func shouldAutoOpenConfigUI(configUI bool, explicit map[string]bool, resolvedConfigPath string) bool {
	if configUI || explicit["config-ui"] || explicit["config-ui-addr"] || explicit["config"] || explicit["gateway"] {
		return false
	}
	return resolvedConfigPath == ""
}

// resolveConfigPath decides which file, if any, main should load as
// -config. An explicitly passed path always wins verbatim — including when
// it points at nothing, so Load's error surfaces as a hard failure the
// operator sees immediately, the same as any other typo'd flag value. Only
// when -config was never mentioned does it fall back to defaultPath, and
// even then only when that file is actually present: a fresh checkout or a
// fresh deployment with no config file yet must keep behaving exactly like
// this feature never shipped, driven by flags alone. A defaultPath that
// exists but fails to parse is not silently skipped, though — main still
// calls agentconfig.Load on whatever this function returns, and a malformed
// file the operator did leave there is a real mistake worth seeing, not one
// to paper over by pretending it was never found. main always passes
// defaultConfigPath here; the parameter exists so tests can point it at a
// temp directory instead of depending on the real working directory's
// contents.
//
// resolveConfigPath 决定 main 该加载哪份文件作为 -config（如果有的话）。显
// 式传入的路径永远原样优先——即使它指向不存在的文件，也要让 Load 的错误立
// 刻暴露给操作者，与任何其他敲错的 flag 值一样。只有命令行完全没提到
// -config 时才会落回 defaultPath，而且只在那份文件真实存在时才用：一次全
// 新的检出、或者一次还没放配置文件的全新部署，行为必须与这个功能从未上线
// 时完全一样，只靠 flag 驱动。defaultPath 存在但解析失败并不会被悄悄跳
// 过——main 仍然会对这个函数返回的路径调用 agentconfig.Load，运维真的放了
// 一份格式错误的文件在那里，是值得被看见的真实失误，不该假装没找到它。
// main 总是把 defaultConfigPath 传进来；这个参数存在是为了让测试能指向一
// 个临时目录，而不必依赖真实工作目录里恰好有什么文件。
func resolveConfigPath(flagValue string, explicit bool, defaultPath string) string {
	if explicit {
		return flagValue
	}
	if _, err := os.Stat(defaultPath); err == nil {
		return defaultPath
	}
	return ""
}

// applyConfig merges cfg (loaded from -config) into the already-parsed flag
// values named in explicit. Runtimes are handled separately by
// runtimesFromConfig: a declared runtime is additive to -ollama-url rather
// than something a flag can override field-by-field, so there is no
// per-field precedence to apply for it.
//
// applyConfig 把 cfg（从 -config 加载）合并进已经解析好的 flag 值，
// explicit 记录了哪些 flag 被显式传入。Runtimes 由 runtimesFromConfig 单
// 独处理：一个声明的运行时是 -ollama-url 之外的叠加项，而不是某个 flag 能
// 逐字段覆盖的东西，因此这里不涉及它的优先级。
func applyConfig(cfg *agentconfig.Config, explicit map[string]bool, opts *tunnelOptions, autoDiscover *bool, autoDiscoverInterval *time.Duration, metricsAddr, logLevel *string) error {
	gw := cfg.Gateway
	if !explicit["gateway"] && len(gw.Endpoints) > 0 {
		opts.gateway = strings.Join(gw.Endpoints, ",")
	}
	if !explicit["registry"] && gw.Registry != "" {
		opts.registry = gw.Registry
	}
	if !explicit["node-id"] && gw.NodeID != "" {
		opts.nodeID = gw.NodeID
	}
	if !explicit["cert-file"] && gw.CertFile != "" {
		opts.certFile = gw.CertFile
	}
	if !explicit["key-file"] && gw.KeyFile != "" {
		opts.keyFile = gw.KeyFile
	}
	if !explicit["ca-file"] && gw.CAFile != "" {
		opts.caFile = gw.CAFile
	}
	if !explicit["bootstrap-token-file"] && gw.BootstrapTokenFile != "" {
		opts.bootstrapToken = gw.BootstrapTokenFile
	}
	if !explicit["allowed-runtimes"] && len(gw.AllowedRuntimes) > 0 {
		opts.allowedRuntimes = strings.Join(gw.AllowedRuntimes, ",")
	}
	if !explicit["labels"] && len(gw.Labels) > 0 {
		pairs := make([]string, 0, len(gw.Labels))
		for k, v := range gw.Labels {
			pairs = append(pairs, k+"="+v)
		}
		opts.labels = strings.Join(pairs, ",")
	}
	if !explicit["max-gateways"] && gw.MaxGateways > 0 {
		opts.maxGateways = gw.MaxGateways
	}

	if !explicit["auto-discover"] && cfg.AutoDiscover != nil {
		*autoDiscover = *cfg.AutoDiscover
	}
	if !explicit["auto-discover-interval"] && cfg.AutoDiscoverInterval != "" {
		d, err := time.ParseDuration(cfg.AutoDiscoverInterval)
		if err != nil {
			return fmt.Errorf("auto_discover_interval: %w", err)
		}
		*autoDiscoverInterval = d
	}
	if !explicit["metrics-addr"] && cfg.MetricsAddr != nil {
		*metricsAddr = *cfg.MetricsAddr
	}
	if !explicit["log-level"] && cfg.LogLevel != "" {
		*logLevel = cfg.LogLevel
	}
	return nil
}

// runtimesFromConfig converts a config file's declared runtimes into the
// runtime.Config values manager.Add expects. It performs no validation of
// its own: an unrecognized Kind surfaces from manager.Add as
// runtime.ErrRuntimeKindUnsupported, the same way any other bad -kind would.
//
// runtimesFromConfig 把配置文件里声明的运行时转换成 manager.Add 所需的
// runtime.Config。它自己不做任何校验：一个不认识的 Kind 会从 manager.Add
// 那里以 runtime.ErrRuntimeKindUnsupported 的形式暴露出来，与任何其他错误
// 的 -kind 一样。
func runtimesFromConfig(declared []agentconfig.RuntimeConfig) []runtime.Config {
	if len(declared) == 0 {
		return nil
	}
	out := make([]runtime.Config, len(declared))
	for i, rc := range declared {
		out[i] = runtime.Config{ID: rc.ID, Kind: runtime.Kind(rc.Kind), BaseURL: rc.BaseURL}
	}
	return out
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
	manifest            string
	allowlist           string
	quotaBytes          int64
	maxConcurrency      int
	ledgerPath          string
	ledgerQuotaBytes    int64
	ledgerPeriod        time.Duration
	diskFreeMarginBytes int64
}

func registerModelPullFlags() *modelPullOptions {
	opts := &modelPullOptions{}
	flag.StringVar(&opts.manifest, "model-pull-manifest", "",
		"path to a JSON manifest of models to fetch and verify at startup; empty disables model pulling")
	flag.StringVar(&opts.allowlist, "model-pull-allowlist", "",
		"comma-separated URL prefixes models may be pulled from; empty rejects every pull")
	flag.Int64Var(&opts.quotaBytes, "model-pull-quota-bytes", 0,
		"byte budget for this run's model pulls; <=0 means unlimited")
	flag.IntVar(&opts.maxConcurrency, "model-pull-max-concurrency", 1,
		"maximum number of model pulls to run at once (subtask 4); <=1 pulls one at a time")
	flag.StringVar(&opts.ledgerPath, "model-pull-ledger-path", "",
		"path to a JSON file tracking cumulative model pull bytes across Agent restarts (subtask 4); empty disables the cross-restart quota ledger")
	flag.Int64Var(&opts.ledgerQuotaBytes, "model-pull-ledger-quota-bytes", 0,
		"cumulative byte budget the ledger enforces across restarts; <=0 means unlimited (only meaningful with -model-pull-ledger-path)")
	flag.DurationVar(&opts.ledgerPeriod, "model-pull-ledger-period", 0,
		"rolling window after which the ledger resets to zero; <=0 means it never resets on its own (only meaningful with -model-pull-ledger-path)")
	flag.Int64Var(&opts.diskFreeMarginBytes, "model-pull-disk-free-margin-bytes", 0,
		"abort a model pull once the target filesystem's free space falls below this many bytes (subtask 4's secondary defense); <=0 disables the check")
	return opts
}

// agentUpgradeOptions is agentupgrade's configuration (STATUS.md's P2 Agent
// auto-upgrade: see
// docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md). It
// comes from flags, is entirely local to this node, and is never accepted
// from the Gateway or control plane. An empty manifest, or a node built
// without agentUpgradePublicKeyHex, disables the feature.
//
// agentUpgradeOptions 是 agentupgrade 的配置（STATUS.md P2 Agent 自动升
// 级，见
// docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md）。它来
// 自 flag，完全是本节点本地的，从不接受 Gateway 或控制面下发。清单为空，
// 或者构建时没带 agentUpgradePublicKeyHex，都会关闭这个功能。
type agentUpgradeOptions struct {
	manifest     string
	workDir      string
	drainTimeout time.Duration
}

func registerAgentUpgradeFlags() *agentUpgradeOptions {
	opts := &agentUpgradeOptions{}
	flag.StringVar(&opts.manifest, "agent-upgrade-manifest", "",
		"path to a JSON manifest of known Agent versions (download URL, SHA256, and one Ed25519 signature over the whole list), signed by the key matching -ldflags=\"-X main.agentUpgradePublicKeyHex=...\"; empty disables the upgrade check. UPGRADE downloads/verifies/drains/execs; ROLLBACK always reports NOT_IMPLEMENTED (STATUS.md P2 Agent auto-upgrade subtask 3)")
	flag.StringVar(&opts.workDir, "agent-upgrade-work-dir", "",
		"directory an UPGRADE downloads and executes the new binary from; empty defaults to the directory the running binary lives in")
	flag.DurationVar(&opts.drainTimeout, "agent-upgrade-drain-timeout", 0,
		"how long an UPGRADE waits for this Agent's in-flight requests, across every Gateway connection, to drain before replacing the process anyway; <=0 defaults to 30s")
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
// declaredRuntimes is main's merged view of -ollama-url/-ollama-id plus
// whatever -config's runtimes: list adds — see runtimesFromConfig. A
// declared runtime that fails to register fails agent startup, since the
// operator explicitly asked for it to be there.
//
// ollamaURL is passed again, separately, only because newModelPuller needs
// to know which single Ollama instance a Kind:"ollama" model-pull spec
// targets; modelpull stays -ollama-url-only and does not consult
// declaredRuntimes, matching its documented "纯本地 flag" contract.
func run(logger *slog.Logger, opts *tunnelOptions, mpOpts *modelPullOptions, cmOpts *comfyuiManagedOptions, auOpts *agentUpgradeOptions, declaredRuntimes []runtime.Config, ollamaURL string, autoDiscover bool, autoDiscoverInterval time.Duration, metricsAddr string) error {
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
	for _, rc := range declaredRuntimes {
		if err := manager.Add(ctx, rc); err != nil {
			return fmt.Errorf("declared runtime %q (%s): %w", rc.ID, rc.Kind, err)
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

	tunnelErr, err := startTunnel(ctx, logger, manager, deps.Metrics, opts, puller, comfyUIManagedSupervisor, upgradeChecker)
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
	cfg := modelpull.Config{
		Allowlist:           opts.allowlistPrefixes(),
		QuotaBytes:          opts.quotaBytes,
		OllamaBaseURL:       ollamaURL,
		MaxConcurrency:      opts.maxConcurrency,
		LedgerQuotaBytes:    opts.ledgerQuotaBytes,
		DiskFreeMarginBytes: opts.diskFreeMarginBytes,
	}
	if opts.ledgerPath != "" {
		cfg.Ledger = &modelpull.Ledger{Path: opts.ledgerPath, Period: opts.ledgerPeriod, Clock: runtime.NewSystemClock()}
	}
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

// newAgentUpgradeChecker builds the Agent-local Checker from opts (STATUS.md's
// P2 Agent auto-upgrade), comparing against this Agent's own version. It
// never returns nil, the same "always-present, empty manifest means no
// known updates" shape newModelPuller uses: a Gateway-triggered
// CHECK/UPGRADE/ROLLBACK against an empty manifest is simply answered as
// unknown/not-implemented instead of needing a separate "feature disabled"
// code path. A manifest that fails to load — including one that fails
// signature verification — is logged, not fatal, and yields no entries; the
// same restraint newModelPuller uses for its own manifest.
//
// Real UPGRADE execution (subtask 2) is enabled only when
// agentUpgradePublicKeyHex was compiled in: without it, LoadManifest
// already refuses to trust any manifest, but EnableExecution is skipped
// too, so the Checker stays in the safe-by-construction "not implemented"
// shape Checker's package doc describes rather than calling EnableExecution
// with a work directory it would never have a verified entry to act on.
// opts.workDir empty resolves to the running binary's own directory, so a
// downloaded version lands and executes next to the one that fetched it.
//
// newAgentUpgradeChecker 基于 opts（STATUS.md P2 Agent 自动升级）构造 Agent
// 本地的 Checker，比较对象是本 Agent 自己的版本。它从不返回 nil，与
// newModelPuller 同一种"始终存在、清单为空就意味着没有已知更新"的形状：对
// 一份空清单触发 CHECK/UPGRADE/ROLLBACK 只是被直接答复为未知/未实现，不需
// 要一条单独的"功能关闭"代码路径。清单加载失败——包括签名校验不通过——只记
// 日志、不算致命，且不产生任何条目；与 newModelPuller 对自己清单的同一种
// 克制。
//
// 真正的 UPGRADE 执行（子任务二）只在编译时带了
// agentUpgradePublicKeyHex 才会打开：没带它时，LoadManifest 本来就会拒绝
// 信任任何清单，但这里也跳过 EnableExecution，让 Checker 保持它包文档描述
// 的、构造上就安全的"未实现"形状，而不是拿着一个永远不会有已校验条目可执
// 行的工作目录去调用 EnableExecution。opts.workDir 为空时解析为运行中二进
// 制自己所在的目录，这样下载到的版本会落在获取它的那个二进制旁边并从那里
// 执行。
func newAgentUpgradeChecker(logger *slog.Logger, opts *agentUpgradeOptions) *agentupgrade.Checker {
	pubKey, pubKeyErr := parseAgentUpgradePublicKey(agentUpgradePublicKeyHex)
	if pubKeyErr != nil {
		logger.Error("agent upgrade public key not usable; upgrade execution stays disabled", slog.Any("error", pubKeyErr))
	}

	var entries []agentupgrade.Entry
	if opts.manifest != "" && pubKeyErr == nil {
		var err error
		entries, err = agentupgrade.LoadManifest(opts.manifest, pubKey)
		if err != nil {
			logger.Error("agent upgrade manifest not loaded", slog.Any("error", err))
			entries = nil
		}
	}

	checker := agentupgrade.NewChecker(entries, version, runtime.NewSystemClock())
	if pubKeyErr == nil {
		workDir := opts.workDir
		if workDir == "" {
			if exe, err := os.Executable(); err == nil {
				workDir = filepath.Dir(exe)
			} else {
				logger.Error("cannot resolve the running binary's directory; upgrade execution stays disabled", slog.Any("error", err))
				return checker
			}
		}
		checker.EnableExecution(workDir, nil, opts.drainTimeout)
	}
	return checker
}

// parseAgentUpgradePublicKey decodes hexKey into an Ed25519 public key. An
// empty string (the default when agentUpgradePublicKeyHex was never stamped
// in at build time) is reported as an error like any other invalid key —
// there is no separate "feature disabled" return value, because the caller
// treats every error here identically: leave upgrade execution off.
//
// parseAgentUpgradePublicKey 把 hexKey 解码成一个 Ed25519 公钥。空字符串
// （构建时从未注入 agentUpgradePublicKeyHex 时的默认值）与任何其他无效的
// 密钥一样被报告为错误——这里没有单独的"功能关闭"返回值，因为调用方对这
// 里的每一种错误都做同一件事：不打开升级执行。
func parseAgentUpgradePublicKey(hexKey string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("agent upgrade public key is not valid hex: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("agent upgrade public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
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
//
// upgradeChecker drives STATUS.md's P2 Agent auto-upgrade subtask one: it
// lets the tunnel's Control stream honor a Gateway-triggered CHECK (and
// accept, but not execute, UPGRADE/ROLLBACK) and report status back.
// newAgentUpgradeChecker never returns nil, so this is never nil either —
// the same shape puller uses.
func startTunnel(ctx context.Context, logger *slog.Logger, manager runtime.Manager, metrics runtime.Metrics, opts *tunnelOptions, puller *modelpull.Puller, comfyUIManaged *comfyuimanaged.Supervisor, upgradeChecker *agentupgrade.Checker) (<-chan error, error) {
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
			AgentUpgrader:   upgradeChecker,
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
	// upgradeChecker's Drainer is wired here rather than through
	// ClientConfig: *tunnel.Manager (tunnels) does not exist until
	// tunnel.NewManager has already consumed upgradeChecker as a config
	// field, so this is the earliest point a Drainer can be installed —
	// well before tunnels.Run starts accepting any Gateway-triggered
	// AgentUpgradeAction frame. See agentupgrade.Checker.SetDrainer's doc.
	//
	// upgradeChecker 的 Drainer 在这里接入，而不是通过 ClientConfig：
	// *tunnel.Manager（tunnels）要到 tunnel.NewManager 已经把
	// upgradeChecker 当作配置字段消费完之后才存在，因此这是能安装 Drainer
	// 的最早时机——早于 tunnels.Run 开始接受任何 Gateway 触发的
	// AgentUpgradeAction 帧。见 agentupgrade.Checker.SetDrainer 的文档。
	upgradeChecker.SetDrainer(tunnels)

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
