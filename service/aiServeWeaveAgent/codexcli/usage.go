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
	return readAccountUsage(ctx, "codex", probeEnv(os.Environ()))
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
