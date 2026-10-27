package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/agentupgrade"
	"AIServeWeave/service/aiServeWeaveAgent/localdiscovery"
	"AIServeWeave/service/aiServeWeaveAgent/modelpull"
)

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
