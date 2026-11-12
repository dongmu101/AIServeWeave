package gormstore

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"database/sql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// The inventory cap has its own singleton so its lock never reverses the
// payment path's order -> tenant -> account -> revocation-outbox lock order.
type accountSalesLock struct {
	ID int64 `gorm:"primaryKey"`
}

func (accountSalesLock) TableName() string { return "account_sales_lock" }

func (s *Store) ListAccountSales(ctx context.Context) ([]model.AccountSale, error) {
	rows := []model.AccountSale{}
	err := s.db.WithContext(ctx).Order("id").Limit(accountaccess.MaxAccounts + 1).Find(&rows).Error
	return rows, translate(err)
}

func (s *Store) SaveAccountSale(ctx context.Context, row model.AccountSale, create bool) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if create {
			// All inventory creators share the cap lock; targets are also unique.
			var lock accountSalesLock
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = 1").Take(&lock).Error; err != nil {
				return err
			}
			var count int64
			if err := tx.Model(&model.AccountSale{}).Count(&count).Error; err != nil {
				return err
			}
			if count >= accountaccess.MaxAccounts {
				return store.ErrConflict
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			var old model.AccountSale
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", row.ID).Take(&old).Error; err != nil {
				return err
			}
			// A kind may be filled in on a legacy row, never changed.
			// 旧行可以补填类型，但类型一旦填写不可更改。
			if old.NodeID != row.NodeID || old.RuntimeID != row.RuntimeID || (old.Kind != "" && old.Kind != row.Kind) {
				return store.ErrConflict
			}
			result := tx.Model(&model.AccountSale{}).Where("id = ?", row.ID).
				Updates(map[string]any{"kind": row.Kind, "name": row.Name, "price_minor": row.PriceMinor, "currency": row.Currency,
					"dodo_product_id": row.DodoProductID, "active": row.Active, "updated_at": row.UpdatedAt})
			if result.Error != nil {
				return result.Error
			}
		}
		return enqueueRevocation(tx)
	}))
}

// CreateAccountCheckoutOrder implements store.AccountSales. It runs at READ
// COMMITTED so the hold check reads committed checkouts without gap locks;
// the account row lock serializes checkouts for the same account.
//
// CreateAccountCheckoutOrder 实现 store.AccountSales。以 READ COMMITTED 运行，使占用
// 检查读取已提交的结账且不加间隙锁；账号行锁使同一账号的结账串行。
func (s *Store) CreateAccountCheckoutOrder(ctx context.Context, order *model.CheckoutOrder, now time.Time) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sale model.AccountSale
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", order.AccountID).Take(&sale).Error; err != nil {
			return err
		}
		if err := store.AccountCheckoutTerms(sale, order.TenantID, order.Provider, order.AmountMinor, order.Currency, now); err != nil {
			return err
		}
		if held, err := accountCheckoutHeld(tx, order.AccountID, order.TenantID, now); err != nil {
			return err
		} else if held {
			return store.ErrConflict
		}
		return tx.Create(order).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted}))
}

// FailCheckoutOrder implements store.AccountSales.
//
// FailCheckoutOrder 实现 store.AccountSales。
func (s *Store) FailCheckoutOrder(ctx context.Context, orderID string, now time.Time) error {
	return translate(s.db.WithContext(ctx).Model(&model.CheckoutOrder{}).
		Where("id = ? AND status = ?", orderID, model.OrderStatusPending).
		Updates(map[string]any{"status": model.OrderStatusFailed, "updated_at": now}).Error)
}

func (l gormLedger) AccountSaleByID(id string) (model.AccountSale, error) {
	var out model.AccountSale
	err := l.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) GrantAccount(accountID, tenantID string, expiresAt, now time.Time) error {
	if err := l.tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).
		Updates(map[string]any{"mode": model.BillingModeAccountMonthly, "updated_at": now}).Error; err != nil {
		return err
	}
	if err := l.tx.Model(&model.AccountSale{}).Where("id = ?", accountID).
		Updates(map[string]any{"tenant_id": tenantID, "expires_at": expiresAt, "updated_at": now}).Error; err != nil {
		return err
	}
	return enqueueRevocation(l.tx)
}
