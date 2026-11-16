package model

import "time"

// Price catalog audit actions, recorded under PlatformScope with the entry
// name as the target. A sync records one summary; each automatically applied
// price change, approval and rejection records its own entry. See
// .scratch/token-pricing/spec.md「价目表」.
//
// 价目表审计动作，记在 PlatformScope 下，以条目名称作为目标。一次同步记一条摘要；每个
// 自动生效的改价、每次批准与拒绝各记一条。见 .scratch/token-pricing/spec.md「价目表」。
const (
	ActionBillingCatalogSync      = "billing.catalog.sync"
	ActionBillingCatalogAutoApply = "billing.catalog.auto_apply"
	ActionBillingCatalogApprove   = "billing.catalog.approve"
	ActionBillingCatalogReject    = "billing.catalog.reject"
)

// CatalogModel is one price catalog entry: an upstream model name, its
// status (pricecatalog.Status*), the hash of its prices in effect, the hash
// of an upstream change an operator rejected, and the upstream entry as last
// applied. Its prices are CatalogPrice rows.
//
// CatalogModel 是一个价目表条目：上游模型名、状态（pricecatalog.Status*）、生效中单价的
// 哈希、运维拒绝过的上游变动的哈希，以及最近一次采用的上游原始条目。其单价是若干
// CatalogPrice 行。
type CatalogModel struct {
	Model        string    `gorm:"primaryKey;size:255"`
	Provider     string    `gorm:"size:64;not null"`
	Mode         string    `gorm:"size:32;not null"`
	Status       string    `gorm:"size:24;not null"`
	PricesHash   string    `gorm:"size:64;not null"`
	RejectedHash string    `gorm:"size:64;not null"`
	RawJSON      string    `gorm:"column:raw_json;not null"`
	CreatedAt    time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (CatalogModel) TableName() string { return "catalog_models" }

// CatalogPrice is one official unit price, in effect from ValidFrom until
// ValidTo (nil while it is still in effect), so the price at any past moment
// can be read back. A change closes the old row and inserts a new one; rows
// are never updated otherwise.
//
// CatalogPrice 是一条官方单价，自 ValidFrom 起生效直到 ValidTo（仍生效时为 nil），因此
// 可以读回任一过去时刻的单价。改价时关闭旧行、插入新行；除此之外从不更新行。
type CatalogPrice struct {
	ID               int64      `gorm:"primaryKey;autoIncrement"`
	Model            string     `gorm:"size:255;not null;index:idx_catalog_prices_model_from,priority:1"`
	Meter            string     `gorm:"size:64;not null"`
	Variant          string     `gorm:"size:32;not null"`
	AboveInputTokens int64      `gorm:"not null"`
	PriceMicroPerM   int64      `gorm:"column:price_micro_per_m;not null"`
	Derived          bool       `gorm:"not null"`
	ValidFrom        time.Time  `gorm:"not null;index:idx_catalog_prices_model_from,priority:2"`
	ValidTo          *time.Time `gorm:"index:idx_catalog_prices_valid_to"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (CatalogPrice) TableName() string { return "catalog_prices" }

// CatalogPending is an upstream change waiting for an operator: a price
// change (ProposedJSON holds the proposed pricecatalog.Entry) or a removal.
// MaxChangeBP is the largest relative change in basis points, or -1 when it
// is unbounded or does not apply.
//
// CatalogPending 是等待运维审核的上游变动：改价（ProposedJSON 保存拟采用的
// pricecatalog.Entry）或删除。MaxChangeBP 是最大的相对变动（基点）；无界或不适用时为 -1。
type CatalogPending struct {
	Model        string    `gorm:"primaryKey;size:255"`
	Kind         string    `gorm:"size:16;not null"`
	ProposedJSON string    `gorm:"column:proposed_json;not null"`
	PricesHash   string    `gorm:"size:64;not null"`
	MaxChangeBP  int64     `gorm:"column:max_change_bp;not null"`
	DetectedAt   time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (CatalogPending) TableName() string { return "catalog_pending" }

// CatalogSyncStateID is the id of the one catalog_sync_state row.
//
// CatalogSyncStateID 是 catalog_sync_state 唯一一行的 id。
const CatalogSyncStateID = 1

// CatalogSyncState is the one row describing the catalog as a whole: the
// last source and content applied (ContentHash), the content of the built-in
// snapshot last applied (EmbeddedHash), how many entries that content had,
// the catalog version (incremented whenever a price takes effect or stops),
// and the last fetch failure. Every catalog write locks this row first, so
// replicas syncing at once serialize on it.
//
// CatalogSyncState 是描述整个价目表的唯一一行：最近一次采用的来源与内容
// （ContentHash）、最近一次采用的内置快照内容（EmbeddedHash）、该内容的条目数、价目表
// 版本号（每当有单价生效或失效就加一），以及最近一次拉取失败。每次写价目表都先锁定这
// 一行，因此同时同步的各副本会在它上面串行。
type CatalogSyncState struct {
	ID           int64  `gorm:"primaryKey;autoIncrement:false"`
	Source       string `gorm:"size:512;not null"`
	ContentHash  string `gorm:"size:64;not null"`
	EmbeddedHash string `gorm:"size:64;not null"`
	EntryCount   int64  `gorm:"not null"`
	Version      int64  `gorm:"not null"`
	SyncedAt     *time.Time
	CheckedAt    *time.Time
	LastError    string `gorm:"size:512;not null"`
	LastErrorAt  *time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (CatalogSyncState) TableName() string { return "catalog_sync_state" }
