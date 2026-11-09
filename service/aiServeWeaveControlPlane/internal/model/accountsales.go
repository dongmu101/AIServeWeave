package model

import "time"

// AccountSale reserves one Agent runtime for exclusive monthly sale. Archiving
// stops new sales; it never releases the runtime into the shared pool.
// AccountSale 为包月独享售卖保留一个运行时；下架不影响已付费期限，也不会恢复共享。
type AccountSale struct {
	ID         string    `gorm:"primaryKey;size:32"`
	NodeID     string    `gorm:"size:128;not null;uniqueIndex:idx_account_sales_target"`
	RuntimeID  string    `gorm:"size:128;not null;uniqueIndex:idx_account_sales_target"`
	Name       string    `gorm:"size:128;not null"`
	PriceMicro int64     `gorm:"not null"`
	Active     bool      `gorm:"not null"`
	TenantID   string    `gorm:"size:32;not null"`
	ExpiresAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

// AccountPurchase is the immutable receipt and idempotency record for one month.
type AccountPurchase struct {
	ID         string    `gorm:"primaryKey;size:32"`
	AccountID  string    `gorm:"size:32;not null"`
	TenantID   string    `gorm:"size:32;not null"`
	PriceMicro int64     `gorm:"not null"`
	ExpiresAt  time.Time `gorm:"not null"`
	CreatedAt  time.Time `gorm:"not null"`
}
