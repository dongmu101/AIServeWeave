package scheduler

import (
	"strings"
	"time"

	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
)

// This file ranks CLI subscription accounts by how much quota they have left,
// so a request moves to another account before the one it would have used
// runs out, rather than after a failed attempt. The cooldown in breaker.go is
// the reactive half: it learns that an account is spent from a rate_limited
// answer. This is the proactive half: it reads the quota snapshot every Agent
// reports (cli_usage, refreshed every few minutes) and demotes an account that
// is close to, or at, the end of a window.
//
// Demotion only reorders. The snapshot is untrusted Agent input and minutes
// old, so an account it calls spent is tried last, never skipped: when every
// other candidate fails, the request still reaches it, and its own answer is
// what decides.
//
// 本文件按剩余额度给 CLI 订阅账户排序，让请求在原本要用的账户用完之前就换到另一个
// 账户，而不是失败一次之后才换。breaker.go 里的冷却是被动的一半：它从 rate_limited
// 应答得知账户已用完。这里是主动的一半：读取每个 Agent 上报的额度快照（cli_usage，每隔
// 几分钟刷新），把接近或已到窗口上限的账户降级。
//
// 降级只调整顺序。快照是不受信任的 Agent 输入，且有几分钟的延迟，因此被它判为用完的
// 账户排到最后尝试，而不是被跳过：其他候选都失败时，请求仍会到达它，由它自己的应答
// 做决定。

// quotaTier is how close a candidate's account is to its quota limit. Larger
// is worse; every ordering policy tries a lower tier first.
//
// quotaTier 表示候选账户距额度上限有多近。数值越大越差；所有排序策略都先尝试较低档位。
type quotaTier uint8

const (
	quotaOK quotaTier = iota
	// quotaLow: some window that applies to the request is at or above the
	// low-water percentage.
	// quotaLow：某个适用于本请求的窗口达到或超过低水位百分比。
	quotaLow
	// quotaSpent: some window that applies to the request is at 100% and has
	// not reset yet.
	// quotaSpent：某个适用于本请求的窗口已到 100% 且尚未重置。
	quotaSpent
)

// defaultQuotaLowPercent is Config.QuotaLowPercent's default.
//
// defaultQuotaLowPercent 是 Config.QuotaLowPercent 的默认值。
const defaultQuotaLowPercent = 90

// maxQuotaSnapshotAge bounds how old a snapshot may be and still rank its
// account. The Agent refreshes every five minutes; one that stopped refreshing
// (a CLI that no longer starts, a wedged monitor) must not keep an account
// demoted on the strength of a figure from hours ago.
//
// maxQuotaSnapshotAge 限定快照还能用来给账户排序的最大年龄。Agent 每五分钟刷新一次；
// 一个停止刷新的 Agent（CLI 启动不了、monitor 卡住）不应凭几个小时前的数字让账户一直
// 处于降级状态。
const maxQuotaSnapshotAge = 15 * time.Minute

// quotaStatusReady is the account status whose windows carry real figures.
//
// quotaStatusReady 是窗口携带真实数据的账户状态。
const quotaStatusReady = "ready"

// accountQuotaTier ranks the account behind one (runtime, model) candidate on
// a node from that node's quota snapshot. An account with no snapshot, a
// snapshot that is not ready or too old, or no window applying to model ranks
// quotaOK: unknown is never treated as spent. lowPercent <= 0 disables the
// quotaLow tier.
//
// accountQuotaTier 依据节点的额度快照，为该节点上一个 (运行时, 模型) 候选背后的账户
// 定档。没有快照、快照未就绪或过旧、或没有适用于 model 的窗口时定为 quotaOK：未知从不
// 被当作用完。lowPercent <= 0 时关闭 quotaLow 档位。
func accountQuotaTier(accounts []cliusagestatus.Account, runtimeID string, kind runtime.Kind, model string, lowPercent float64, now time.Time) quotaTier {
	if kind != runtime.KindClaude && kind != runtime.KindCodex {
		return quotaOK
	}
	account, ok := findQuotaAccount(accounts, runtimeID, kind)
	if !ok || account.Status != quotaStatusReady || account.CheckedAt.IsZero() || now.Sub(account.CheckedAt) > maxQuotaSnapshotAge {
		return quotaOK
	}
	tier := quotaOK
	for i := range account.Windows {
		w := &account.Windows[i]
		// A window past its reset time says nothing about now: whatever it
		// had used is gone.
		// 已过重置时间的窗口对此刻没有意义：它用掉的额度已经清零。
		if w.ResetsAt.IsZero() || !now.Before(w.ResetsAt) || !windowApplies(kind, w, model) {
			continue
		}
		switch {
		case w.UsedPercent >= 100:
			return quotaSpent
		case lowPercent > 0 && w.UsedPercent >= lowPercent:
			tier = quotaLow
		}
	}
	return tier
}

// findQuotaAccount returns the account reported for runtimeID. An Agent that
// predates multi-account support reports an empty runtime ID and at most one
// account per kind, which then belongs to whichever runtime of that kind asks.
//
// findQuotaAccount 返回为 runtimeID 上报的账户。早于多账户支持的 Agent 上报空的运行时
// ID，且每种 kind 至多一个账户，此时它属于该 kind 的任意运行时。
func findQuotaAccount(accounts []cliusagestatus.Account, runtimeID string, kind runtime.Kind) (*cliusagestatus.Account, bool) {
	var legacy *cliusagestatus.Account
	for i := range accounts {
		a := &accounts[i]
		if a.Kind != kind {
			continue
		}
		if a.RuntimeID == runtimeID {
			return a, true
		}
		if a.RuntimeID == "" && legacy == nil {
			legacy = a
		}
	}
	return legacy, legacy != nil
}

// windowApplies reports whether a quota window limits requests for model.
// Claude's five-hour and seven-day windows cover every model; its per-family
// weekly windows cover only that family, so a spent Opus allowance does not
// demote the account for Sonnet. Codex reports windows per metering bucket;
// only the general "codex" bucket is known to cover every model, so other
// buckets are not used — a demotion based on a guessed mapping would move
// traffic for no reason.
//
// windowApplies 报告某个额度窗口是否限制 model 的请求。Claude 的五小时与七天窗口覆盖
// 所有模型；按模型系列划分的周窗口只覆盖该系列，因此 Opus 额度用完不会让该账户在
// Sonnet 上降级。Codex 按计量桶上报窗口；只知道通用的 "codex" 桶覆盖所有模型，其他桶
// 不使用——基于猜测的映射来降级，只会让流量无谓地迁移。
func windowApplies(kind runtime.Kind, w *cliusagestatus.Window, model string) bool {
	if kind == runtime.KindCodex {
		return w.Bucket == "" || w.Bucket == "codex"
	}
	switch w.Name {
	case "five_hour", "seven_day":
		return true
	case "seven_day_sonnet":
		return strings.Contains(strings.ToLower(model), "sonnet")
	case "seven_day_opus":
		return strings.Contains(strings.ToLower(model), "opus")
	}
	return false
}
