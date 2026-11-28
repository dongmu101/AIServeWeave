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
	TenantID         string    `gorm:"size:40;not null"`
	Model            string    `gorm:"size:128;not null"`
	Endpoint         string    `gorm:"size:24;not null"`
	PromptTokens     int64     `gorm:"not null"`
	CompletionTokens int64     `gorm:"not null"`
	TotalTokens      int64     `gorm:"not null"`
	CreatedAt        time.Time `gorm:"primaryKey;not null"`
	// KeyID is the API key that made the request, as the Gateway reported
	// it. UserID is that key's creator, resolved when the record is written
	// so the attribution survives the key's or the user's later deletion.
	// Both are empty for records written before they existed.
	//
	// KeyID 是 Gateway 上报的、发起请求的 API key。UserID 是该 key 的创建者，在写入
	// 记录时解析，使归属在 key 或用户之后被删除时依然保留。两者出现之前写入的记录为空。
	KeyID  string `gorm:"size:40;not null;default:''"`
	UserID string `gorm:"size:40;not null;default:''"`
	// NodeID is the node that served the request, as the Gateway reported
	// it, or empty. It decides whether the record is charged (a tenant's own
	// node, or a platform node made free, is not) and is not stored. Older
	// Gateways report only tenant-owned nodes.
	//
	// NodeID 是 Gateway 上报的、服务该请求的节点，没有则为空。它决定记录是否扣费（商户
	// 自有节点或被设为免费的平台节点不扣费），不被存储。旧版 Gateway 只上报商户自有节点。
	NodeID string `gorm:"-"`
	// CacheReadTokens and CacheWriteTokens are the prompt-cache reads and
	// writes (every TTL), taken out of Details so summaries can add them up;
	// both are part of PromptTokens. Other meters live only in Details.
	//
	// CacheReadTokens 与 CacheWriteTokens 是提示缓存的读取与写入（各种 TTL 合计），从
	// Details 中取出，使汇总可以求和；两者都是 PromptTokens 的一部分。其他计量项只存在
	// Details 中。
	CacheReadTokens  int64 `gorm:"not null;default:0"`
	CacheWriteTokens int64 `gorm:"not null;default:0"`
	// CostMicro, BaseCostMicro, RatePPM, PriceSource and Charged are what
	// charging decided for the record; they stay zero and empty until
	// pricing writes them (.scratch/token-pricing, item 04), and for records
	// written with billing off. Charged is nil for those records.
	//
	// CostMicro、BaseCostMicro、RatePPM、PriceSource 与 Charged 是扣费为这条记录所作
	// 的决定；在计价写入它们之前（.scratch/token-pricing 第 04 项），以及计费关闭时写入
	// 的记录，它们保持为零或空，Charged 为 nil。
	CostMicro     int64  `gorm:"not null;default:0"`
	BaseCostMicro int64  `gorm:"not null;default:0"`
	RatePPM       int64  `gorm:"column:rate_ppm;not null;default:0"`
	PriceSource   string `gorm:"size:16;not null;default:''"`
	Charged       *bool
	// Details is the record's meter breakdown as UsageDetails JSON, or nil
	// when the Gateway reported none.
	//
	// Details 是这条记录按计量项的细分（UsageDetails 的 JSON），Gateway 没有上报时为 nil。
	Details *string `gorm:"type:text"`
	// ReportedDetails and ReportedAttributes are the validated breakdown and
	// request facts the Gateway sent, kept for pricing; they are stored only
	// inside Details.
	//
	// ReportedDetails 与 ReportedAttributes 是 Gateway 发来、已经校验过的细分与请求
	// 属性，留给计价使用；它们只以 Details 的形式存储。
	ReportedDetails    map[string]int64  `gorm:"-"`
	ReportedAttributes map[string]string `gorm:"-"`
}

// Price sources a charged usage record names in PriceSource: where the
// prices it was charged at came from. See .scratch/token-pricing/spec.md
// 「定价与价格方案」. An empty PriceSource is a record written before pricing
// existed or with billing off.
//
// 已扣费的用量记录在 PriceSource 中注明的价格来源：扣费所用的单价从哪里来。见
// .scratch/token-pricing/spec.md「定价与价格方案」。PriceSource 为空表示记录写于
// 计价出现之前，或写于计费关闭时。
const (
	PriceSourceOverride  = "override"
	PriceSourceCatalog   = "catalog"
	PriceSourceDefault   = "default"
	PriceSourcePlanFixed = "plan_fixed"
	PriceSourceNone      = "none"
	// PriceSourceLegacy marks a record whose cost was backfilled from
	// usage_charges: charged under the per-model prices before this pricing.
	// PriceSourceLegacy 标记从 usage_charges 回填费用的记录：按本计价之前的按模型单价
	// 扣费。
	PriceSourceLegacy = "legacy"
)

// UsageDetailsVersion is the format version UsageDetails is written with.
//
// UsageDetailsVersion 是写入 UsageDetails 时所用的格式版本。
const UsageDetailsVersion = 1

// UsageDetails is the JSON stored in usage_records.details: the meter tree
// of the request and, once charged, how it was priced. Every field but V
// and Meters is optional, so the format can grow without a migration; a
// reader must ignore fields it does not know. See
// .scratch/token-pricing/spec.md「单笔计价」.
//
// UsageDetails 是存在 usage_records.details 中的 JSON：请求的计量项树，以及扣费后的
// 计价过程。除 V 与 Meters 外的字段都是可选的，使格式无需迁移就能扩展；读取方必须忽略
// 不认识的字段。见 .scratch/token-pricing/spec.md「单笔计价」。
type UsageDetails struct {
	V          int               `json:"v"`
	Meters     []UsageMeterLine  `json:"m"`
	Attributes map[string]string `json:"attrs,omitempty"`
	// Adjusted is set when a parent meter was raised to the sum of its
	// children because the reported counts were inconsistent.
	// Adjusted 在因上报计数不一致、父项被提高到子项之和时设置。
	Adjusted bool `json:"adj,omitempty"`
	// The fields below say how the record was priced; they are absent on a
	// record written with billing off.
	// 以下字段说明记录是如何计价的；计费关闭时写入的记录没有它们。
	//
	// Revision is the pricing configuration revision, Plan the tenant's
	// price plan, Source the price source (PriceSource*), Ref the catalog
	// entry when Source is catalog (shown to operators only), Tier the tier
	// the request fell in and Rate the combined rate in parts per million.
	// Revision 是定价配置版本，Plan 是租户的价格方案，Source 是价格来源
	// （PriceSource*），Ref 是 Source 为 catalog 时的价目表条目（只向运维显示），
	// Tier 是请求所落入的档位，Rate 是以百万分之一为单位的总倍率。
	Revision int64  `json:"rev,omitempty"`
	Plan     string `json:"plan,omitempty"`
	Source   string `json:"src,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Tier     int64  `json:"tier,omitempty"`
	Rate     int64  `json:"rate,omitempty"`
	// OwnNode is set when the tenant's own node served the request: it is
	// priced as usual but not charged.
	// OwnNode 在请求由租户自己的节点服务时设置：照常计价，但不扣费。
	OwnNode bool `json:"own,omitempty"`
	// FreeNode is set when a platform node an operator made free served the
	// request: it is priced as usual but not charged.
	// FreeNode 在请求由运维设为免费的平台节点服务时设置：照常计价，但不扣费。
	FreeNode bool `json:"free,omitempty"`
	// OwnKey is set when the tenant's own provider key served the request
	// (attribute credential=tenant): it is priced as usual but not charged.
	// OwnKey 在请求用租户自己的供应商 Key 完成时设置（属性 credential=tenant）：照常计价，
	// 但不扣费。
	OwnKey bool `json:"own_key,omitempty"`
	// Unpriced is set when some billed quantity found no price.
	// Unpriced 在有计费数量找不到单价时设置。
	Unpriced bool `json:"unpriced,omitempty"`
}

// UsageMeterLine is one meter of a UsageDetails: Q is the quantity after
// the parent-not-below-children correction. The pricing fields are filled
// when the record is charged.
//
// UsageMeterLine 是 UsageDetails 中的一个计量项：Q 是经过「父项不小于子项之和」校正后
// 的数量。计价相关的字段在扣费时填写。
type UsageMeterLine struct {
	K string `json:"k"`
	Q int64  `json:"q"`
	// X is Q less the meter's children, P the base unit price applied to it
	// (micro-credits per million units) and C its cost at the record's
	// rate, rounded up on its own.
	// X 是 Q 减去子项之后的数量，P 是用于它的基础单价（micro 点 / 百万单位），C 是它按
	// 记录的倍率计算、单独向上取整的费用。
	X int64 `json:"x,omitempty"`
	P int64 `json:"p,omitempty"`
	C int64 `json:"c,omitempty"`
}

// TableName returns "usage_records", the table UsageRecord persists to.
//
// TableName 返回 "usage_records"，即 UsageRecord 持久化到的表名。
func (UsageRecord) TableName() string { return "usage_records" }
