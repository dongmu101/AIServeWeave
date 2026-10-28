package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
	"AIServeWeave/service/aiServeWeaveAgent/cliusage"
)

// errNoReloadSource means the Agent was started without a config file to
// re-read, so there is nothing a reload could change.
//
// errNoReloadSource 表示 Agent 启动时没有可重新读取的配置文件，重新加载无从改变任何东西。
var errNoReloadSource = errors.New("agent: no config file to reload runtimes from")

// runtimeReloader re-reads the -config file's runtimes: list and reconciles
// the running Agent with it — removing runtimes no longer declared, adding new
// ones and replacing changed ones — without a restart, so a new CLI account
// or provider comes online in seconds. It touches only the runtimes the file
// declares (plus -ollama-url's, which the file cannot change): auto-discovered
// and Gateway-configured instances are left alone, and every other setting
// (Gateway endpoints, certificates, the runtime allowlist, metrics, logging)
// still needs a restart, because the tunnel and listeners were built from it.
//
// runtimeReloader 重新读取 -config 文件的 runtimes: 列表，并据此调和运行中的 Agent
// ——移除不再声明的运行时、添加新的、替换改动过的——无需重启，使新的 CLI 账户或供应商在
// 几秒内上线。它只触碰文件声明的运行时（外加 -ollama-url 声明的那个，文件改变不了它）：
// 自动发现的与 Gateway 下发的实例不受影响；其他所有设置（Gateway 端点、证书、运行时
// 白名单、指标、日志）仍需重启，因为隧道与监听器是按它们建起来的。
type runtimeReloader struct {
	path    string
	extra   []runtime.Config
	local   *localDeclarations
	manager runtime.Manager
	usage   *cliusage.Monitor
	// usageStarted is called when the quota monitor has accounts but no
	// refresh loop (see cliusage.Monitor.Replace), so the caller can start
	// one; the loop returns at once while there is no account.
	// usageStarted 在额度监视器有账户却没有刷新循环时调用（见
	// cliusage.Monitor.Replace），以便调用方启动一个；没有账户时该循环会立即返回。
	usageStarted func()
	allowed      []string
	logger       *slog.Logger

	mu      sync.Mutex
	current []runtime.Config
}

// runtimeChanges summarises one reload, by runtime ID.
// runtimeChanges 按运行时 ID 汇总一次重新加载。
type runtimeChanges struct {
	Added, Replaced, Removed, Failed []string
}

// Reload applies the file's current runtimes. A file that fails to load, or a
// declaration list that fails validation, changes nothing; past that point
// each runtime is reconciled on its own and a failure leaves that one runtime
// as it was, reported in Failed and in the returned error.
//
// Reload 应用文件当前的运行时声明。文件加载失败或声明列表校验失败时什么都不改变；过了
// 这一关，每个运行时各自调和，某个运行时失败只让它保持原样，并在 Failed 与返回的错误中
// 报告。
func (r *runtimeReloader) Reload(ctx context.Context) (runtimeChanges, error) {
	if r == nil || r.path == "" {
		return runtimeChanges{}, errNoReloadSource
	}
	cfg, err := agentconfig.Load(r.path)
	if err != nil {
		return runtimeChanges{}, err
	}
	next := append(runtimesFromConfig(cfg.Runtimes), r.extra...)
	if err := validateDeclared(next); err != nil {
		return runtimeChanges{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	previous := make(map[string]runtime.Config, len(r.current))
	for _, rc := range r.current {
		previous[rc.ID] = rc
	}
	wanted := make(map[string]bool, len(next))
	for _, rc := range next {
		wanted[rc.ID] = true
	}

	// The factories must admit a new ID before it is added; the final set
	// below drops whatever failed again.
	// 工厂必须先接受新 ID 才能添加；下面最终的集合会再次去掉失败的那些。
	r.local.set(append(slices.Clone(r.current), next...))

	var changes runtimeChanges
	var problems []error
	var applied []runtime.Config
	for _, rc := range r.current {
		if wanted[rc.ID] {
			continue
		}
		if err := r.manager.Remove(ctx, rc.ID); err != nil && !errors.Is(err, runtime.ErrRuntimeNotFound) {
			changes.Failed = append(changes.Failed, rc.ID)
			problems = append(problems, fmt.Errorf("remove %q: %w", rc.ID, err))
			applied = append(applied, rc)
			continue
		}
		changes.Removed = append(changes.Removed, rc.ID)
	}
	for _, rc := range next {
		old, declared := previous[rc.ID]
		if declared && reflect.DeepEqual(old, rc) {
			applied = append(applied, rc)
			continue
		}
		rc.AllowUnavailable = true
		apply, verb := r.manager.Add, "add"
		if _, running := r.manager.Get(rc.ID); running {
			apply, verb = r.manager.Replace, "replace"
		}
		if err := apply(ctx, rc); err != nil {
			changes.Failed = append(changes.Failed, rc.ID)
			problems = append(problems, fmt.Errorf("%s %q: %w", verb, rc.ID, err))
			if declared {
				applied = append(applied, old)
			}
			continue
		}
		rc.AllowUnavailable = false
		applied = append(applied, rc)
		if verb == "add" {
			changes.Added = append(changes.Added, rc.ID)
		} else {
			changes.Replaced = append(changes.Replaced, rc.ID)
		}
	}

	r.current = applied
	r.local.set(applied)
	if r.usage != nil && r.usage.Replace(cliUsageProviders(applied)) && r.usageStarted != nil {
		r.usageStarted()
	}
	r.logChanges(changes, applied)
	return changes, errors.Join(problems...)
}

// validateDeclared rejects a declaration list a reload must not apply at all:
// a duplicated ID, or a runtime whose own configuration is invalid.
//
// validateDeclared 拒绝重新加载完全不应应用的声明列表：重复的 ID，或自身配置非法的运行时。
func validateDeclared(declared []runtime.Config) error {
	seen := make(map[string]bool, len(declared))
	for _, rc := range declared {
		if seen[rc.ID] {
			return fmt.Errorf("runtime id %q is declared more than once", rc.ID)
		}
		seen[rc.ID] = true
		if err := rc.Normalize().Validate(); err != nil {
			return err
		}
	}
	return nil
}

// logChanges reports a reload, and warns about any applied runtime the local
// allowlist would refuse to execute, since the allowlist itself only changes
// on restart.
//
// logChanges 报告一次重新加载，并对本地白名单会拒绝执行的已应用运行时发出警告，因为
// 白名单本身只在重启时改变。
func (r *runtimeReloader) logChanges(changes runtimeChanges, applied []runtime.Config) {
	r.logger.Info("runtimes reloaded / 运行时已重新加载",
		slog.Any("added", changes.Added), slog.Any("replaced", changes.Replaced),
		slog.Any("removed", changes.Removed), slog.Any("failed", changes.Failed))
	if len(r.allowed) == 0 {
		return
	}
	for _, rc := range applied {
		if !slices.Contains(r.allowed, rc.ID) {
			r.logger.Warn("reloaded runtime is outside the runtime allowlist and will not execute until the allowlist includes it and the Agent restarts / 重新加载的运行时不在运行时白名单内，需加入白名单并重启 Agent 后才会执行",
				slog.String("runtime_id", rc.ID))
		}
	}
}
