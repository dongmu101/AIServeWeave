package tunnelwire

import (
	"sort"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
)

// CLIUsageReportToProto encodes an Agent's CLI subscription quota snapshot.
// Expired and Stale are not sent: they depend on the moment of reading, so the
// receiver derives them from the reset and check timestamps, and an unchanged
// quota encodes identically from one poll to the next.
//
// CLIUsageReportToProto 编码 Agent 的 CLI 订阅额度快照。Expired 与 Stale 不外送：
// 它们取决于读取的时刻，由接收方依据重置与查询时间戳自行推出，因此额度没变化时，
// 相邻两次轮询的编码完全相同。
func CLIUsageReportToProto(accounts []cliusagestatus.Account) *tunnelv1.CLIUsageReport {
	out := make([]*tunnelv1.CLIUsageAccount, len(accounts))
	for i, account := range accounts {
		windows := make([]*tunnelv1.CLIUsageWindow, len(account.Windows))
		for j, window := range account.Windows {
			windows[j] = &tunnelv1.CLIUsageWindow{
				Name:            window.Name,
				Bucket:          window.Bucket,
				UsedPercent:     window.UsedPercent,
				DurationMinutes: window.DurationMinutes,
				ResetsUnixMs:    unixMilliOrZero(window.ResetsAt),
			}
		}
		out[i] = &tunnelv1.CLIUsageAccount{
			Kind:          string(account.Kind),
			Status:        account.Status,
			Windows:       windows,
			CheckedUnixMs: unixMilliOrZero(account.CheckedAt),
		}
	}
	return &tunnelv1.CLIUsageReport{Accounts: out}
}

// CLIUsageReportFromProto restores the reported accounts, sorted by kind so a
// Gateway-side consumer never depends on the Agent's send order.
//
// CLIUsageReportFromProto 还原上报的账户，按 kind 排序，让 Gateway 一侧的使用者
// 不依赖 Agent 的发送顺序。
func CLIUsageReportFromProto(pb *tunnelv1.CLIUsageReport) []cliusagestatus.Account {
	accounts := pb.GetAccounts()
	out := make([]cliusagestatus.Account, len(accounts))
	for i, account := range accounts {
		windows := make([]cliusagestatus.Window, len(account.GetWindows()))
		for j, window := range account.GetWindows() {
			windows[j] = cliusagestatus.Window{
				Name:            window.GetName(),
				Bucket:          window.GetBucket(),
				UsedPercent:     window.GetUsedPercent(),
				DurationMinutes: window.GetDurationMinutes(),
				ResetsAt:        timeFromUnixMilli(window.GetResetsUnixMs()),
			}
		}
		out[i] = cliusagestatus.Account{
			Kind:      runtime.Kind(account.GetKind()),
			Status:    account.GetStatus(),
			Windows:   windows,
			CheckedAt: timeFromUnixMilli(account.GetCheckedUnixMs()),
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// unixMilliOrZero maps the zero time to 0, since time.Time{}.UnixMilli() is a
// large negative number that would not round-trip as "never".
//
// unixMilliOrZero 把零值时间映射为 0，因为 time.Time{}.UnixMilli() 是一个很大的
// 负数，无法还原成「从未」。
func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func timeFromUnixMilli(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
