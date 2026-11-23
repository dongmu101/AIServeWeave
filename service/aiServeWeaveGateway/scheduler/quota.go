package scheduler

import (
	"log/slog"
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

// quotaVerdict is what one quota snapshot says about one candidate. known is
// false when there is nothing to go on — no snapshot, one that is not ready or
// too old — and every other field is then zero. spentUntil is the latest reset
// among the windows that make the candidate quotaSpent; checkedAt is when the
// Agent took the snapshot.
//
// quotaVerdict 是一份额度快照对一个候选的结论。没有可依据的信息时——没有快照、快照
// 未就绪或过旧——known 为 false，其余字段都是零值。spentUntil 是使候选成为 quotaSpent
// 的那些窗口中最晚的重置时刻；checkedAt 是 Agent 获取快照的时刻。
type quotaVerdict struct {
	known      bool
	tier       quotaTier
	spentUntil time.Time
	checkedAt  time.Time
}

// accountQuota judges the account behind one (runtime, model) candidate on a
// node from that node's quota snapshot. An account with no snapshot, a
// snapshot that is not ready or too old ranks quotaOK and is not known;
// unknown is never treated as spent. lowPercent <= 0 disables the quotaLow
// tier.
//
// receivedAt is when the Gateway received the report. The snapshot counts as
// taken at the earlier of that and the Agent's CheckedAt: an Agent clock
// running ahead must not make an old snapshot look newer than a refusal
// (reconcileQuota), and a report resent on reconnect still carries its old
// CheckedAt.
//
// accountQuota 依据节点的额度快照，判断该节点上一个 (运行时, 模型) 候选背后的账户。
// 没有快照、快照未就绪或过旧时定为 quotaOK 且 known 为 false：未知从不被当作用完。
// lowPercent <= 0 时关闭 quotaLow 档位。
//
// receivedAt 是 Gateway 收到上报的时刻。快照的获取时刻取它与 Agent 的 CheckedAt 中
// 较早者：偏快的 Agent 时钟不能让一份旧快照显得比一次拒绝更新（reconcileQuota），而
// 重连时重发的上报仍带着旧的 CheckedAt。
func accountQuota(accounts []cliusagestatus.Account, receivedAt time.Time, runtimeID string, kind runtime.Kind, model string, lowPercent float64, now time.Time) quotaVerdict {
	if kind != runtime.KindClaude && kind != runtime.KindCodex {
		return quotaVerdict{}
	}
	account, ok := findQuotaAccount(accounts, runtimeID, kind)
	if !ok || account.Status != quotaStatusReady || account.CheckedAt.IsZero() {
		return quotaVerdict{}
	}
	checkedAt := account.CheckedAt
	if !receivedAt.IsZero() && receivedAt.Before(checkedAt) {
		checkedAt = receivedAt
	}
	if now.Sub(checkedAt) > maxQuotaSnapshotAge {
		return quotaVerdict{}
	}
	v := quotaVerdict{known: true, checkedAt: checkedAt}
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
			v.tier = quotaSpent
			if w.ResetsAt.After(v.spentUntil) {
				v.spentUntil = w.ResetsAt
			}
		case lowPercent > 0 && w.UsedPercent >= lowPercent && v.tier < quotaLow:
			v.tier = quotaLow
		}
	}
	return v
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

// quotaAction is what reconcileQuota did to a cooldown, for the caller to log
// once it has released the registry's lock.
//
// quotaAction 是 reconcileQuota 对冷却做了什么，供调用方在释放注册表锁之后记录日志。
type quotaAction uint8

const (
	quotaKept quotaAction = iota
	quotaReleased
	quotaExtended
)

// reconcileQuota lets a quota snapshot sustain or end a cooldown that a real
// rate_limited answer started. It never starts one: a snapshot alone only
// reorders (see the file comment), so an account it calls spent but that has
// not refused anything is still tried, last.
//
//   - A snapshot taken after the refusal that no longer calls the candidate
//     spent ends the cooldown at once: the quota came back (a reset, a plan
//     change) and the Agent's own usage query, which runs no inference, is
//     what found out.
//   - A fresh snapshot that still calls it spent keeps it aside past
//     MaxRateLimitCooldown, up to the window's reset, instead of letting a
//     real request find out again. It is extended in steps of at most
//     MaxRateLimitCooldown, and only once less than half a step remains, so
//     a cooldown is rewritten (and, when shared, stored) a few times an hour
//     at most, and each step needs a snapshot fresh enough to vouch for it.
//
// reconcileQuota 让额度快照延续或结束由真实 rate_limited 应答开启的冷却。它从不开启
// 冷却：单靠快照只调整顺序（见文件注释），因此被它判为用完、但从未拒绝过请求的账户
// 仍会被尝试，只是排在最后。
//
//   - 在拒绝之后获取、且不再判定该候选用完的快照立即结束冷却：额度已经恢复（重置、
//     套餐变化），发现这一点的是 Agent 自己的用量查询，它不运行推理。
//   - 仍判定其用完的新鲜快照让它在 MaxRateLimitCooldown 之后继续被搁置，直到窗口重置，
//     而不是让真实请求再去试一次。每次延长至多 MaxRateLimitCooldown，且只在剩余不足
//     半步时延长，因此一条冷却每小时至多被改写（共享时写入存储）几次，而每一步都需要
//     一份足够新鲜的快照来担保。
func (r *breakerRegistry) reconcileQuota(c Candidate, v quotaVerdict, now time.Time) (quotaAction, Cooldown, bool) {
	if !v.known {
		return quotaKept, Cooldown{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cd := r.cooldownLocked(c)
	if cd.Until.IsZero() || !now.Before(cd.Until) {
		return quotaKept, cd, false
	}
	switch {
	case v.tier != quotaSpent && !cd.Since.IsZero() && v.checkedAt.After(cd.Since):
		return quotaReleased, Cooldown{}, r.setLimitedLocked(c, Cooldown{})
	case v.tier == quotaSpent && v.spentUntil.After(cd.Until) && cd.Until.Sub(now) < r.maxLimitCooldown/2:
		next := Cooldown{Since: cd.Since, Until: minTime(v.spentUntil, now.Add(r.maxLimitCooldown))}
		if !next.Until.After(cd.Until) {
			return quotaKept, cd, false
		}
		return quotaExtended, next, r.setLimitedLocked(c, next)
	}
	return quotaKept, cd, false
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// applyQuota runs reconcileQuota for one candidate and logs what changed.
//
// applyQuota 为一个候选运行 reconcileQuota，并记录发生的变化。
func (r *breakerRegistry) applyQuota(c Candidate, v quotaVerdict, now time.Time) {
	action, cd, dropped := r.reconcileQuota(c, v, now)
	if dropped {
		r.logger.Warn("shared cooldown write dropped; the store is not keeping up",
			slog.String("node_id", c.NodeID), slog.String("runtime_id", c.RuntimeID))
	}
	switch action {
	case quotaReleased:
		r.logger.Info("candidate quota is back per the Agent's usage report; ending its cooldown early",
			slog.String("node_id", c.NodeID), slog.String("runtime_id", c.RuntimeID))
	case quotaExtended:
		r.logger.Info("candidate quota still spent per the Agent's usage report; extending its cooldown",
			slog.String("node_id", c.NodeID), slog.String("runtime_id", c.RuntimeID),
			slog.Time("until", cd.Until))
	}
}
