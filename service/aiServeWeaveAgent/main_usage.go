package main

import (
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
	"AIServeWeave/service/aiServeWeaveAgent/cliusage"
	"AIServeWeave/service/aiServeWeaveAgent/codexcli"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// cliUsageRefreshInterval is how often the cached CLI quota is refreshed for the
// tunnel to report. It is deliberately slower than the monitor's one-batch-per-
// minute bound: each pass starts a CLI process, and the console shows the check
// time, so a few minutes of age is visible rather than hidden.
//
// cliUsageRefreshInterval 是为隧道上报而刷新 CLI 额度缓存的间隔。它刻意比 monitor
// 「每分钟一批」的下限更慢：每一轮都会启动一个 CLI 进程，而控制台会显示查询时间，
// 数据有几分钟的年龄是看得见的，而不是被掩盖的。
const cliUsageRefreshInterval = 5 * time.Minute

// cliUsageSource hands the monitor to the tunnel, or nil when no CLI is
// declared, so a node without Codex / Claude Code never reports an empty quota.
//
// cliUsageSource 把 monitor 交给隧道；没有声明任何 CLI 时返回 nil，让没有 Codex /
// Claude Code 的节点不会上报空的额度。
func cliUsageSource(monitor *cliusage.Monitor) tunnel.CLIUsageSource {
	if monitor == nil || len(monitor.Snapshot()) == 0 {
		return nil
	}
	return monitor
}

// newCLIUsageMonitor selects quota readers only for locally declared CLI backends.
// newCLIUsageMonitor 只为本地声明的 CLI 后端选择额度读取器。
func newCLIUsageMonitor(configs []runtime.Config, clock runtime.Clock) *cliusage.Monitor {
	var providers []cliusage.Provider
	for _, cfg := range configs {
		switch cfg.Kind {
		case runtime.KindCodex:
			providers = append(providers, cliusage.Provider{Kind: cfg.Kind, Read: codexcli.ReadAccountUsage})
		case runtime.KindClaude:
			providers = append(providers, cliusage.Provider{Kind: cfg.Kind, Read: claudecode.ReadAccountUsage})
		}
	}
	return cliusage.NewMonitor(clock, providers)
}
