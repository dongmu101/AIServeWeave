package scheduler

import (
	"log/slog"
	"time"

	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// This file follows up one rate_limited answer from a CLI subscription
// account. The breaker sets aside the candidate that was refused, and a
// candidate is one (node, runtime, model). But a refusal is about a quota
// window, and most windows (Claude's five-hour and seven-day, Codex's general
// bucket) cover every model of the account: a client mixing a main model with
// a lighter one would otherwise walk the second straight into the same spent
// account, once per model and per replica, before the next snapshot demotes
// it. So the window that refused is worked out from the account's snapshot,
// and every other model of the account it covers rests as long as the one
// refused. A per-family window (seven_day_opus) only covers that family.
//
// The snapshot is untrusted and minutes old, and a wrong guess is cheap to
// undo: each model set aside this way carries the refusal as its start, so
// reconcileQuota releases it as soon as a later snapshot shows that model is
// not spent. Without a usable snapshot nothing spreads, as before.
//
// 本文件处理 CLI 订阅账户的一次 rate_limited 应答之后的事。熔断器搁置被拒绝的那个候选，
// 而一个候选是 (节点, 运行时, 模型)。但拒绝针对的是额度窗口，而大多数窗口（Claude 的五小时
// 与七天窗口、Codex 的通用计量桶）覆盖账户的所有模型：混用主模型与较轻模型的客户端，否则
// 会在下一份快照把它降级之前，带着第二个模型径直撞上同一个已用完的账户，每个模型、每个副本
// 各撞一次。因此依据账户快照推断是哪个窗口拒绝的，并让该窗口覆盖的账户其他模型与被拒绝的
// 模型搁置同样久。按模型系列的窗口（seven_day_opus）只覆盖该系列。
//
// 快照不受信任且有几分钟延迟，而猜错的代价很小：以这种方式搁置的每个模型都以那次拒绝作为
// 冷却开始时刻，因此只要之后的快照显示该模型并未用完，reconcileQuota 就会立即解除它。没有
// 可用快照时不扩散，与此前相同。

// quotaResetMatch is how far a window's reset may be from the moment a
// refusal's RetryAfter points at and still be taken as the window that
// refused. The two come from different clocks (RetryAfter is reckoned on the
// Agent's, the reset is upstream's), while different windows of one account
// reset hours apart.
//
// quotaResetMatch 是窗口重置时刻与拒绝的 RetryAfter 所指时刻之间允许的最大偏差，在此范围
// 内即视为拒绝来自该窗口。两者来自不同的时钟（RetryAfter 按 Agent 时钟推算，重置时刻来自
// 上游），而同一账户的不同窗口通常相隔数小时重置。
const quotaResetMatch = 5 * time.Minute

// maxSpreadModels bounds how many other models one refusal sets aside. A CLI
// account serves a handful; the bound only keeps a node advertising an
// absurd inventory from flooding the shared cooldown store.
//
// maxSpreadModels 限定一次拒绝最多搁置多少个其他模型。一个 CLI 账户只服务少数几个模型；
// 这个上限只是防止一个上报荒谬清单的节点灌满共享冷却存储。
const maxSpreadModels = 32

// record applies one dispatch outcome against c (breakerRegistry.record)
// and follows a rate_limited answer up in afterRateLimited.
//
// record 把一次派发结果作用于 c（breakerRegistry.record），并在答复 rate_limited 时由
// afterRateLimited 跟进。
func (s *Scheduler) record(c Candidate, err error) {
	now := s.clock.Now()
	s.breakers.record(c, err, now)
	if retryAfter, ok := rateLimitedError(err); ok {
		s.afterRateLimited(c, retryAfter, now)
	}
}

// afterRateLimited counts a CLI account's refusal by the quota tier its
// snapshot gave the candidate at that moment, and sets aside the account's
// other models that the refusing window covers.
//
// afterRateLimited 按快照此刻给该候选的额度档位统计一次 CLI 账户的拒绝，并搁置拒绝它的
// 窗口所覆盖的账户其他模型。
func (s *Scheduler) afterRateLimited(c Candidate, retryAfter time.Duration, now time.Time) {
	if c.Kind != runtime.KindClaude && c.Kind != runtime.KindCodex {
		return
	}
	node, ok := s.server.SchedNode(c.NodeID)
	if !ok {
		s.metrics.QuotaRateLimited(quotaVerdict{})
		return
	}
	s.metrics.QuotaRateLimited(accountQuota(node.CLIUsage, node.CLIUsageAt, c.RuntimeID, c.Kind, c.Model, s.quota, now))
	account, _, ok := freshQuotaAccount(node.CLIUsage, node.CLIUsageAt, c.RuntimeID, c.Kind, now)
	if !ok {
		return
	}
	refusing := refusingWindows(account, c.Kind, c.Model, retryAfter, now)
	if len(refusing) == 0 {
		return
	}
	siblings := accountSiblings(&node, c, refusing)
	if len(siblings) == 0 {
		return
	}
	changed, dropped := s.breakers.spreadLimited(c, siblings)
	if dropped {
		s.logger.Warn("shared cooldown write dropped; the store is not keeping up",
			slog.String("node_id", c.NodeID), slog.String("runtime_id", c.RuntimeID))
	}
	if changed > 0 {
		s.metrics.QuotaCooldownSpread(changed)
		s.logger.Info("account quota window covers other models; setting them aside too",
			slog.String("node_id", c.NodeID), slog.String("runtime_id", c.RuntimeID),
			slog.String("window", refusing[0].Name), slog.Int("models", changed))
	}
}

// refusingWindows names the windows of account that most likely refused a
// request for model. With a RetryAfter, it is the applicable window whose
// reset lies within quotaResetMatch of the moment RetryAfter points at, the
// most used one when several do (Claude's weekly windows may share a reset).
// Without a match it is every applicable window the snapshot already shows
// spent. It returns nothing when neither finds one.
//
// refusingWindows 给出 account 中最可能拒绝了 model 请求的窗口。有 RetryAfter 时，取重置
// 时刻与 RetryAfter 所指时刻相差不超过 quotaResetMatch 的适用窗口，多个窗口都符合时取已用
// 最多的那个（Claude 的几个周窗口可能同时重置）。没有匹配时，取快照已显示用完的全部适用
// 窗口。两者都找不到时返回空。
func refusingWindows(account *cliusagestatus.Account, kind runtime.Kind, model string, retryAfter time.Duration, now time.Time) []*cliusagestatus.Window {
	if retryAfter > 0 {
		at := now.Add(retryAfter)
		var matched *cliusagestatus.Window
		for i := range account.Windows {
			w := &account.Windows[i]
			if !liveWindow(kind, w, model, now) {
				continue
			}
			gap := w.ResetsAt.Sub(at)
			if gap < 0 {
				gap = -gap
			}
			if gap <= quotaResetMatch && (matched == nil || w.UsedPercent > matched.UsedPercent) {
				matched = w
			}
		}
		if matched != nil {
			return []*cliusagestatus.Window{matched}
		}
	}
	var spent []*cliusagestatus.Window
	for i := range account.Windows {
		w := &account.Windows[i]
		if liveWindow(kind, w, model, now) && w.UsedPercent >= 100 {
			spent = append(spent, w)
		}
	}
	return spent
}

// accountSiblings returns c's runtime's other models on node that any of
// windows covers, as candidates, at most maxSpreadModels of them.
//
// accountSiblings 以候选的形式返回 node 上 c 所在运行时中、被 windows 中任一窗口覆盖的其他
// 模型，至多 maxSpreadModels 个。
func accountSiblings(node *tunnelserver.SchedNode, c Candidate, windows []*cliusagestatus.Window) []Candidate {
	var out []Candidate
	for r := range node.Runtimes {
		snap := &node.Runtimes[r]
		if snap.Descriptor.ID != c.RuntimeID {
			continue
		}
		seen := map[string]bool{c.Model: true}
		for i := range snap.Discovery.Models {
			id := snap.Discovery.Models[i].ID
			if seen[id] {
				continue
			}
			seen[id] = true
			for _, w := range windows {
				if w.Applies(c.Kind, id) {
					out = append(out, Candidate{NodeID: c.NodeID, RuntimeID: c.RuntimeID, Kind: c.Kind, Model: id})
					break
				}
			}
			if len(out) >= maxSpreadModels {
				return out
			}
		}
	}
	return out
}

// spreadLimited gives each of others the rate-limit cooldown c holds now,
// unless one already rests at least as long. It reports how many changed and
// whether a shared write was dropped, for the caller to log once it has
// released r.mu.
//
// spreadLimited 把 c 此刻持有的限流冷却赋给 others 中的每一个，已经搁置得至少一样久的
// 除外。它报告有多少个发生了变化、是否有共享写入被丢弃，由调用方在释放 r.mu 之后记录。
func (r *breakerRegistry) spreadLimited(c Candidate, others []Candidate) (changed int, dropped bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cd := r.cooldownLocked(c)
	if cd.Until.IsZero() {
		return 0, false
	}
	for _, o := range others {
		if !r.cooldownLocked(o).Until.Before(cd.Until) {
			continue
		}
		if r.setLimitedLocked(o, cd) {
			dropped = true
		}
		changed++
	}
	return changed, dropped
}
