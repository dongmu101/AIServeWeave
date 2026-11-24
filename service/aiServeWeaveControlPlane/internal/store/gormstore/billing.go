package gormstore

import (
	"context"
	"database/sql"
	"errors"
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
			gift, err := store.CreditGift(model.TenantBilling{}, grant, grant.AmountMicro)
			if err != nil {
				return err
			}
			billing.GiftBalanceMicro = gift
			grant.CreatedAt = store.NextLedgerTime(nil, grant.CreatedAt)
			billing.LastLedgerAt = &grant.CreatedAt
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
		modeChanged := row.Mode != mode
		row.Mode = mode
		if err := tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).
			Updates(map[string]any{"mode": mode, "updated_at": now}).Error; err != nil {
			return err
		}
		if modeChanged || before != row.Blocked() {
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
// order before the already-recorded check, so two concurrent pushes of the
// same batch serialize on them and the second sees the first's records.
//
// The already-recorded check reads usage_records by id alone. usage_records
// is partitioned by created_at, so its primary key is (id, created_at) and
// cannot by itself keep an id unique; reading by id across every partition
// under the tenant locks does, even for a resend whose created_at differs.
// With monthly partitions kept 400 days that is about fourteen primary-key
// probes per id.
//
// The transaction runs at READ COMMITTED on both databases. That is what lets
// the check be a plain read: after the tenant locks, it sees every record
// committed by the push that held them. A locking read would work too, but
// under MySQL's default REPEATABLE READ a locking read of ids that do not
// exist yet takes gap locks, and two replicas pushing unrelated batches would
// deadlock inserting into each other's gaps. Tenants without a billing row
// are exempt and not locked; a concurrent duplicate for one of them meets the
// primary key when its created_at is the same, and at worst adds a report
// row, never a charge, when it is not.
//
// ChargeUsage 实现 store.Billing。在检查「是否已入账」之前，按 id 顺序锁定租户行，
// 因此同一批次的两次并发推送会在这些行上串行化，后一次能看到前一次写入的记录。
//
// 「是否已入账」的检查只按 id 读取 usage_records。usage_records 按 created_at 分区，
// 主键是 (id, created_at)，单靠它无法保证 id 唯一；在租户锁之下按 id 跨所有分区读取则
// 可以，即使重发的记录 created_at 不同也一样。按月分区、保留 400 天时，每个 id 约是
// 14 次主键探查。
//
// 事务在两种数据库上都以 READ COMMITTED 运行，这使检查可以是普通读：拿到租户锁之后，
// 它能看到持有这些锁的那次推送已提交的全部记录。锁定读也能达到目的，但在 MySQL 默认的
// REPEATABLE READ 下，对尚不存在的 id 做锁定读会加间隙锁，两个副本推送互不相关的批次时
// 会因向对方的间隙插入而死锁。没有计费行的租户是 exempt，不加锁；它们的并发重复在
// created_at 相同时会撞上主键，不同时最多多出一行报表记录，绝不会多扣费。
func (s *Store) ChargeUsage(ctx context.Context, records []model.UsageRecord, now, sealAt time.Time) (bool, error) {
	if len(records) == 0 {
		return false, nil
	}
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tenantIDs := make([]string, 0, len(records))
		ids := make([]string, 0, len(records))
		seenTenant := map[string]bool{}
		for _, r := range records {
			ids = append(ids, r.ID)
			if !seenTenant[r.TenantID] {
				seenTenant[r.TenantID] = true
				tenantIDs = append(tenantIDs, r.TenantID)
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
		if err := tx.Model(&model.UsageRecord{}).Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
			return err
		}
		recorded := make(map[string]bool, len(existing))
		for _, id := range existing {
			recorded[id] = true
		}

		fresh := make([]model.UsageRecord, 0, len(records))
		debit := map[string]int64{}
		count := map[string]int{}
		for _, r := range records {
			if recorded[r.ID] {
				continue
			}
			recorded[r.ID] = true
			b, ok := byTenant[r.TenantID]
			charged := ok && b.Mode == model.BillingModePrepaid && r.CostMicro > 0
			r.Charged = &charged
			if charged {
				if debit[r.TenantID] > math.MaxInt64-r.CostMicro {
					return errBalanceOverflow
				}
				debit[r.TenantID] += r.CostMicro
				count[r.TenantID]++
			}
			fresh = append(fresh, r)
		}
		if len(fresh) == 0 {
			return nil
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&fresh).Error; err != nil {
			return err
		}
		for _, tenantID := range tenantIDs {
			amount := debit[tenantID]
			if amount == 0 {
				continue
			}
			b := byTenant[tenantID]
			before := b.BalanceMicro
			if store.SealDue(b, now) {
				if err := sealUsage(tx, &b, now); err != nil {
					return err
				}
			}
			after := b.BalanceMicro - amount
			if after > b.BalanceMicro {
				return errBalanceOverflow
			}
			b.BalanceMicro = after
			store.AccrueUsage(&b, amount, count[tenantID], now, sealAt)
			columns := store.UsageAccumulationColumns(b)
			columns["balance_micro"], columns["updated_at"] = after, now
			if err := tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).Updates(columns).Error; err != nil {
				return err
			}
			if (model.TenantBilling{Mode: b.Mode, BalanceMicro: before}).Blocked() != b.Blocked() {
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

// OpenRefundRequest implements store.Billing.
//
// OpenRefundRequest 实现 store.Billing。
func (s *Store) OpenRefundRequest(ctx context.Context, req model.RefundRequest, policy store.RefundPolicy, now time.Time) (model.RefundRequest, model.CheckoutOrder, bool, error) {
	var (
		order   model.CheckoutOrder
		changed bool
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		req, order, changed, err = store.OpenRefundRules(gormLedger{tx: tx}, req, policy, now)
		return err
	})
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, translate(err)
	}
	return req, order, changed, nil
}

// ApproveRefundRequest implements store.Billing.
//
// ApproveRefundRequest 实现 store.Billing。
func (s *Store) ApproveRefundRequest(ctx context.Context, id, reviewer, note string, amountMinor int64, policy store.RefundPolicy, now time.Time) (model.RefundRequest, model.CheckoutOrder, bool, error) {
	var (
		req     model.RefundRequest
		order   model.CheckoutOrder
		changed bool
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		req, order, changed, err = store.ApproveRefundRules(gormLedger{tx: tx}, id, reviewer, note, amountMinor, policy, now)
		return err
	})
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, translate(err)
	}
	return req, order, changed, nil
}

// TransitionRefundRequest implements store.Billing.
//
// TransitionRefundRequest 实现 store.Billing。
func (s *Store) TransitionRefundRequest(ctx context.Context, t store.RefundTransition, now time.Time) (model.RefundRequest, bool, error) {
	var (
		req     model.RefundRequest
		changed bool
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		req, changed, err = store.TransitionRefundRules(gormLedger{tx: tx}, t, now)
		return err
	})
	if err != nil {
		return model.RefundRequest{}, false, translate(err)
	}
	return req, changed, nil
}

// RecordRefundSubmitted implements store.Billing.
//
// RecordRefundSubmitted 实现 store.Billing。
func (s *Store) RecordRefundSubmitted(ctx context.Context, id, refundID string, now time.Time) error {
	return translate(s.db.WithContext(ctx).Model(&model.RefundRequest{}).
		Where("id = ? AND dodo_refund_id IS NULL", id).
		Updates(map[string]any{"dodo_refund_id": refundID, "updated_at": now}).Error)
}

// GetRefundRequest implements store.Billing.
//
// GetRefundRequest 实现 store.Billing。
func (s *Store) GetRefundRequest(ctx context.Context, id string) (model.RefundRequest, error) {
	var out model.RefundRequest
	return out, translate(s.db.WithContext(ctx).Where("id = ?", id).Take(&out).Error)
}

// ListRefundRequests implements store.Billing.
//
// ListRefundRequests 实现 store.Billing。
func (s *Store) ListRefundRequests(ctx context.Context, query store.ListQuery, filter store.RefundRequestFilter) (store.Page[model.RefundRequest], error) {
	db := s.db.WithContext(ctx).Model(&model.RefundRequest{})
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.OrderID != "" {
		db = db.Where("order_id = ?", filter.OrderID)
	}
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	return readPage(db, query, func(r model.RefundRequest) (time.Time, string) { return r.CreatedAt, r.ID })
}

// ActiveRefundOrders implements store.Billing.
//
// ActiveRefundOrders 实现 store.Billing。
func (s *Store) ActiveRefundOrders(ctx context.Context, tenantID string) (map[string]bool, error) {
	var ids []string
	err := s.db.WithContext(ctx).Model(&model.RefundRequest{}).
		Where("tenant_id = ? AND status IN ?", tenantID, []string{model.RefundStatusPending, model.RefundStatusProcessing}).
		Pluck("order_id", &ids).Error
	if err != nil {
		return nil, translate(err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
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
	if filter.ExcludeTenantDeleted {
		db = db.Where("tenant_deleted_at IS NULL")
	}
	if filter.TenantID != "" {
		db = db.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if filter.Awaiting {
		db = db.Where("presale_id <> '' AND status = ? AND account_expires_at IS NULL", model.OrderStatusPaid)
	}
	if filter.Provider != "" {
		db = db.Where("provider = ?", filter.Provider)
	}
	if !filter.CreatedAfter.IsZero() {
		db = db.Where("created_at > ?", filter.CreatedAfter)
	}
	if !filter.CreatedBefore.IsZero() {
		db = db.Where("created_at < ?", filter.CreatedBefore)
	}
	return readPage(db, query, func(o model.CheckoutOrder) (time.Time, string) { return o.CreatedAt, o.ID })
}

// DeleteCheckoutOrder implements store.Billing.
//
// DeleteCheckoutOrder 实现 store.Billing。
func (s *Store) DeleteCheckoutOrder(ctx context.Context, tenantID, orderID string, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&model.CheckoutOrder{}).
		Where("id = ? AND tenant_id = ? AND tenant_deleted_at IS NULL", orderID, tenantID).
		UpdateColumn("tenant_deleted_at", now)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
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
	gift, err := store.CreditGift(row, entry, after)
	if err != nil {
		return false, false, err
	}
	// The pending usage is sealed first, so the usage entry lands before
	// this one and the balance chain holds (store.SealUsage). Sealing moves
	// neither the balance nor its gift part, both already charged.
	// 先为尚未封账的用量封账，使用量账目排在本条之前，余额链保持成立（store.SealUsage）。
	// 封账不改变余额及其赠送部分，二者都已扣过。
	if err := sealUsage(tx, &row, entry.CreatedAt); err != nil {
		return false, false, err
	}
	entry.CreatedAt = store.NextLedgerTime(row.LastLedgerAt, entry.CreatedAt)
	row.LastLedgerAt = &entry.CreatedAt
	row.GiftBalanceMicro = gift
	columns := store.UsageAccumulationColumns(row)
	columns["balance_micro"], columns["updated_at"] = after, entry.CreatedAt
	if err := tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", entry.TenantID).Updates(columns).Error; err != nil {
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

func (l gormLedger) TenantBilling(tenantID string, now time.Time) (model.TenantBilling, error) {
	return lockTenantBilling(l.tx, tenantID, now)
}

func (l gormLedger) RefundRequestOrderID(id string) (string, error) {
	var req model.RefundRequest
	err := l.tx.Select("order_id").Where("id = ?", id).Take(&req).Error
	return req.OrderID, translate(err)
}

func (l gormLedger) RefundRequestByID(id string) (model.RefundRequest, error) {
	var out model.RefundRequest
	err := l.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) RefundRequestByRefund(refundID string) (model.RefundRequest, error) {
	var out model.RefundRequest
	err := l.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("dodo_refund_id = ?", refundID).Take(&out).Error
	return out, translate(err)
}

func (l gormLedger) OrderHasActiveRefund(orderID, exceptID string) (bool, error) {
	var n int64
	err := l.tx.Model(&model.RefundRequest{}).
		Where("order_id = ? AND id <> ? AND status IN ?", orderID, exceptID, []string{model.RefundStatusPending, model.RefundStatusProcessing}).
		Count(&n).Error
	return n > 0, err
}

func (l gormLedger) InsertRefundRequest(req model.RefundRequest) error {
	return l.tx.Create(&req).Error
}

func (l gormLedger) SaveRefundRequest(req model.RefundRequest) error {
	return l.tx.Model(&model.RefundRequest{}).Where("id = ?", req.ID).Updates(map[string]any{
		"status": req.Status, "amount_minor": req.AmountMinor, "currency": req.Currency,
		"credits_micro": req.CreditsMicro, "reviewed_by": req.ReviewedBy, "review_note": req.ReviewNote,
		"dodo_refund_id": req.DodoRefundID, "failure": req.Failure, "updated_at": req.UpdatedAt,
	}).Error
}

func (l gormLedger) SaveOrder(order model.CheckoutOrder) error {
	return l.tx.Model(&model.CheckoutOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
		"dodo_payment_id": order.DodoPaymentID, "refunded_minor": order.RefundedMinor,
		"paid_amount_minor": order.PaidAmountMinor, "paid_currency": order.PaidCurrency,
		"status": order.Status, "account_id": order.AccountID, "account_expires_at": order.AccountExpiresAt, "updated_at": order.UpdatedAt,
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

// sealUsage seals row's pending usage into a ledger entry inside tx, row
// being locked; the caller writes the changed row back. A row that predates
// LastLedgerAt reads it from the ledger first.
//
// sealUsage 在 tx 内把 row 尚未封账的用量封成一条账目（row 已加锁）；由调用方把改动后
// 的行写回。早于 LastLedgerAt 出现的行会先从账目中读取它。
func sealUsage(tx *gorm.DB, row *model.TenantBilling, now time.Time) error {
	if row.UsageAccCount == 0 {
		return nil
	}
	if err := loadLastLedgerAt(tx, row); err != nil {
		return err
	}
	entry := store.SealUsage(row, now)
	return tx.Create(entry).Error
}

// loadLastLedgerAt fills row.LastLedgerAt from the tenant's latest ledger
// entry when it is still unset.
//
// loadLastLedgerAt 在 row.LastLedgerAt 尚未设置时，用租户最新一条账目的时间填写它。
func loadLastLedgerAt(tx *gorm.DB, row *model.TenantBilling) error {
	if row.LastLedgerAt != nil {
		return nil
	}
	var last struct{ At *time.Time }
	if err := tx.Model(&model.CreditLedgerEntry{}).Select("MAX(created_at) AS at").
		Where("tenant_id = ?", row.TenantID).Scan(&last).Error; err != nil {
		return err
	}
	if last.At != nil {
		at := last.At.UTC()
		row.LastLedgerAt = &at
	}
	return nil
}

// SealDueUsage implements store.UsageLedger. Each tenant is sealed in its
// own READ COMMITTED transaction under its billing-row lock and re-checked
// there, so replicas running it at once seal each accumulation once.
//
// SealDueUsage 实现 store.UsageLedger。每个租户在各自的 READ COMMITTED 事务中、在其
// 计费行锁之下封账，并在锁内重新检查，因此各副本同时执行也只会为每个累计封账一次。
func (s *Store) SealDueUsage(ctx context.Context, now time.Time) (int, error) {
	var due []string
	if err := s.db.WithContext(ctx).Model(&model.TenantBilling{}).
		Where("usage_acc_until IS NOT NULL AND usage_acc_until <= ?", now).
		Order("usage_acc_until").Limit(store.SealDueBatch).Pluck("tenant_id", &due).Error; err != nil {
		return 0, translate(err)
	}
	sealed := 0
	for _, tenantID := range due {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var row model.TenantBilling
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ?", tenantID).Take(&row).Error; err != nil {
				return err
			}
			if !store.SealDue(row, now) {
				return nil
			}
			if err := sealUsage(tx, &row, now); err != nil {
				return err
			}
			sealed++
			return tx.Model(&model.TenantBilling{}).Where("tenant_id = ?", tenantID).
				Updates(store.UsageAccumulationColumns(row)).Error
		}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return sealed, translate(err)
		}
	}
	return sealed, nil
}
