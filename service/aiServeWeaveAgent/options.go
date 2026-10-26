package main

import (
	"flag"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/comfyuimanaged"
)

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
