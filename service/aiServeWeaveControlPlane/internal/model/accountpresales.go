package model

import "time"

// Account kinds a monthly account or presale names. They are the runtime
// kinds an Agent reports for its Claude Code and Codex adapters.
//
// 包月账号与预售所指的账号类型，即 Agent 为其 Claude Code 与 Codex 适配器上报的
// 运行时类型。
const (
	AccountKindClaude = "claude"
	AccountKindCodex  = "codex"
)

// ValidAccountKind reports whether kind is a sellable account kind.
//
// ValidAccountKind 报告 kind 是否为可售卖的账号类型。
func ValidAccountKind(kind string) bool {
	return kind == AccountKindClaude || kind == AccountKindCodex
}

// AccountPresale sells one month of an account of a kind before any account
// is chosen. A paid presale order waits until an operator assigns it a free
// monthly account of the same kind; the month starts at that assignment.
// Capacity bounds the orders still waiting for an account, pending
// checkouts within AccountCheckoutHold included. Kind never changes after
// creation.
//
// AccountPresale 在尚未选定账号时预售某一类型账号的一个月。已付款的预售订单等待运维
// 为其分配一个同类型的空闲包月账号，一个月自分配时起算。Capacity 限制仍在等待账号的
// 订单数，包括 AccountCheckoutHold 内的待付款结账。Kind 创建后不可更改。
type AccountPresale struct {
	ID            string    `gorm:"primaryKey;size:32"`
	Kind          string    `gorm:"size:16;not null"`
	Name          string    `gorm:"size:128;not null"`
	PriceMinor    int64     `gorm:"not null"`
	Currency      string    `gorm:"size:8;not null"`
	DodoProductID string    `gorm:"size:64;not null"`
	Capacity      int       `gorm:"not null"`
	Active        bool      `gorm:"not null"`
	UpdatedAt     time.Time `gorm:"not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (AccountPresale) TableName() string { return "account_presales" }

// MaxPresales bounds the presale catalog; MaxPresaleCapacity bounds one
// presale's waiting orders.
//
// MaxPresales 限制预售商品数量；MaxPresaleCapacity 限制单个预售的等待订单数。
const (
	MaxPresales        = 100
	MaxPresaleCapacity = 10_000
)
