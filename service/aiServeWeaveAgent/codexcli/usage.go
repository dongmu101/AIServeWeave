package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"AIServeWeave/service/aiServeWeaveAgent/cliusage"
)

// ReadAccountUsage queries the native ChatGPT login through app-server, without inference.
// ReadAccountUsage 通过 app-server 查询原生 ChatGPT 登录的账户额度，不执行推理。
func ReadAccountUsage(ctx context.Context) (cliusage.Account, error) {
	return AccountUsageReader("")(ctx)
}

// AccountUsageReader is ReadAccountUsage for the login under accountDir
// (runtime.Config.AccountDir); an empty accountDir reads the Agent's own.
//
// AccountUsageReader 是针对 accountDir（runtime.Config.AccountDir）下那份登录的
// ReadAccountUsage；accountDir 为空时读取 Agent 自己的登录。
func AccountUsageReader(accountDir string) func(context.Context) (cliusage.Account, error) {
	env := withAccountDir(probeEnv(os.Environ()), accountDir)
	return func(ctx context.Context) (cliusage.Account, error) {
		return readAccountUsage(ctx, "codex", env)
	}
}

func readAccountUsage(ctx context.Context, program string, env []string) (cliusage.Account, error) {
	var account cliusage.Account
	err := withServer(ctx, program, env, func(p *protocol, _ string) error {
		if err := initializeServer(p); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := p.request(3, "account/rateLimits/read", map[string]any{}, &raw); err != nil {
			return err
		}
		var err error
		account, err = parseAccountUsage(raw)
		return err
	})
	if errors.Is(err, ErrAuthentication) {
		err = cliusage.ErrUnauthorized
	}
	return account, err
}

func parseAccountUsage(raw []byte) (cliusage.Account, error) {
	type quotaWindow struct {
		Used    *float64 `json:"usedPercent"`
		Minutes int64    `json:"windowDurationMins"`
		Reset   *int64   `json:"resetsAt"` // null when upstream reports no reset / 上游未报告重置时间时为 null
	}
	type bucket struct {
		Primary   *quotaWindow `json:"primary"`
		Secondary *quotaWindow `json:"secondary"`
	}
	var response struct {
		Legacy  *bucket           `json:"rateLimits"`
		Buckets map[string]bucket `json:"rateLimitsByLimitId"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Buckets) > 16 {
		return cliusage.Account{}, ErrProtocol
	}
	if len(response.Buckets) == 0 && response.Legacy != nil {
		response.Buckets = map[string]bucket{"codex": *response.Legacy}
	}
	keys := make([]string, 0, len(response.Buckets))
	for id := range response.Buckets {
		if id == "" || len(id) > 128 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return cliusage.Account{}, ErrProtocol
		}
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var result cliusage.Account
	for _, id := range keys {
		b := response.Buckets[id]
		for _, entry := range []struct {
			name   string
			window *quotaWindow
		}{{"primary", b.Primary}, {"secondary", b.Secondary}} {
			w := entry.window
			if w == nil {
				continue
			}
			if w.Used == nil || math.IsNaN(*w.Used) || math.IsInf(*w.Used, 0) || *w.Used < 0 || (w.Reset != nil && *w.Reset <= 0) || w.Minutes < 0 {
				return cliusage.Account{}, ErrProtocol
			}
			if w.Reset == nil {
				continue // cannot be aged out, so skip it rather than fail the account / 无法判断何时过期，跳过该窗口而不判整个账户失败
			}
			result.Windows = append(result.Windows, cliusage.Window{Name: entry.name, Bucket: id, UsedPercent: *w.Used, DurationMinutes: w.Minutes, ResetsAt: time.Unix(*w.Reset, 0)})
		}
	}
	if len(result.Windows) == 0 {
		return result, cliusage.ErrUnavailable
	}
	return result, nil
}

// errQuotaExhausted is what a quotaError matches with errors.Is.
// errQuotaExhausted 是 quotaError 用 errors.Is 匹配的哨兵。
var errQuotaExhausted = errors.New("codexcli: account usage limit reached")

// quotaError is a turn refused because the account's quota is spent.
// resetsAt is when the exhausted window comes back, zero when unknown.
//
// quotaError 表示因账户额度用完而被拒绝的回合。resetsAt 是用尽的窗口恢复的时刻，
// 未知时为零值。
type quotaError struct {
	resetsAt time.Time
}

func (e *quotaError) Error() string { return errQuotaExhausted.Error() }
func (e *quotaError) Unwrap() error { return errQuotaExhausted }

// exhaustedReset returns when the account can serve again: the latest reset
// among the windows at or above 100%, since every exhausted window has to come
// back. It is zero when no window is exhausted — a short-term throttle the
// account's windows do not show.
//
// exhaustedReset 返回账户何时能再次服务：所有达到或超过 100% 的窗口里最晚的重置时刻，
// 因为每个用尽的窗口都要恢复才行。没有窗口用尽时（账户窗口反映不出的短期限流）返回零值。
func exhaustedReset(account cliusage.Account) time.Time {
	var latest time.Time
	for _, window := range account.Windows {
		if window.UsedPercent >= 100 && window.ResetsAt.After(latest) {
			latest = window.ResetsAt
		}
	}
	return latest
}
