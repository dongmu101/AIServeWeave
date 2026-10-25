// Package cliusagestatus is the credential-free shape of a local CLI's
// subscription quota, shared by the Agent that reads it and the tunnel codec
// that carries it to the Gateway. It holds only what the upstream reported —
// percentages, window names and timestamps — never an account name, token or
// credential, and never a token-count estimate.
//
// cliusagestatus 是本地 CLI 订阅额度的不含凭据形状，由读取它的 Agent 与把它送往
// Gateway 的隧道编解码共用。它只保存上游报告的内容——百分比、窗口名与时间戳——
// 绝不含账户名、token 或任何凭据，也不含 token 数量推算值。
package cliusagestatus

import (
	"time"

	"AIServeWeave/common/runtime"
)

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
