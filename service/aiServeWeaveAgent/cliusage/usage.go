// Package cliusage holds credential-free CLI subscription quota snapshots.
// Package cliusage 保存不含凭据的 CLI 订阅额度快照。
package cliusage

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

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
type Window struct {
	Name            string    // fixed window identifier / 固定窗口标识
	Bucket          string    // validated Codex metering bucket / 经校验的 Codex 计量桶
	UsedPercent     float64   // reported usage, including a real zero / 上报的已用比例，包括真实零值
	DurationMinutes int64     // zero means the duration was not reported / 零表示未报告窗口时长
	ResetsAt        time.Time // upstream reset timestamp / 上游重置时间
	Expired         bool      // snapshot time has reached the reset boundary / 快照时间已到重置边界
}

// Account is a safe projection of one CLI account's subscription quota.
// Account 是一个 CLI 账户订阅额度的安全投影。
type Account struct {
	Kind      runtime.Kind // locally declared provider / 本地声明的供应方
	Status    string       // pending, ready, unauthorized, unavailable or error / 待查询、可用、未登录、未提供或失败
	Windows   []Window     // bounded upstream quota windows / 有界的上游额度窗口
	CheckedAt time.Time    // time of the latest query attempt / 最近查询时间
	Stale     bool         // cached data is older than two minutes / 缓存数据已超过两分钟
}

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

// Refresh queries the fixed providers concurrently within a shared 15-second deadline.
// Refresh 在共同的 15 秒期限内并发查询固定来源。
func (m *Monitor) Refresh(ctx context.Context) {
	m.mu.Lock()
	if m.refreshing || (!m.lastAttempt.IsZero() && m.clock.Now().Sub(m.lastAttempt) < time.Minute) {
		m.mu.Unlock()
		return
	}
	m.refreshing, m.lastAttempt = true, m.clock.Now()
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.refreshing = false; m.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
