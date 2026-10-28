package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
)

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

// defaultConfigPath is the config file in the working directory used by both
// normal startup and the local setup page when -config is not explicitly passed.
//
// defaultConfigPath 是未显式传入 -config 时，正常启动与本地设置页面
// 共用的当前工作目录下的配置文件。
const defaultConfigPath = "config.yaml"

// defaultConfigUIAddr is the local setup page's loopback address, used during
// normal startup and in the standalone -config-ui mode.
//
// defaultConfigUIAddr 是本地设置页面的回环地址，正常启动与独立的
// -config-ui 模式均使用此默认值。
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
//
// A hosted provider's key is read from the environment variable named by
// api_key_env at this point; an unset variable leaves the key empty, which
// the provider then rejects as unauthorized rather than failing startup.
// api_key_env and models apply to hosted kinds only, the same rule the
// setup page follows, so a CLI's start-up never depends on whether some
// environment variable happens to be set. Duplicate model IDs are dropped.
//
// 托管供应商的 Key 在此处从 api_key_env 指定的环境变量读取；变量未设置时 Key
// 为空，由供应商以未授权拒绝，而不是让启动失败。api_key_env 与 models 只对托管
// 种类生效（与设置页面的规则相同），本机 CLI 能否启动从不取决于某个环境变量
// 是否碰巧存在。重复的模型 ID 会被去掉。
func runtimesFromConfig(declared []agentconfig.RuntimeConfig) []runtime.Config {
	if len(declared) == 0 {
		return nil
	}
	out := make([]runtime.Config, len(declared))
	for i, rc := range declared {
		out[i] = runtime.Config{ID: rc.ID, Kind: runtime.Kind(rc.Kind), BaseURL: rc.BaseURL, ReasoningParser: rc.ReasoningParser}
		if !out[i].Kind.IsHosted() {
			continue
		}
		if rc.APIKeyEnv != "" {
			out[i].APIKey = os.Getenv(rc.APIKeyEnv)
		}
		for _, id := range rc.Models.IDs() {
			if !slices.Contains(out[i].Models, id) {
				out[i].Models = append(out[i].Models, id)
			}
		}
	}
	return out
}
