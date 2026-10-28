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
}

// NewMonitor selects at most one provider for each declared runtime of a
// supported local CLI kind.
// NewMonitor 为每个受支持本地 CLI 种类的已声明运行时最多选取一个查询来源。
func NewMonitor(clock runtime.Clock, providers []Provider) *Monitor {
	m := &Monitor{clock: clock}
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
// nobody is looking at the local settings page. Each pass still goes through
// Refresh and so keeps its one-batch-per-minute bound. Run returns at once when
// no provider is declared.
//
// Run 立即刷新一次缓存，此后每隔 interval 刷新一次，直到 ctx 被取消，这样即使没有
// 人在看本地设置页，隧道也有一份最新快照可以上报。每一轮仍经过 Refresh，因此继续
// 受「每分钟一批」的限制。没有声明任何来源时，Run 立即返回。
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
		timer, stop := m.clock.NewTimer(interval)
		select {
		case <-timer:
		case <-ctx.Done():
			stop()
			return
		}
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
			m.mu.Lock()
			for i, current := range m.providers {
				if current.key() == p.key() && current.Source == p.Source {
					m.accounts[i] = account
					break
				}
			}
			m.mu.Unlock()
		})
	}
	wg.Wait()
}
