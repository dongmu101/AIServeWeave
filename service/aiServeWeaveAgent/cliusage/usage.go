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
	Kind runtime.Kind                           // identifies Codex or Claude Code / 标识 Codex 或 Claude Code
	Read func(context.Context) (Account, error) // must honor cancellation / 必须响应取消
}

// Monitor bounds quota queries to one batch per minute and one process per CLI.
// Monitor 将额度查询限制为每分钟一批，每个 CLI 最多一个查询进程。
type Monitor struct {
	clock       runtime.Clock
	providers   []Provider
	mu          sync.Mutex
	refreshing  bool
	lastAttempt time.Time
	accounts    []Account
}

// NewMonitor selects at most one provider for each supported local CLI kind.
// NewMonitor 对每种支持的本地 CLI 最多选取一个查询来源。
func NewMonitor(clock runtime.Clock, providers []Provider) *Monitor {
	m := &Monitor{clock: clock}
	seen := make(map[runtime.Kind]bool)
	for _, p := range providers {
		if (p.Kind != runtime.KindCodex && p.Kind != runtime.KindClaude) || p.Read == nil || seen[p.Kind] {
			continue
		}
		seen[p.Kind] = true
		m.providers = append(m.providers, p)
		m.accounts = append(m.accounts, Account{Kind: p.Kind, Status: "pending"})
	}
	return m
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
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	if len(m.providers) == 0 {
		return
	}
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
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.refreshing = false; m.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, p := range m.providers {
		wg.Go(func() {
			account, err := p.Read(ctx)
			account.Kind, account.CheckedAt, account.Stale = p.Kind, m.clock.Now(), false
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
			m.mu.Lock()
			m.accounts[i] = account
			m.mu.Unlock()
		})
	}
	wg.Wait()
}
