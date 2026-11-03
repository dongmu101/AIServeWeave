package main

import (
	"sync"
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

// usageRefresher re-selects the quota accounts from the effective local
// declarations. Reloads and CLI auto-discovery both change those
// declarations from different goroutines; serializing the read-and-replace
// keeps an older snapshot from overwriting a newer one.
//
// usageRefresher 依据有效的本地声明重新选取额度账户。重新加载与 CLI 自动发现会在不同
// 协程中修改这些声明；把「读取并替换」串行化，避免较旧的快照覆盖较新的快照。
type usageRefresher struct {
	mu      sync.Mutex
	monitor *cliusage.Monitor
	local   *localDeclarations
	// start launches the refresh loop; it is called only when the monitor
	// has accounts but no loop (see cliusage.Monitor.Replace).
	// start 启动刷新循环；仅在监视器有账户却没有循环时调用（见 cliusage.Monitor.Replace）。
	start func()
}

// refresh replaces the monitor's providers with those of every effective
// declaration, including a CLI the Agent registered itself.
//
// refresh 用全部有效声明（包括 Agent 自己注册的 CLI）的查询来源替换监视器的来源。
func (u *usageRefresher) refresh() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.monitor.Replace(cliUsageProviders(u.local.all())) && u.start != nil {
		u.start()
	}
}
