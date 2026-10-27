package main

import (
	"context"
	"log/slog"
	"os"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/agentupgrade"
	"AIServeWeave/service/aiServeWeaveAgent/cliusage"
	"AIServeWeave/service/aiServeWeaveAgent/comfyuimanaged"
	"AIServeWeave/service/aiServeWeaveAgent/hostresources"
	"AIServeWeave/service/aiServeWeaveAgent/modelpull"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// startTunnel wires the connection table and runs it in the background,
// returning its manager for read-only status and the channel its outcome arrives
// on. A nil channel is returned
// when no gateway is configured, which blocks forever in the select above and
// leaves the agent running its runtimes only.
//
// startTunnel 装配连接表并在后台运行，返回供只读状态查询的管理器及结果通道。
// 未配置 Gateway 时返回 nil 管理器与 nil 通道，Agent 仅运行本地运行时。
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
func startTunnel(ctx context.Context, logger *slog.Logger, manager runtime.Manager, metrics runtime.Metrics, opts *tunnelOptions, puller *modelpull.Puller, comfyUIManaged *comfyuimanaged.Supervisor, upgradeChecker *agentupgrade.Checker, cliUsage *cliusage.Monitor) (<-chan error, *tunnel.Manager, error) {
	if !opts.enabled() {
		return nil, nil, nil
	}

	caPEM, err := os.ReadFile(opts.caFile)
	if err != nil {
		return nil, nil, err
	}
	connector, err := tunnel.NewGRPCRegistryConnector(opts.registry, caPEM)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}

	// The identity is obtained before the first dial: a node with no valid
	// certificate has nothing to say to a gateway, and failing here gives the
	// operator a clear error instead of an authentication loop. The
	// connection table keeps it rotated from then on.
	identity, err := identities.Ensure(ctx)
	if err != nil {
		return nil, nil, err
	}

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         manager,
		NodeID:          identity.NodeID,
		AllowedRuntimes: opts.runtimeIDs(),
		Metrics:         metrics,
		Logger:          logger,
	})
	if err != nil {
		return nil, nil, err
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
			CLIUsage:        cliUsageSource(cliUsage),
			Metrics:         metrics,
			Logger:          logger,
		},
		SeedEndpoints: opts.seeds(),
		MaxGateways:   opts.maxGateways,
		Identities:    identities,
		Logger:        logger,
	})
	if err != nil {
		return nil, nil, err
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
	return done, tunnels, nil
}
