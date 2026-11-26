package cliusage

import (
	"math"
	"time"

	"AIServeWeave/common/runtime"
)

// This file makes Monitor the Agent's runtime.QuotaGuard. A CLI runtime asks
// it before every turn whether the account behind it is already known to
// refuse the model, and answers rate_limited itself when it is, without
// starting the CLI: a subscription account at its limit must not be sent
// request after request that the upstream can only refuse, which looks like
// abuse from the provider's side and gains nothing.
//
// Two things are known to refuse, and either holds the account until its
// quota may be back:
//
//   - A usage reading (the last ready one, kept across failed queries) with a
//     window that applies to the model at 100% and a reset still ahead. Usage
//     only grows until the reset, so such a reading stays true until then,
//     whatever its age.
//   - A refusal the CLI reported with the upstream's reset time (Refused).
//     It covers the gap until the next reading shows the spent window, and
//     accounts whose usage cannot be read at all. A reading taken at least
//     quotaHoldSettle after the refusal that no longer shows the refusing
//     scope spent ends it early: the quota came back (a plan change, an early
//     reset) and the usage query, which runs no inference, found out.
//
// The Gateway learns of a hold the same way it learns of a real refusal: a
// rate_limited answer whose RetryAfter runs to the reset, so it sets the
// account aside and stops asking.
//
// 本文件让 Monitor 成为 Agent 的 runtime.QuotaGuard。CLI 运行时在每个回合之前询问它：
// 背后的账户是否已知会拒绝该模型；若是，运行时自己回答 rate_limited，不启动 CLI。已到
// 上限的订阅账户不应被一个接一个地发送上游只能拒绝的请求：在服务商看来这像滥用，而且
// 毫无收获。
//
// 有两种情况已知会拒绝，任一种都会把账户搁置到额度可能恢复为止：
//
//   - 一份用量读数（最近一次 ready 的读数，查询失败时保留），其中适用于该模型的窗口已到
//     100% 且重置尚未到来。用量在重置之前只增不减，因此这样的读数无论多旧，在重置之前都
//     成立。
//   - CLI 报告的、带上游重置时间的一次拒绝（Refused）。它覆盖下一份读数显示该窗口用完
//     之前的空档，以及完全读不到用量的账户。在拒绝之后至少 quotaHoldSettle 才获取、且
//     不再显示拒绝范围用完的读数会提前结束它：额度已经恢复（套餐变化、提前重置），发现
//     这一点的是不运行推理的用量查询。
//
// Gateway 得知搁置的方式与得知真实拒绝相同：一个 RetryAfter 指向重置时刻的 rate_limited
// 应答，于是它把该账户放到一边，不再询问。

var _ runtime.QuotaGuard = (*Monitor)(nil)

// quotaHoldSettle is how long after a refusal a usage reading has to be taken
// before it may end the hold: the usage figures can trail the limiter that
// refused, and a reading from that moment would release an account that is
// in fact spent.
//
// quotaHoldSettle 是拒绝之后、用量读数至少要晚多久获取才能结束搁置：用量数字可能落后于
// 作出拒绝的限额器，那一刻的读数会放行一个实际上已用完的账户。
const quotaHoldSettle = 2 * time.Minute

// maxQuotaHold bounds how far ahead a reset is believed, past the longest
// window any CLI reports (seven days): a refusal is held at most this long,
// and a reading whose reset lies further ahead holds nothing.
//
// maxQuotaHold 限定一个重置时刻最远可信到多久之后，比任何 CLI 上报的最长窗口（七天）
// 更长：一次拒绝至多搁置这么久，重置时刻更远的读数不搁置任何东西。
const maxQuotaHold = 8 * 24 * time.Hour

// quotaHold is one refusal Refused recorded.
//
// quotaHold 是 Refused 记录的一次拒绝。
type quotaHold struct {
	runtimeID string
	// source is the provider's Source when the refusal came in; a runtime
	// since moved to another login drops it (see Replace).
	// source 是拒绝到来时来源的 Source；之后换到另一份登录的运行时会丢弃它（见 Replace）。
	source string
	// model is the refused model, or "" when the refusing limit covers every
	// model of the account.
	// model 是被拒绝的模型；拒绝它的限额覆盖账户所有模型时为 ""。
	model        string
	since, until time.Time
}

// Notice implements runtime.QuotaGuard: the CLI says the account is near or
// at a limit, so the reading is refreshed as soon as the per-minute floor
// allows.
//
// Notice 实现 runtime.QuotaGuard：CLI 表示账户接近或已到上限，因此在每分钟下限允许时
// 尽快刷新读数。
func (m *Monitor) Notice(string) { m.Nudge() }

// Refused implements runtime.QuotaGuard.
//
// Refused 实现 runtime.QuotaGuard。
func (m *Monitor) Refused(runtimeID, model string, resetsAt time.Time) {
	m.Nudge()
	now := m.clock.Now()
	if !resetsAt.After(now) {
		return
	}
	if limit := now.Add(maxQuotaHold); resetsAt.After(limit) {
		resetsAt = limit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	source := ""
	if i := m.providerLocked(runtimeID); i >= 0 {
		source = m.providers[i].Source
	}
	for i := range m.holds {
		h := &m.holds[i]
		if h.runtimeID == runtimeID && h.model == model && h.source == source {
			h.since = now
			h.until = later(h.until, resetsAt)
			return
		}
	}
	m.holds = append(m.holds, quotaHold{runtimeID: runtimeID, source: source, model: model, since: now, until: resetsAt})
}

// Hold implements runtime.QuotaGuard.
//
// Hold 实现 runtime.QuotaGuard。
func (m *Monitor) Hold(runtimeID, model string) (time.Duration, bool) {
	now := m.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	var until time.Time
	kept := m.holds[:0]
	for _, h := range m.holds {
		if !now.Before(h.until) || m.releasedLocked(h, now) {
			continue
		}
		kept = append(kept, h)
		if h.runtimeID == runtimeID && (h.model == "" || h.model == model) {
			until = later(until, h.until)
		}
	}
	clear(m.holds[len(kept):])
	m.holds = kept
	if account := m.readyLocked(runtimeID); account != nil {
		if spent, ok := spentUntil(account, model, now); ok {
			until = later(until, spent)
		}
	}
	if until.IsZero() {
		return 0, false
	}
	return until.Sub(now), true
}

// releasedLocked reports whether a reading taken quotaHoldSettle or more
// after h no longer shows h's scope spent. Callers hold m.mu.
//
// releasedLocked 报告在 h 之后至少 quotaHoldSettle 获取的读数是否已不再显示 h 的范围
// 用完。调用方持有 m.mu。
func (m *Monitor) releasedLocked(h quotaHold, now time.Time) bool {
	account := m.readyLocked(h.runtimeID)
	if account == nil || account.CheckedAt.Before(h.since.Add(quotaHoldSettle)) {
		return false
	}
	_, spent := spentUntil(account, h.model, now)
	return !spent
}

// providerLocked returns the index of runtimeID's provider, or -1. Callers
// hold m.mu.
//
// providerLocked 返回 runtimeID 的来源下标，没有时返回 -1。调用方持有 m.mu。
func (m *Monitor) providerLocked(runtimeID string) int {
	for i, p := range m.providers {
		if p.RuntimeID == runtimeID {
			return i
		}
	}
	return -1
}

// readyLocked returns runtimeID's account when it holds a ready reading.
// Callers hold m.mu.
//
// readyLocked 在 runtimeID 的账户持有 ready 读数时返回它。调用方持有 m.mu。
func (m *Monitor) readyLocked(runtimeID string) *Account {
	i := m.providerLocked(runtimeID)
	if i < 0 || i >= len(m.accounts) || m.accounts[i].Status != "ready" {
		return nil
	}
	return &m.accounts[i]
}

// pruneHoldsLocked drops the holds of runtimes that are no longer declared or
// now read another login. Callers hold m.mu.
//
// pruneHoldsLocked 丢弃已不再声明、或现在读取另一份登录的运行时的搁置。调用方持有 m.mu。
func (m *Monitor) pruneHoldsLocked() {
	kept := m.holds[:0]
	for _, h := range m.holds {
		if i := m.providerLocked(h.runtimeID); i >= 0 && m.providers[i].Source == h.source {
			kept = append(kept, h)
		}
	}
	clear(m.holds[len(kept):])
	m.holds = kept
}

// spentUntil reports whether account shows a window that applies to model
// (every-model windows only, for an empty model) at 100% or more with a reset
// still ahead, and the latest such reset. A figure that is not a finite
// number, or a reset further ahead than maxQuotaHold, is not trusted.
//
// spentUntil 报告 account 是否有适用于 model 的窗口（model 为空时只看覆盖所有模型的
// 窗口）已到 100% 或以上且重置尚未到来，并给出其中最晚的重置时刻。不是有限数的数字、
// 或比 maxQuotaHold 更远的重置时刻不予采信。
func spentUntil(account *Account, model string, now time.Time) (time.Time, bool) {
	var until time.Time
	for _, w := range account.Windows {
		if math.IsNaN(w.UsedPercent) || math.IsInf(w.UsedPercent, 0) || w.UsedPercent < 100 {
			continue
		}
		if !now.Before(w.ResetsAt) || w.ResetsAt.After(now.Add(maxQuotaHold)) || !w.Applies(account.Kind, model) {
			continue
		}
		until = later(until, w.ResetsAt)
	}
	return until, !until.IsZero()
}

// later returns the later of a and b. / later 返回 a 与 b 中较晚者。
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
