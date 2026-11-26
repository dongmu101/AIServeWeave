// Package cliusage holds credential-free CLI subscription quota snapshots.
// Package cliusage 保存不含凭据的 CLI 订阅额度快照。
package cliusage

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
)

// ErrUnauthorized means the CLI's native subscription login is unavailable.
// ErrUnauthorized 表示 CLI 原生订阅登录不可用。
var ErrUnauthorized = errors.New("CLI subscription login required")

// ErrUnavailable means the CLI did not provide subscription quota data.
// ErrUnavailable 表示 CLI 未提供订阅额度数据。
var ErrUnavailable = errors.New("CLI subscription usage unavailable")

// Window contains one upstream-reported quota window, never a token estimate.
// Window 保存一个上游报告的额度窗口，不包含 token 推算值。
type Window = cliusagestatus.Window

// Account is a safe projection of one CLI account's subscription quota.
// Account 是一个 CLI 账户订阅额度的安全投影。
type Account = cliusagestatus.Account

// Provider reads one locally declared CLI without submitting an inference turn.
// Provider 查询一个本地声明的 CLI，不提交推理回合。
type Provider struct {
	RuntimeID string                                 // the declared runtime whose login this reads / 所读取登录归属的已声明运行时
	Kind      runtime.Kind                           // identifies Codex or Claude Code / 标识 Codex 或 Claude Code
	Read      func(context.Context) (Account, error) // must honor cancellation / 必须响应取消
	// Source names where Read reads from, such as the runtime's account
	// directory. Replace keeps a cached account only while it is unchanged,
	// so a runtime moved to another login never shows the old one's quota.
	// Source 指明 Read 从哪里读取，例如运行时的账户目录。只有它不变时 Replace 才保留缓存
	// 账户，因此换到另一份登录的运行时不会显示旧登录的额度。
	Source string
}

// key identifies the account a provider reads: one per runtime, so two
// runtimes of the same kind with their own logins are two accounts.
// key 标识一个来源读取的账户：每个运行时一个，因此同一种类、各有登录的两个运行时是两个账户。
func (p Provider) key() string { return string(p.Kind) + "\x00" + p.RuntimeID }

// Monitor bounds quota queries to one batch per minute and one process per CLI.
// Monitor 将额度查询限制为每分钟一批，每个 CLI 最多一个查询进程。
type Monitor struct {
	clock       runtime.Clock
	providers   []Provider
	mu          sync.Mutex
	refreshing  bool
	running     bool // a Run loop is active / 有一个 Run 循环正在运行
	lastAttempt time.Time
	accounts    []Account
	// nudges wakes the Run loop early; see Nudge. One pending nudge is
	// enough, so it is buffered by one and a full buffer drops the send.
	// nudges 提前唤醒 Run 循环，见 Nudge。一个待处理的唤醒就够了，因此缓冲为一，
	// 缓冲已满时直接丢弃。
	nudges chan struct{}
	// holds are the refusals Refused recorded (guard.go). Guarded by mu.
	// holds 是 Refused 记录的拒绝（guard.go）。受 mu 保护。
	holds []quotaHold
}

// HotPercent is the used percentage at which Run refreshes every
// HotInterval instead of the interval it was given: near a limit the
// Gateway's ordering needs current figures, and an account can go from here
// to spent within one slow interval. A usage query runs no inference.
//
// HotPercent 是 Run 改为每 HotInterval 刷新一次（而不是调用方给定的间隔）的已用百分比：
// 接近上限时 Gateway 的排序需要最新数字，而账户从这里到用完可能用不了一个慢间隔。用量
// 查询不执行推理。
const HotPercent = 80

// HotInterval is Run's interval while an account is at HotPercent or above.
// It equals the one-batch-per-minute floor, the fastest Refresh will go.
//
// HotInterval 是有账户达到 HotPercent 时 Run 的间隔。它等于「每分钟一批」的下限，也就是
// Refresh 能达到的最快频率。
const HotInterval = time.Minute

// NewMonitor selects at most one provider for each declared runtime of a
// supported local CLI kind.
// NewMonitor 为每个受支持本地 CLI 种类的已声明运行时最多选取一个查询来源。
func NewMonitor(clock runtime.Clock, providers []Provider) *Monitor {
	m := &Monitor{clock: clock, nudges: make(chan struct{}, 1)}
	m.providers, m.accounts = selectProviders(providers, nil, nil)
	return m
}

// Replace swaps in a new provider set, as an Agent does when its declared
// runtimes are reloaded. An account whose runtime is still declared keeps its
// cached snapshot; a new one starts pending until the next refresh. It
// reports whether the monitor now has providers but no Run loop, which is
// when a caller must start Run, since Run returns at once without providers.
// A loop that is already running keeps going through an empty spell and picks
// the new providers up on its next pass.
//
// Replace 换入一组新的查询来源，Agent 重新加载已声明运行时时会这样做。运行时仍然声明着
// 的账户保留缓存快照；新增的账户在下次刷新前处于待查询状态。它报告监视器现在是否有来源
// 却没有 Run 循环——此时调用方必须启动 Run，因为没有来源时 Run 会立即返回。已在运行的
// 循环会熬过来源为空的阶段，并在下一轮拿到新的来源。
func (m *Monitor) Replace(providers []Provider) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers, m.accounts = selectProviders(providers, m.providers, m.accounts)
	m.pruneHoldsLocked()
	return len(m.providers) > 0 && !m.running
}

// selectProviders keeps the supported, distinct providers and pairs each with
// the cached account of the same previous provider reading the same Source,
// or a pending one. previous and accounts are index-aligned, as Monitor keeps
// them.
// selectProviders 保留受支持且互不重复的来源，并为每个来源配上 previous 中同一来源、
// 同一 Source 的缓存账户，没有则为待查询账户。previous 与 accounts 按下标对齐，
// 与 Monitor 中的存放方式一致。
func selectProviders(providers, previous []Provider, accounts []Account) ([]Provider, []Account) {
	type cachedAccount struct {
		source  string
		account Account
	}
	cached := make(map[string]cachedAccount, len(previous))
	for i, p := range previous {
		if i < len(accounts) {
			cached[p.key()] = cachedAccount{source: p.Source, account: accounts[i]}
		}
	}
	var kept []Provider
	var selected []Account
	seen := make(map[string]bool)
	for _, p := range providers {
		if (p.Kind != runtime.KindCodex && p.Kind != runtime.KindClaude) || p.Read == nil || seen[p.key()] {
			continue
		}
		seen[p.key()] = true
		kept = append(kept, p)
		account := Account{RuntimeID: p.RuntimeID, Kind: p.Kind, Status: "pending"}
		if previous, ok := cached[p.key()]; ok && previous.source == p.Source {
			account = previous.account
		}
		selected = append(selected, account)
	}
	return kept, selected
}

// Snapshot returns independent cached copies without starting CLI processes.
// Snapshot 返回独立缓存副本，不启动 CLI 进程。
func (m *Monitor) Snapshot() []Account {
	m.mu.Lock()
	defer m.mu.Unlock()
	accounts := slices.Clone(m.accounts)
	for i := range accounts {
		accounts[i].Windows = slices.Clone(accounts[i].Windows)
		for j := range accounts[i].Windows {
			accounts[i].Windows[j].Expired = !m.clock.Now().Before(accounts[i].Windows[j].ResetsAt)
		}
		accounts[i].Stale = !accounts[i].CheckedAt.IsZero() && m.clock.Now().Sub(accounts[i].CheckedAt) > 2*time.Minute
	}
	return accounts
}

// Run refreshes the cache once immediately and then every interval until ctx
// is cancelled, so the tunnel has a current snapshot to report even when
// nobody is looking at the local settings page. While an account is at
// HotPercent the interval drops to HotInterval, and a Nudge refreshes early.
// Each pass still goes through Refresh and so keeps its one-batch-per-minute
// bound. Run returns at once when no provider is declared.
//
// Run 立即刷新一次缓存，此后每隔 interval 刷新一次，直到 ctx 被取消，这样即使没有
// 人在看本地设置页，隧道也有一份最新快照可以上报。有账户达到 HotPercent 时间隔缩短为
// HotInterval，Nudge 会让刷新提前。每一轮仍经过 Refresh，因此继续受「每分钟一批」的
// 限制。没有声明任何来源时，Run 立即返回。
//
// At most one Run loop is active at a time; a second call while one runs
// returns at once.
// 同一时刻最多只有一个 Run 循环；已有循环在运行时，再次调用会立即返回。
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	m.mu.Lock()
	if len(m.providers) == 0 || m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
	for {
		m.Refresh(ctx)
		if !m.wait(ctx, m.nextInterval(interval)) {
			return
		}
	}
}

// Nudge asks the Run loop to refresh now rather than at its next interval,
// as soon as the one-batch-per-minute floor allows. A runtime calls it when
// its CLI says the account is near or at a limit, so the snapshot the tunnel
// reports catches up within about a minute instead of a whole interval. It
// never blocks, and does nothing without a Run loop.
//
// Nudge 请求 Run 循环立即刷新，而不是等到下一个间隔；仍受「每分钟一批」的下限约束。运行时
// 在 CLI 表示账户接近或已到上限时调用它，让隧道上报的快照在一分钟左右赶上，而不是等一整个
// 间隔。它从不阻塞；没有 Run 循环时什么也不做。
func (m *Monitor) Nudge() {
	select {
	case m.nudges <- struct{}{}:
	default:
	}
}

// nextInterval is interval, or HotInterval when that is shorter and some
// ready account has a window that has not reset at HotPercent or above but
// below 100%. An account with a spent window covering every model does not
// count: it can serve nothing until that reset, so its figures cannot move,
// and asking the upstream every minute would only add load (the reset is
// already known, and Hold lets go at it).
//
// nextInterval 返回 interval；若 HotInterval 更短，且有 ready 账户的某个尚未重置的窗口
// 达到 HotPercent 但低于 100%，则返回 HotInterval。有覆盖所有模型的窗口已用完的账户不算：
// 它在重置前什么也服务不了，数字不会变化，每分钟问一次上游只会增加负担（重置时刻已经
// 知道，Hold 届时会放行）。
func (m *Monitor) nextInterval(interval time.Duration) time.Duration {
	if interval <= HotInterval {
		return interval
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	for i := range m.accounts {
		account := &m.accounts[i]
		if account.Status != "ready" {
			continue
		}
		if _, frozen := spentUntil(account, "", now); frozen {
			continue
		}
		for _, w := range account.Windows {
			if w.UsedPercent >= HotPercent && w.UsedPercent < 100 && now.Before(w.ResetsAt) {
				return HotInterval
			}
		}
	}
	return interval
}

// wait blocks for d, or until a Nudge and then the rest of the per-minute
// floor, and reports false once ctx ends.
//
// wait 阻塞 d；或者等到一次 Nudge，再等完每分钟下限的剩余部分。ctx 结束时返回 false。
func (m *Monitor) wait(ctx context.Context, d time.Duration) bool {
	timer, stop := m.clock.NewTimer(d)
	select {
	case <-timer:
		return true
	case <-m.nudges:
		stop()
	case <-ctx.Done():
		stop()
		return false
	}
	var rest time.Duration
	m.mu.Lock()
	if !m.lastAttempt.IsZero() {
		rest = time.Minute - m.clock.Now().Sub(m.lastAttempt)
	}
	m.mu.Unlock()
	if rest <= 0 {
		return true
	}
	timer, stop = m.clock.NewTimer(rest)
	select {
	case <-timer:
		return true
	case <-ctx.Done():
		stop()
		return false
	}
}

// Refresh queries the fixed providers concurrently within a shared 15-second
// deadline. The batch ignores ctx cancellation: its result is the cache every
// viewer shares, so a page reload that abandons one request must not record a
// failure that blocks retries for the next minute.
//
// Refresh 在共同的 15 秒期限内并发查询固定来源。该批次不随 ctx 取消：结果是所有
// 查看者共用的缓存，单个请求因页面刷新而中断时，不能写入一次失败并挡住此后一分钟的重试。
func (m *Monitor) Refresh(ctx context.Context) {
	m.mu.Lock()
	if m.refreshing || (!m.lastAttempt.IsZero() && m.clock.Now().Sub(m.lastAttempt) < time.Minute) {
		m.mu.Unlock()
		return
	}
	m.refreshing, m.lastAttempt = true, m.clock.Now()
	providers := m.providers
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.refreshing = false; m.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Go(func() {
			account, err := p.Read(ctx)
			account.RuntimeID, account.Kind, account.CheckedAt, account.Stale = p.RuntimeID, p.Kind, m.clock.Now(), false
			account.Status = "ready"
			if err != nil || len(account.Windows) == 0 {
				account.Status, account.Windows = "error", nil
				switch {
				case errors.Is(err, ErrUnauthorized):
					account.Status = "unauthorized"
				case err == nil, errors.Is(err, ErrUnavailable):
					account.Status = "unavailable"
				}
			}
			// Matched by key and Source, not index: Replace may have changed
			// the set while this query ran, and the result of a runtime since
			// dropped or moved to another login is discarded.
			// 按 key 与 Source 而不是下标匹配：查询期间 Replace 可能已改变来源集合，已移除
			// 或已换到另一份登录的运行时的结果直接丢弃。
			//
			// A failed query keeps the figures last read, with their own
			// CheckedAt, so they age instead of vanishing: Stale here, and
			// too old for the Gateway to rank by after its snapshot age
			// bound. An account without figures counts as having quota to
			// spare, and one timed-out query must not turn a nearly spent
			// account into that. A definite answer (unauthorized,
			// unavailable) still replaces them.
			// 查询失败时保留上次读到的数字及其 CheckedAt，让它们自然变旧而不是消失：本地显示
			// 为 Stale，超过 Gateway 的快照年龄上限后不再参与排序。没有数字的账户被当作额度
			// 充足，一次查询超时不能把快用完的账户变成那样。明确的答复（未登录、未提供）仍会
			// 替换它们。
			m.mu.Lock()
			for i, current := range m.providers {
				if current.key() == p.key() && current.Source == p.Source {
					if account.Status != "error" || m.accounts[i].Status != "ready" {
						m.accounts[i] = account
					}
					break
				}
			}
			m.mu.Unlock()
		})
	}
	wg.Wait()
}
