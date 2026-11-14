// usagerecord.go holds the persisted per-tenant/model token usage ledger,
// per STATUS.md's P2 "按租户/模型的持久化用量账本" — the durable record a
// settlement process reads, replacing the Prometheus gateway_tokens_total
// counter that resets on every Gateway replica restart and carries no model
// dimension.
//
// usagerecord.go 保存按租户/模型的持久化 token 用量账本，对应 STATUS.md
// 的 P2「按租户/模型的持久化用量账本」——供结算流程读取的持久化记录，取代
// 每次 Gateway 副本重启即归零、也不带模型维度的 Prometheus
// gateway_tokens_total 计数器。
package model

import "time"

// UsageRecord is one billable request's token usage, as a Gateway replica
// reported it. ID is the request-correlation id common/reqid minted for it
// on the Gateway side, not a surrogate key — the same choice RequestLog.ID
// already makes, and for the same reason: the id is already globally unique,
// so a duplicate report (a retried batch push) is naturally rejected — here,
// silently skipped — by the same uniqueness this key already provides. That
// uniqueness is this table's dedup rule; there is no separate deduplication
// pass. The table is range-partitioned by month on CreatedAt, so the primary
// key is (id, created_at): the dedup holds because the Gateway's outbox
// resends a record with the CreatedAt it was first stamped with. Charging
// does not rely on it — usage_charges keeps request_id globally unique.
//
// Model is never raw client free text — see the Gateway's usageledger.go for
// why the value recorded here is always bounded to a modelroute alias.
//
// UsageRecord 是一次可计费请求的 token 用量，由 Gateway 副本上报。ID 是
// Gateway 一侧 common/reqid 为它铸造的请求关联 id，不是代理键——与
// RequestLog.ID 相同的选择，理由也相同：这个 id 本就全局唯一，因此重复上报
// （一次被重试的批量推送）天然会被这同一个唯一性拒绝——这里的表现是被悄悄
// 跳过。这份唯一性就是这张表的去重规则；不存在另一道独立的去重步骤。这张表
// 按 CreatedAt 按月做 RANGE 分区，因此主键是 (id, created_at)：去重依然成立，
// 因为 Gateway 的 outbox 重发记录时沿用其首次生成时的 CreatedAt。扣费不依赖
// 这一点——usage_charges 仍保持 request_id 全局唯一。
//
// Model 从不是客户端的自由文本——为什么这里记录的值总是被约束在一个
// modelroute 别名内，见 Gateway 的 usageledger.go。
type UsageRecord struct {
	ID               string    `gorm:"primaryKey;size:64"`
	TenantID         string    `gorm:"size:32;not null"`
	Model            string    `gorm:"size:128;not null"`
	Endpoint         string    `gorm:"size:24;not null"`
	PromptTokens     int64     `gorm:"not null"`
	CompletionTokens int64     `gorm:"not null"`
	TotalTokens      int64     `gorm:"not null"`
	CreatedAt        time.Time `gorm:"primaryKey;not null"`
	// NodeID is the tenant-owned node that served the request, as the Gateway
	// reported it, or empty. It is used to price the record and not stored.
	//
	// NodeID 是 Gateway 上报的、服务该请求的商户自有节点，没有则为空。它只用于给记录
	// 定价，不被存储。
	NodeID string `gorm:"-"`
}

// TableName returns "usage_records", the table UsageRecord persists to.
//
// TableName 返回 "usage_records"，即 UsageRecord 持久化到的表名。
func (UsageRecord) TableName() string { return "usage_records" }
