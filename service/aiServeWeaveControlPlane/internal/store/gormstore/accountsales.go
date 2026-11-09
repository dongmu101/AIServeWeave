package gormstore

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (s *Store) ListAccountSales(ctx context.Context) ([]model.AccountSale, error) {
	rows := []model.AccountSale{}
	err := s.db.WithContext(ctx).Order("id").Limit(accountaccess.MaxAccounts + 1).Find(&rows).Error
	return rows, translate(err)
}

func (s *Store) SaveAccountSale(ctx context.Context, row model.AccountSale, create bool) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if create {
			// All inventory writers share the durable singleton lock, including
			// the cap check; the target also has a database unique constraint.
			var lock revocationOutbox
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
			result := tx.Model(&model.AccountSale{}).Where("id = ? AND node_id = ? AND runtime_id = ?", row.ID, row.NodeID, row.RuntimeID).
				Updates(map[string]any{"name": row.Name, "price_micro": row.PriceMicro, "active": row.Active, "updated_at": row.UpdatedAt})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return store.ErrConflict
			}
		}
		return enqueueRevocation(tx)
	}))
}

func (s *Store) BuyAccount(ctx context.Context, accountID, tenantID, actorID, requestID string, price int64, now time.Time) (model.AccountPurchase, error) {
	var receipt model.AccountPurchase
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Same order as refunds/usage: lock the tenant balance before debit.
		balance, err := lockTenantBilling(tx, tenantID, now)
		if err != nil {
			return err
		}
		err = tx.Where("id = ?", requestID).Take(&receipt).Error
		if err == nil {
			if receipt.TenantID != tenantID || receipt.AccountID != accountID || receipt.PriceMicro != price {
				return store.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var sale model.AccountSale
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", accountID).Take(&sale).Error; err != nil {
			return err
		}
		end, err := store.AccountPurchaseTerms(sale, tenantID, price, now)
		if err != nil {
			return err
		}
		if balance.BalanceMicro < price {
			return store.ErrInsufficientCredit
		}
		balance.BalanceMicro -= price
		if err := tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).
			Updates(map[string]any{"balance_micro": balance.BalanceMicro, "mode": model.BillingModeAccountMonthly, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.AccountSale{}).Where("id = ?", accountID).
			Updates(map[string]any{"tenant_id": tenantID, "expires_at": end, "updated_at": now}).Error; err != nil {
			return err
		}
		receipt = model.AccountPurchase{ID: requestID, AccountID: accountID, TenantID: tenantID, PriceMicro: price, ExpiresAt: end, CreatedAt: now}
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		key := "account:" + requestID
		if err := tx.Create(&model.CreditLedgerEntry{ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: model.LedgerKindAccountPurchase,
			AmountMicro: -price, BalanceAfterMicro: balance.BalanceMicro, Ref: accountID, ActorID: actorID, IdempotencyKey: &key, CreatedAt: now}).Error; err != nil {
			return err
		}
		return enqueueRevocation(tx)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return receipt, translate(err)
}
