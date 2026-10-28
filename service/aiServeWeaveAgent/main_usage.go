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

// cliUsageSource hands the monitor to the tunnel, or nil when there is none.
// A monitor with no account is still handed over, because a runtime reload
// may declare a CLI later; the tunnel itself never sends an empty report, so a
// node without Codex / Claude Code still reports no quota.
//
// cliUsageSource 把 monitor 交给隧道，没有时返回 nil。没有任何账户的 monitor 也照样交出，
// 因为之后的运行时重新加载可能会声明 CLI；隧道自己从不发送空的报告，因此没有 Codex /
// Claude Code 的节点仍然不会上报额度。
func cliUsageSource(monitor *cliusage.Monitor) tunnel.CLIUsageSource {
	if monitor == nil {
		return nil
	}
	return monitor
}

// cliUsageProviders selects quota readers only for locally declared CLI
// backends, one per runtime, each reading the login under the runtime's own
// AccountDir.
//
// cliUsageProviders 只为本地声明的 CLI 后端选择额度读取器，每个运行时一个，各自读取
// 该运行时 AccountDir 下的登录。
func cliUsageProviders(configs []runtime.Config) []cliusage.Provider {
	var providers []cliusage.Provider
	for _, cfg := range configs {
		switch cfg.Kind {
		case runtime.KindCodex:
			providers = append(providers, cliusage.Provider{RuntimeID: cfg.ID, Kind: cfg.Kind, Read: codexcli.AccountUsageReader(cfg.AccountDir), Source: cfg.AccountDir})
		case runtime.KindClaude:
			providers = append(providers, cliusage.Provider{RuntimeID: cfg.ID, Kind: cfg.Kind, Read: claudecode.AccountUsageReader(cfg.AccountDir), Source: cfg.AccountDir})
		}
	}
	return providers
}

// newCLIUsageMonitor builds the monitor for the locally declared CLI backends.
// newCLIUsageMonitor 为本地声明的 CLI 后端构造 monitor。
func newCLIUsageMonitor(configs []runtime.Config, clock runtime.Clock) *cliusage.Monitor {
	return cliusage.NewMonitor(clock, cliUsageProviders(configs))
}
