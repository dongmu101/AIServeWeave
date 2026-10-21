package configui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/common/browserlang"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/cliusage"
)

type cliUsageView struct {
	Text     pageText
	Accounts []cliAccountView
}

type cliAccountView struct {
	Name, StatusKey, CheckedAt string
	Stale                      bool
	Windows                    []cliWindowView
}

type cliWindowView struct {
	NameKey, Bucket, Used, Remaining, Reset string
	Minutes                                 int64
	Expired                                 bool
}

// currentCLIUsage renders only cached, credential-free account fields.
// currentCLIUsage 仅渲染缓存中不含凭据的账户字段。
func currentCLIUsage(monitor *cliusage.Monitor, locale browserlang.Locale) cliUsageView {
	view := cliUsageView{Text: newPageText(locale)}
	if monitor == nil {
		return view
	}
	for _, account := range monitor.Snapshot() {
		entry := cliAccountView{Name: "Codex CLI", StatusKey: "u.pending", Stale: account.Stale}
		if account.Kind == runtime.KindClaude {
			entry.Name = "Claude Code"
		}
		switch account.Status {
		case "ready":
			entry.StatusKey = "u.ready"
		case "unauthorized":
			entry.StatusKey = "u.unauthorized"
		case "unavailable":
			entry.StatusKey = "u.unavailable"
		case "error":
			entry.StatusKey = "u.error"
		}
		if !account.CheckedAt.IsZero() {
			entry.CheckedAt = account.CheckedAt.UTC().Format(time.RFC3339)
		}
		for _, w := range account.Windows {
			name := "u.primary"
			switch w.Name {
			case "secondary":
				name = "u.secondary"
			case "five_hour":
				name = "u.five_hour"
			case "seven_day":
				name = "u.seven_day"
			case "seven_day_sonnet":
				name = "u.sonnet"
			case "seven_day_opus":
				name = "u.opus"
			}
			window := cliWindowView{NameKey: name, Bucket: w.Bucket, Minutes: w.DurationMinutes, Reset: w.ResetsAt.UTC().Format(time.RFC3339), Expired: w.Expired}
			window.Used, window.Remaining = formatQuotaPercent(w.UsedPercent), formatQuotaPercent(max(0, 100-w.UsedPercent))
			if window.Expired {
				window.Used, window.Remaining = "—", "—"
			}
			entry.Windows = append(entry.Windows, window)
		}
		view.Accounts = append(view.Accounts, entry)
	}
	return view
}

// formatQuotaPercent rounds display values to two decimals without binary float tails.
// formatQuotaPercent 将显示值保留至两位小数，避免二进制浮点尾差。
func formatQuotaPercent(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".") + "%"
}

// newUsageHandler refreshes quotas without accepting commands, paths or credentials.
// newUsageHandler 刷新额度，不接受命令、路径或凭据参数。
func newUsageHandler(monitor *cliusage.Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		locale := requestLocale(r)
		w.Header().Set("Content-Language", string(locale))
		w.Header().Set("Vary", "Cookie, Accept-Language")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			return
		}
		if monitor != nil {
			monitor.Refresh(r.Context())
		}
		_ = formTemplate.ExecuteTemplate(w, "cli-usage", currentCLIUsage(monitor, locale))
	}
}
