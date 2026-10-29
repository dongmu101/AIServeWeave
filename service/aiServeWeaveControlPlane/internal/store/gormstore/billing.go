package gormstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// errBalanceOverflow refuses a credit that would wrap the balance around.
//
// errBalanceOverflow 拒绝会让余额回绕的入账。
var errBalanceOverflow = errors.New("gormstore: balance overflow")

// GetTenantBilling implements store.Billing.
//
// GetTenantBilling 实现 store.Billing。
func (s *Store) GetTenantBilling(ctx context.Context, tenantID string) (model.TenantBilling, error) {
	var out model.TenantBilling
	err := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Take(&out).Error
	return out, translate(err)
}

// GetTenantBillings implements store.Billing.
//
// GetTenantBillings 实现 store.Billing。
func (s *Store) GetTenantBillings(ctx context.Context, tenantIDs []string) (map[string]model.TenantBilling, error) {
	out := make(map[string]model.TenantBilling, len(tenantIDs))
	if len(tenantIDs) == 0 {
		return out, nil
	}
	var rows []model.TenantBilling
	if err := s.db.WithContext(ctx).Where("tenant_id IN ?", tenantIDs).Find(&rows).Error; err != nil {
		return nil, translate(err)
	}
	for _, row := range rows {
		out[row.TenantID] = row
	}
	return out, nil
}

// InitTenantBilling implements store.Billing.
//
// InitTenantBilling 实现 store.Billing。
func (s *Store) InitTenantBilling(ctx context.Context, billing model.TenantBilling, grant *model.CreditLedgerEntry) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if grant != nil {
			billing.BalanceMicro = grant.AmountMicro
			grant.BalanceAfterMicro = grant.AmountMicro
		}
		if err := tx.Create(&billing).Error; err != nil {
			return err
		}
		if grant != nil {
			return tx.Create(grant).Error
		}
		return nil
	}))
}

// SetTenantBillingMode implements store.Billing.
//
// SetTenantBillingMode 实现 store.Billing。
func (s *Store) SetTenantBillingMode(ctx context.Context, tenantID, mode string, now time.Time) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockTenantBilling(tx, tenantID, now)
		if err != nil {
			return err
		}
		before := row.Blocked()
		row.Mode = mode
		if err := tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).
			Updates(map[string]any{"mode": mode, "updated_at": now}).Error; err != nil {
			return err
		}
		if before != row.Blocked() {
			changed = true
			return enqueueRevocation(tx)
		}
		return nil
	})
	return changed, translate(err)
}

// SetDodoCustomerID implements store.Billing.
//
// SetDodoCustomerID 实现 store.Billing。
func (s *Store) SetDodoCustomerID(ctx context.Context, tenantID, customerID string, now time.Time) (string, error) {
	effective := customerID
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockTenantBilling(tx, tenantID, now)
		if err != nil {
			return err
		}
		if row.DodoCustomerID != "" {
			effective = row.DodoCustomerID
			return nil
		}
		return tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).
			Updates(map[string]any{"dodo_customer_id": customerID, "updated_at": now}).Error
	})
	return effective, translate(err)
}

// AdjustCredit implements store.Billing.
//
// AdjustCredit 实现 store.Billing。
func (s *Store) AdjustCredit(ctx context.Context, entry model.CreditLedgerEntry) (model.CreditLedgerEntry, bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		changed, _, err = applyCredit(tx, &entry)
		return err
	})
	return entry, changed, translate(err)
}

// ChargeUsage implements store.Billing. The tenant rows are locked in id
// order before the already-charged check, so two concurrent pushes of the
// same batch serialize on them and the second sees the first's charges.
//
// The transaction runs at READ COMMITTED on both databases. That is what lets
// the already-charged check be a plain read: after the tenant locks, it sees
// every charge committed by the push that held them. A locking read would
// work too, but under MySQL's default REPEATABLE READ a locking read of
// request ids that do not exist yet takes gap locks, and two replicas pushing
// unrelated batches would deadlock inserting into each other's gaps.
// Tenants without a billing row are exempt, so a concurrent duplicate for
// one of them only meets the primary key and inserts nothing.
//
// ChargeUsage 实现 store.Billing。在检查「是否已扣费」之前，按 id 顺序锁定租户行，
// 因此同一批次的两次并发推送会在这些行上串行化，后一次能看到前一次的扣费。
//
// 事务在两种数据库上都以 READ COMMITTED 运行，这使「是否已扣费」的检查可以是普通读：
// 拿到租户锁之后，它能看到持有这些锁的那次推送已提交的全部扣费。锁定读也能达到目的，
// 但在 MySQL 默认的 REPEATABLE READ 下，对尚不存在的 request id 做锁定读会加间隙锁，
// 两个副本推送互不相关的批次时会因向对方的间隙插入而死锁。没有计费行的租户是 exempt，
// 它们的并发重复只会撞上主键，什么也不插入。
func (s *Store) ChargeUsage(ctx context.Context, records []model.UsageRecord, charges []model.UsageCharge) (bool, error) {
	if len(records) == 0 && len(charges) == 0 {
		return false, nil
	}
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(records) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&records).Error; err != nil {
				return err
			}
		}
		if len(charges) == 0 {
			return nil
		}
		tenantIDs := make([]string, 0, len(charges))
		requestIDs := make([]string, 0, len(charges))
		seenTenant := map[string]bool{}
		for _, c := range charges {
			requestIDs = append(requestIDs, c.RequestID)
			if !seenTenant[c.TenantID] {
				seenTenant[c.TenantID] = true
				tenantIDs = append(tenantIDs, c.TenantID)
			}
		}
		sort.Strings(tenantIDs)
		var billings []model.TenantBilling
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id IN ?", tenantIDs).
			Order("tenant_id").Find(&billings).Error; err != nil {
			return err
		}
		byTenant := make(map[string]model.TenantBilling, len(billings))
		for _, b := range billings {
			byTenant[b.TenantID] = b
		}
		var existing []string
		if err := tx.Model(&model.UsageCharge{}).
			Where("request_id IN ?", requestIDs).Pluck("request_id", &existing).Error; err != nil {
			return err
		}
		charged := make(map[string]bool, len(existing))
		for _, id := range existing {
			charged[id] = true
		}

		fresh := make([]model.UsageCharge, 0, len(charges))
		debit := map[string]int64{}
		count := map[string]int{}
		for _, c := range charges {
			if charged[c.RequestID] {
				continue
			}
			charged[c.RequestID] = true
			b, ok := byTenant[c.TenantID]
			c.Charged = ok && b.Mode == model.BillingModePrepaid && c.CostMicro > 0
			if c.Charged {
				if debit[c.TenantID] > math.MaxInt64-c.CostMicro {
					return errBalanceOverflow
				}
				debit[c.TenantID] += c.CostMicro
				count[c.TenantID]++
			}
			fresh = append(fresh, c)
		}
		if len(fresh) == 0 {
			return nil
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&fresh).Error; err != nil {
			return err
		}
		now := fresh[0].CreatedAt
		for _, tenantID := range tenantIDs {
			amount := debit[tenantID]
			if amount == 0 {
				continue
			}
			b := byTenant[tenantID]
			after := b.BalanceMicro - amount
			if after > b.BalanceMicro {
				return errBalanceOverflow
			}
			if err := tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).
				Updates(map[string]any{"balance_micro": after, "updated_at": now}).Error; err != nil {
				return err
			}
			entry := model.CreditLedgerEntry{
				ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: model.LedgerKindUsage,
				AmountMicro: -amount, BalanceAfterMicro: after, Ref: fmt.Sprintf("%d requests", count[tenantID]),
				CreatedAt: now,
			}
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
			if b.Blocked() != (model.TenantBilling{Mode: b.Mode, BalanceMicro: after}).Blocked() {
				changed = true
			}
		}
		if changed {
			return enqueueRevocation(tx)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return changed, translate(err)
}

// ApplyBillingEvent implements store.Billing.
//
// ApplyBillingEvent 实现 store.Billing。
func (s *Store) ApplyBillingEvent(ctx context.Context, event store.BillingEvent, now time.Time) (store.BillingEventResult, error) {
	var result store.BillingEventResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if event.WebhookID != "" {
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.BillingWebhookEvent{
				WebhookID: event.WebhookID, Type: event.Type, ReceivedAt: now,
			})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				result.Outcome = store.BillingOutcomeDuplicate
				return nil
			}
		}
		var err error
		result, err = store.ApplyBillingEventRules(gormLedger{tx: tx}, event, now)
		return err
	})
	if err != nil {
		return store.BillingEventResult{}, translate(err)
	}
	return result, nil
}

// CreateCreditPackage implements store.Billing.
//
// CreateCreditPackage 实现 store.Billing。
func (s *Store) CreateCreditPackage(ctx context.Context, pkg *model.CreditPackage) error {
	return translate(s.db.WithContext(ctx).Create(pkg).Error)
}

// UpdateCreditPackage implements store.Billing.
//
// UpdateCreditPackage 实现 store.Billing。
func (s *Store) UpdateCreditPackage(ctx context.Context, pkg model.CreditPackage) error {
	res := s.db.WithContext(ctx).Model(&model.CreditPackage{}).Where("id = ?", pkg.ID).Select("*").Omit("id", "created_at").Updates(&pkg)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetCreditPackage implements store.Billing.
//
// GetCreditPackage 实现 store.Billing。
func (s *Store) GetCreditPackage(ctx context.Context, id string) (model.CreditPackage, error) {
	var out model.CreditPackage
	return out, translate(s.db.WithContext(ctx).Where("id = ?", id).Take(&out).Error)
}

// ListCreditPackages implements store.Billing.
//
// ListCreditPackages 实现 store.Billing。
func (s *Store) ListCreditPackages(ctx context.Context, activeOnly bool) ([]model.CreditPackage, error) {
	db := s.db.WithContext(ctx).Model(&model.CreditPackage{})
	if activeOnly {
		db = db.Where("active = ?", true)
	}
	var out []model.CreditPackage
	return out, translate(db.Order("sort_order, created_at, id").Find(&out).Error)
}

// ListModelPrices implements store.Billing.
//
// ListModelPrices 实现 store.Billing。
func (s *Store) ListModelPrices(ctx context.Context) ([]model.ModelPrice, error) {
	var out []model.ModelPrice
	return out, translate(s.db.WithContext(ctx).Order("model").Find(&out).Error)
}

// PutModelPrice implements store.Billing.
//
// PutModelPrice 实现 store.Billing。
func (s *Store) PutModelPrice(ctx context.Context, price model.ModelPrice) error {
	return translate(s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&price).Error)
}

// DeleteModelPrice implements store.Billing.
//
// DeleteModelPrice 实现 store.Billing。
func (s *Store) DeleteModelPrice(ctx context.Context, modelAlias string) error {
	res := s.db.WithContext(ctx).Where("model = ?", modelAlias).Delete(&model.ModelPrice{})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// CreateCheckoutOrder implements store.Billing.
//
// CreateCheckoutOrder 实现 store.Billing。
func (s *Store) CreateCheckoutOrder(ctx context.Context, order *model.CheckoutOrder) error {
	return translate(s.db.WithContext(ctx).Create(order).Error)
}

// SetCheckoutOrderSession implements store.Billing.
//
// SetCheckoutOrderSession 实现 store.Billing。
func (s *Store) SetCheckoutOrderSession(ctx context.Context, orderID, sessionID string, now time.Time) error {
	res := s.db.WithContext(ctx).Model(&model.CheckoutOrder{}).Where("id = ?", orderID).
		Updates(map[string]any{"dodo_session_id": sessionID, "updated_at": now})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListCheckoutOrders implements store.Billing.
//
// ListCheckoutOrders 实现 store.Billing。
func (s *Store) ListCheckoutOrders(ctx context.Context, query store.ListQuery, filter store.CheckoutOrderFilter) (store.Page[model.CheckoutOrder], error) {
	db := s.db.WithContext(ctx).Model(&model.CheckoutOrder{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	return readPage(db, query, func(o model.CheckoutOrder) (time.Time, string) { return o.CreatedAt, o.ID })
}

// ListCreditLedger implements store.Billing.
//
// ListCreditLedger 实现 store.Billing。
func (s *Store) ListCreditLedger(ctx context.Context, query store.ListQuery, filter store.CreditLedgerFilter) (store.Page[model.CreditLedgerEntry], error) {
	db := s.db.WithContext(ctx).Model(&model.CreditLedgerEntry{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.Kind != "" {
		db = db.Where("kind = ?", filter.Kind)
	}
	return readPage(db, query, func(e model.CreditLedgerEntry) (time.Time, string) { return e.CreatedAt, e.ID })
}

// lockTenantBilling locks a tenant's billing row, inserting an exempt row with
// a zero balance first when there is none.
//
// lockTenantBilling 锁定租户的计费行；没有计费行时先插入一行零余额的 exempt 记录。
func lockTenantBilling(tx *gorm.DB, tenantID string, now time.Time) (model.TenantBilling, error) {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.TenantBilling{
		TenantID: tenantID, Mode: model.BillingModeExempt, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		return model.TenantBilling{}, err
	}
	var row model.TenantBilling
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ?", tenantID).Take(&row).Error
	return row, err
}

// applyCredit applies entry to its tenant's balance inside tx. A non-nil
// IdempotencyKey already in the ledger makes it a no-op that reports
// duplicate.
//
// applyCredit 在 tx 内把 entry 记入其租户的余额。非空且已出现在账目中的
// IdempotencyKey 会让它成为一次报告 duplicate 的空操作。
func applyCredit(tx *gorm.DB, entry *model.CreditLedgerEntry) (changed, duplicate bool, err error) {
	if entry.IdempotencyKey != nil {
		var n int64
		if err := tx.Model(&model.CreditLedgerEntry{}).Where("idempotency_key = ?", *entry.IdempotencyKey).Count(&n).Error; err != nil {
			return false, false, err
		}
		if n > 0 {
			return false, true, nil
		}
	}
	row, err := lockTenantBilling(tx, entry.TenantID, entry.CreatedAt)
	if err != nil {
		return false, false, err
	}
	after, ok := addBalance(row.BalanceMicro, entry.AmountMicro)
	if !ok {
		return false, false, errBalanceOverflow
	}
	if err := tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", entry.TenantID).
		Updates(map[string]any{"balance_micro": after, "updated_at": entry.CreatedAt}).Error; err != nil {
		return false, false, err
	}
	entry.BalanceAfterMicro = after
	if err := tx.Create(entry).Error; err != nil {
		return false, false, err
	}
	if row.Blocked() != (model.TenantBilling{Mode: row.Mode, BalanceMicro: after}).Blocked() {
		return true, false, enqueueRevocation(tx)
	}
	return false, false, nil
}

// gormLedger adapts a transaction to store.BillingTx.
//
// gormLedger 把一个事务适配为 store.BillingTx。
type gormLedger struct{ tx *gorm.DB }

var _ store.BillingTx = gormLedger{}

func (l gormLedger) OrderByID(id string) (model.CheckoutOrder, error) {
	var out model.CheckoutOrder
	err := l.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) OrderByPayment(paymentID string) (model.CheckoutOrder, error) {
	var out model.CheckoutOrder
	err := l.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("dodo_payment_id = ?", paymentID).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) SaveOrder(order model.CheckoutOrder) error {
	return l.tx.Model(&model.CheckoutOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
		"dodo_payment_id": order.DodoPaymentID, "refunded_minor": order.RefundedMinor,
		"paid_amount_minor": order.PaidAmountMinor, "paid_currency": order.PaidCurrency,
		"status": order.Status, "updated_at": order.UpdatedAt,
	}).Error
}

func (l gormLedger) Credit(entry *model.CreditLedgerEntry) (bool, bool, error) {
	return applyCredit(l.tx, entry)
}

func (l gormLedger) LedgerAmount(key string) (int64, bool, error) {
	var entry model.CreditLedgerEntry
	err := l.tx.Where("idempotency_key = ?", key).Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return entry.AmountMicro, true, nil
}

func addBalance(balance, delta int64) (int64, bool) {
	if (delta > 0 && balance > math.MaxInt64-delta) || (delta < 0 && balance < math.MinInt64-delta) {
		return 0, false
	}
	return balance + delta, true
}
