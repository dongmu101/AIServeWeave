package main

import (
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
	"AIServeWeave/service/aiServeWeaveAgent/cliusage"
	"AIServeWeave/service/aiServeWeaveAgent/codexcli"
)

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
