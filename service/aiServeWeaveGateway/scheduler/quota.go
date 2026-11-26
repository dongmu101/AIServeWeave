package scheduler

import (
	"log/slog"
	"math"
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
// is close to, or at, the end of a window. It also weighs time: an account
// whose window resets soon with allowance it will not use is tried first,
// since that allowance is lost at the reset, and one near its limit but about
// to reset is not demoted.
//
// Demotion only reorders. The snapshot is untrusted Agent input and minutes
// old, so an account it calls spent is tried last, never skipped: when every
// other candidate fails, the request still reaches it, and its own answer is
// what decides.
//
// 本文件按剩余额度给 CLI 订阅账户排序，让请求在原本要用的账户用完之前就换到另一个
// 账户，而不是失败一次之后才换。breaker.go 里的冷却是被动的一半：它从 rate_limited
// 应答得知账户已用完。这里是主动的一半：读取每个 Agent 上报的额度快照（cli_usage，每隔
// 几分钟刷新），把接近或已到窗口上限的账户降级。它同时考虑时间：窗口即将重置、且还有用不完
// 额度的账户先尝试，因为这部分额度在重置时作废；接近上限但马上就重置的账户不降级。
//
// 降级只调整顺序。快照是不受信任的 Agent 输入，且有几分钟的延迟，因此被它判为用完的
// 账户排到最后尝试，而不是被跳过：其他候选都失败时，请求仍会到达它，由它自己的应答
// 做决定。

// quotaTier is how close a candidate's account is to its quota limit. Larger
// is worse; every ordering policy tries a lower tier first (see quotaRank and
// orderRank). The zero value is quotaOK, which is also what an account with
// no usable snapshot ranks as.
//
// quotaTier 表示候选账户距额度上限有多近。数值越大越差；所有排序策略都先尝试较低档位
// （见 quotaRank 与 orderRank）。零值是 quotaOK，没有可用快照的账户也按它排序。
type quotaTier int8

const (
	// quotaExpiring: a window that applies to the request resets soon with
	// allowance left that, at an even pace, would go unused, and no other
	// window that applies is being used up well ahead of its pace. Such an
	// account is tried first: what it does not spend before the reset is
	// lost, while every other account keeps what it does not spend now. A
	// conversation's pinned account still comes before it (orderRank), so a
	// conversation in progress is not moved off its prompt cache.
	// quotaExpiring：某个适用于本请求的窗口即将重置，按均匀速度它还会剩下用不完的额度，
	// 且其他适用的窗口都没有明显快于均匀速度地消耗。这样的账户先尝试：它在重置前没用掉
	// 的额度会作废，而其他账户此刻没用掉的额度仍然留着。对话的被钉选账户仍排在它之前
	// （orderRank），因此进行中的对话不会被迁离其提示缓存。
	quotaExpiring quotaTier = -1
	quotaOK       quotaTier = 0
	// quotaLow: some window that applies to the request is at or above the
	// low-water percentage and, at an even pace, would run out before it
	// resets. One that resets soon enough for its remainder to last is not
	// low: it is about to come back anyway.
	// quotaLow：某个适用于本请求的窗口达到或超过低水位百分比，且按均匀速度会在重置前
	// 用完。剩余额度足以撑到重置的窗口不算紧张：它马上就会恢复。
	quotaLow quotaTier = 1
	// quotaSpent: some window that applies to the request is at 100% and has
	// not reset yet.
	// quotaSpent：某个适用于本请求的窗口已到 100% 且尚未重置。
	quotaSpent quotaTier = 2
)

// String names the tier for metric labels.
//
// String 为指标标签给出档位名称。
func (t quotaTier) String() string {
	switch t {
	case quotaExpiring:
		return "expiring"
	case quotaLow:
		return "low"
	case quotaSpent:
		return "spent"
	}
	return "ok"
}

// defaultQuotaLowPercent is Config.QuotaLowPercent's default.
//
// defaultQuotaLowPercent 是 Config.QuotaLowPercent 的默认值。
const defaultQuotaLowPercent = 90

// defaultQuotaExpiringPercent is Config.QuotaExpiringPercent's default: the
// last tenth of a window, half an hour of a five-hour window and about 17
// hours of a weekly one.
//
// defaultQuotaExpiringPercent 是 Config.QuotaExpiringPercent 的默认值：窗口的最后十分
// 之一，即五小时窗口的最后半小时、周窗口的最后约 17 小时。
const defaultQuotaExpiringPercent = 10

// quotaPaceMargin is how far, as a share of a window's allowance, its usage
// must be off an even pace to count: an expiring window needs this much more
// left than its share of time, and another window this much less to keep the
// account from quotaExpiring. An account slightly ahead of pace early in the
// week is normal and must not hold back an expiring five-hour window.
//
// quotaPaceMargin 是窗口用量需要偏离均匀速度多少（按额度份额计）才算数：即将重置的窗口
// 剩余额度须比剩余时间份额多出这么多；其他窗口须比剩余时间份额少这么多，才会阻止账户成为
// quotaExpiring。一周开头略快于均匀速度是常态，不应拦住一个即将重置的五小时窗口。
const quotaPaceMargin = 0.1

// maxWindowMinutes bounds a reported window length; anything longer is
// treated as unknown rather than trusted into the arithmetic.
//
// maxWindowMinutes 限定上报的窗口时长；更长的值按未知处理，而不是被信任地代入计算。
const maxWindowMinutes = 366 * 24 * 60

// quotaPolicy is the configured thresholds accountQuota judges by, as
// Scheduler keeps them after defaulting. lowPercent <= 0 disables quotaLow;
// expiringShare <= 0 disables quotaExpiring.
//
// quotaPolicy 是 accountQuota 据以判断的已配置阈值，即 Scheduler 取默认值之后保存的
// 值。lowPercent <= 0 关闭 quotaLow；expiringShare <= 0 关闭 quotaExpiring。
type quotaPolicy struct {
	lowPercent    float64
	expiringShare float64
}

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
// Agent took the snapshot; used is the highest used percentage among the
// windows that apply, which orders accounts inside quotaLow.
//
// quotaVerdict 是一份额度快照对一个候选的结论。没有可依据的信息时——没有快照、快照
// 未就绪或过旧——known 为 false，其余字段都是零值。spentUntil 是使候选成为 quotaSpent
// 的那些窗口中最晚的重置时刻；checkedAt 是 Agent 获取快照的时刻；used 是适用窗口中最高
// 的已用百分比，用于 quotaLow 档内排序。
type quotaVerdict struct {
	known      bool
	tier       quotaTier
	spentUntil time.Time
	checkedAt  time.Time
	used       float64
}

// usedPercent is v.used as the small integer scored keeps, 0 to 100.
//
// usedPercent 是 scored 保存的 v.used 小整数形式，取值 0 到 100。
func (v quotaVerdict) usedPercent() uint8 {
	return uint8(min(max(v.used, 0), 100))
}

// accountQuota judges the account behind one (runtime, model) candidate on a
// node from that node's quota snapshot. An account with no snapshot, a
// snapshot that is not ready or too old ranks quotaOK and is not known;
// unknown is never treated as spent.
//
// Pace compares, for each window, the share of its allowance left with the
// share of its length left. Less allowance than time means it is being used
// faster than an even pace that would last exactly until the reset; more
// means some of it will go unused. A window whose length is unknown has no
// pace: it can make an account low by the threshold alone, and keeps it from
// quotaExpiring.
//
// 速度比较的是每个窗口剩余的额度份额与剩余的时间份额。额度少于时间，说明它比「恰好用到
// 重置」的均匀速度消耗得更快；额度多于时间，说明有一部分会用不完。时长未知的窗口没有
// 速度可言：它只凭阈值就能让账户成为紧张，并阻止账户成为 quotaExpiring。
//
// receivedAt is when the Gateway received the report. The snapshot counts as
// taken at the earlier of that and the Agent's CheckedAt: an Agent clock
// running ahead must not make an old snapshot look newer than a refusal
// (reconcileQuota), and a report resent on reconnect still carries its old
// CheckedAt.
//
// accountQuota 依据节点的额度快照，判断该节点上一个 (运行时, 模型) 候选背后的账户。
// 没有快照、快照未就绪或过旧时定为 quotaOK 且 known 为 false：未知从不被当作用完。
//
// receivedAt 是 Gateway 收到上报的时刻。快照的获取时刻取它与 Agent 的 CheckedAt 中
// 较早者：偏快的 Agent 时钟不能让一份旧快照显得比一次拒绝更新（reconcileQuota），而
// 重连时重发的上报仍带着旧的 CheckedAt。
func accountQuota(accounts []cliusagestatus.Account, receivedAt time.Time, runtimeID string, kind runtime.Kind, model string, policy quotaPolicy, now time.Time) quotaVerdict {
	account, checkedAt, ok := freshQuotaAccount(accounts, receivedAt, runtimeID, kind, now)
	if !ok {
		return quotaVerdict{}
	}
	v := quotaVerdict{known: true, checkedAt: checkedAt}
	// expiring: some window resets soon with allowance it will not use.
	// steady: no window is being used well ahead of its pace.
	// expiring：某个窗口即将重置且有用不完的额度。steady：没有窗口明显快于其速度。
	expiring, steady := false, true
	for i := range account.Windows {
		w := &account.Windows[i]
		if !liveWindow(kind, w, model, now) {
			continue
		}
		used := max(w.UsedPercent, 0)
		v.used = max(v.used, used)
		// left and timeLeft are the shares of the allowance and of the
		// window still to come; timeLeft is negative when the length is
		// unknown.
		// left 与 timeLeft 是尚余的额度份额与时间份额；时长未知时 timeLeft 为负。
		left := max(0, 100-used) / 100
		timeLeft := -1.0
		if d := windowDuration(kind, w); d > 0 {
			timeLeft = min(1, float64(w.ResetsAt.Sub(now))/float64(d))
		}
		switch {
		case used >= 100:
			v.tier = quotaSpent
			if w.ResetsAt.After(v.spentUntil) {
				v.spentUntil = w.ResetsAt
			}
		case policy.lowPercent > 0 && used >= policy.lowPercent && (timeLeft < 0 || left < timeLeft) && v.tier < quotaLow:
			v.tier = quotaLow
		}
		switch {
		case policy.expiringShare > 0 && timeLeft >= 0 && timeLeft <= policy.expiringShare && left > timeLeft+quotaPaceMargin:
			expiring = true
		case timeLeft < 0 || left < timeLeft-quotaPaceMargin:
			steady = false
		}
	}
	if v.tier == quotaOK && expiring && steady {
		v.tier = quotaExpiring
	}
	return v
}

// freshQuotaAccount returns the account behind runtimeID when its snapshot is
// ready and young enough to act on, with the time it counts as taken: the
// earlier of the Agent's CheckedAt and receivedAt (see accountQuota).
//
// freshQuotaAccount 在快照已就绪且足够新、可以据以行动时，返回 runtimeID 背后的账户，
// 以及该快照视作获取的时刻：Agent 的 CheckedAt 与 receivedAt 中较早者（见 accountQuota）。
func freshQuotaAccount(accounts []cliusagestatus.Account, receivedAt time.Time, runtimeID string, kind runtime.Kind, now time.Time) (*cliusagestatus.Account, time.Time, bool) {
	if kind != runtime.KindClaude && kind != runtime.KindCodex {
		return nil, time.Time{}, false
	}
	account, ok := findQuotaAccount(accounts, runtimeID, kind)
	if !ok || account.Status != quotaStatusReady || account.CheckedAt.IsZero() {
		return nil, time.Time{}, false
	}
	checkedAt := account.CheckedAt
	if !receivedAt.IsZero() && receivedAt.Before(checkedAt) {
		checkedAt = receivedAt
	}
	if now.Sub(checkedAt) > maxQuotaSnapshotAge {
		return nil, time.Time{}, false
	}
	return account, checkedAt, true
}

// liveWindow reports whether w limits requests for model right now: it
// applies to the model, has not reset, and carries a finite figure.
//
// liveWindow 报告 w 此刻是否限制 model 的请求：它适用于该模型、尚未重置，且数字是有限数。
func liveWindow(kind runtime.Kind, w *cliusagestatus.Window, model string, now time.Time) bool {
	// A window past its reset time says nothing about now: whatever it had
	// used is gone. A figure that is not a finite number is untrusted input
	// gone wrong, not a measurement.
	// 已过重置时间的窗口对此刻没有意义：它用掉的额度已经清零。不是有限数的数字是出错的
	// 不受信任输入，而不是测量值。
	return !w.ResetsAt.IsZero() && now.Before(w.ResetsAt) && w.Applies(kind, model) &&
		!math.IsNaN(w.UsedPercent) && !math.IsInf(w.UsedPercent, 0)
}

// windowDuration is how long w runs from one reset to the next: what the
// Agent reported, or, for Claude's windows, which carry no length, what
// their names say. Zero means unknown.
//
// windowDuration 是 w 从一次重置到下一次的时长：取 Agent 上报的值；Claude 的窗口不带
// 时长，按窗口名确定。零表示未知。
func windowDuration(kind runtime.Kind, w *cliusagestatus.Window) time.Duration {
	if w.DurationMinutes > 0 && w.DurationMinutes <= maxWindowMinutes {
		return time.Duration(w.DurationMinutes) * time.Minute
	}
	if kind == runtime.KindClaude {
		switch w.Name {
		case "five_hour":
			return 5 * time.Hour
		case "seven_day", "seven_day_sonnet", "seven_day_opus":
			return 7 * 24 * time.Hour
		}
	}
	return 0
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
