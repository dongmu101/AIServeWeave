package memstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"sort"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// errBalanceOverflow mirrors gormstore's refusal of a balance that would wrap.
//
// errBalanceOverflow 对应 gormstore 对会回绕的余额的拒绝。
var errBalanceOverflow = errors.New("memstore: balance overflow")

// billingState is the in-memory billing tables. Every method holds Store.mu
// for its whole run, which is this store's stand-in for a transaction.
//
// billingState 是内存版的计费表。每个方法在整个执行期间都持有 Store.mu，这就是本
// store 对事务的替代。
type billingState struct {
	tenants  map[string]model.TenantBilling
	packages map[string]model.CreditPackage
	prices   map[string]model.ModelPrice
	orders   map[string]model.CheckoutOrder
	ledger   []model.CreditLedgerEntry
	charges  map[string]model.UsageCharge
	webhooks map[string]model.BillingWebhookEvent
}

func newBillingState() billingState {
	return billingState{
		tenants:  map[string]model.TenantBilling{},
		packages: map[string]model.CreditPackage{},
		prices:   map[string]model.ModelPrice{},
		orders:   map[string]model.CheckoutOrder{},
		charges:  map[string]model.UsageCharge{},
		webhooks: map[string]model.BillingWebhookEvent{},
	}
}

// GetTenantBilling implements store.Billing.
//
// GetTenantBilling 实现 store.Billing。
func (s *Store) GetTenantBilling(_ context.Context, tenantID string) (model.TenantBilling, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.billing.tenants[tenantID]
	if !ok {
		return model.TenantBilling{}, store.ErrNotFound
	}
	return row, nil
}

// GetTenantBillings implements store.Billing.
//
// GetTenantBillings 实现 store.Billing。
func (s *Store) GetTenantBillings(_ context.Context, tenantIDs []string) (map[string]model.TenantBilling, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]model.TenantBilling, len(tenantIDs))
	for _, id := range tenantIDs {
		if row, ok := s.billing.tenants[id]; ok {
			out[id] = row
		}
	}
	return out, nil
}

// InitTenantBilling implements store.Billing.
//
// InitTenantBilling 实现 store.Billing。
func (s *Store) InitTenantBilling(_ context.Context, billing model.TenantBilling, grant *model.CreditLedgerEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.billing.tenants[billing.TenantID]; ok {
		return store.ErrConflict
	}
	if grant != nil {
		billing.BalanceMicro = grant.AmountMicro
		grant.BalanceAfterMicro = grant.AmountMicro
		s.billing.ledger = append(s.billing.ledger, *grant)
	}
	s.billing.tenants[billing.TenantID] = billing
	return nil
}

// SetTenantBillingMode implements store.Billing.
//
// SetTenantBillingMode 实现 store.Billing。
func (s *Store) SetTenantBillingMode(_ context.Context, tenantID, mode string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.billingRow(tenantID, now)
	before := row.Blocked()
	row.Mode, row.UpdatedAt = mode, now
	s.billing.tenants[tenantID] = row
	return before != row.Blocked(), nil
}

// SetDodoCustomerID implements store.Billing.
//
// SetDodoCustomerID 实现 store.Billing。
func (s *Store) SetDodoCustomerID(_ context.Context, tenantID, customerID string, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.billingRow(tenantID, now)
	if row.DodoCustomerID != "" {
		return row.DodoCustomerID, nil
	}
	row.DodoCustomerID, row.UpdatedAt = customerID, now
	s.billing.tenants[tenantID] = row
	return customerID, nil
}

// AdjustCredit implements store.Billing.
//
// AdjustCredit 实现 store.Billing。
func (s *Store) AdjustCredit(_ context.Context, entry model.CreditLedgerEntry) (model.CreditLedgerEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed, _, err := s.applyCredit(&entry)
	return entry, changed, err
}

// ChargeUsage implements store.Billing.
//
// ChargeUsage 实现 store.Billing。
func (s *Store) ChargeUsage(_ context.Context, records []model.UsageRecord, charges []model.UsageCharge) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Everything is priced and checked before anything is written, so an
	// overflow leaves the store as it was, as gormstore's rollback does.
	// 先完成全部定价与检查再写入，溢出时 store 保持原样，与 gormstore 的回滚一致。
	debit := map[string]int64{}
	count := map[string]int{}
	var order []string
	fresh := make([]model.UsageCharge, 0, len(charges))
	seen := map[string]bool{}
	for _, c := range charges {
		if _, done := s.billing.charges[c.RequestID]; done || seen[c.RequestID] {
			continue
		}
		seen[c.RequestID] = true
		b, ok := s.billing.tenants[c.TenantID]
		c.Charged = ok && b.Mode == model.BillingModePrepaid && c.CostMicro > 0
		if c.Charged {
			if debit[c.TenantID] > math.MaxInt64-c.CostMicro {
				return false, errBalanceOverflow
			}
			if _, counted := debit[c.TenantID]; !counted {
				order = append(order, c.TenantID)
			}
			debit[c.TenantID] += c.CostMicro
			count[c.TenantID]++
		}
		fresh = append(fresh, c)
	}
	for _, tenantID := range order {
		if s.billing.tenants[tenantID].BalanceMicro < math.MinInt64+debit[tenantID] {
			return false, errBalanceOverflow
		}
	}

	for _, r := range records {
		if _, exists := s.usageRecords[r.ID]; !exists {
			s.usageRecords[r.ID] = r
		}
	}
	var now time.Time
	for _, c := range fresh {
		s.billing.charges[c.RequestID] = c
		now = c.CreatedAt
	}
	changed := false
	for _, tenantID := range order {
		b := s.billing.tenants[tenantID]
		before := b.Blocked()
		b.BalanceMicro -= debit[tenantID]
		b.UpdatedAt = now
		s.billing.tenants[tenantID] = b
		s.billing.ledger = append(s.billing.ledger, model.CreditLedgerEntry{
			ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: model.LedgerKindUsage,
			AmountMicro: -debit[tenantID], BalanceAfterMicro: b.BalanceMicro, Ref: requestsRef(count[tenantID]), CreatedAt: now,
		})
		if before != b.Blocked() {
			changed = true
		}
	}
	return changed, nil
}

// ApplyBillingEvent implements store.Billing.
//
// ApplyBillingEvent 实现 store.Billing。
func (s *Store) ApplyBillingEvent(_ context.Context, event store.BillingEvent, now time.Time) (store.BillingEventResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.WebhookID != "" {
		if _, seen := s.billing.webhooks[event.WebhookID]; seen {
			return store.BillingEventResult{Outcome: store.BillingOutcomeDuplicate}, nil
		}
	}
	// The rules may fail after writing an order; restoring the snapshot is
	// this store's rollback, so a failed event leaves no trace and its
	// redelivery is not taken for a duplicate.
	// 规则可能在写入订单之后才失败；恢复快照就是本 store 的回滚，使失败的事件不留痕迹，
	// 其重新投递也不会被当作重复。
	restore := s.snapshotBilling()
	result, err := store.ApplyBillingEventRules(memLedger{s}, event, now)
	if err != nil {
		restore()
		return store.BillingEventResult{}, err
	}
	if event.WebhookID != "" {
		s.billing.webhooks[event.WebhookID] = model.BillingWebhookEvent{WebhookID: event.WebhookID, Type: event.Type, ReceivedAt: now}
	}
	return result, nil
}

// snapshotBilling captures the tables an event can change and returns a
// function restoring them. The caller holds s.mu.
//
// snapshotBilling 捕获一个事件可能修改的表，并返回恢复它们的函数。调用方须持有 s.mu。
func (s *Store) snapshotBilling() func() {
	tenants := maps.Clone(s.billing.tenants)
	orders := maps.Clone(s.billing.orders)
	ledgerLen := len(s.billing.ledger)
	return func() {
		s.billing.tenants = tenants
		s.billing.orders = orders
		s.billing.ledger = s.billing.ledger[:ledgerLen]
	}
}

// CreateCreditPackage implements store.Billing.
//
// CreateCreditPackage 实现 store.Billing。
func (s *Store) CreateCreditPackage(_ context.Context, pkg *model.CreditPackage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.billing.packages[pkg.ID]; ok {
		return store.ErrConflict
	}
	s.billing.packages[pkg.ID] = *pkg
	return nil
}

// UpdateCreditPackage implements store.Billing.
//
// UpdateCreditPackage 实现 store.Billing。
func (s *Store) UpdateCreditPackage(_ context.Context, pkg model.CreditPackage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.billing.packages[pkg.ID]
	if !ok {
		return store.ErrNotFound
	}
	pkg.CreatedAt = old.CreatedAt
	s.billing.packages[pkg.ID] = pkg
	return nil
}

// GetCreditPackage implements store.Billing.
//
// GetCreditPackage 实现 store.Billing。
func (s *Store) GetCreditPackage(_ context.Context, id string) (model.CreditPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pkg, ok := s.billing.packages[id]
	if !ok {
		return model.CreditPackage{}, store.ErrNotFound
	}
	return pkg, nil
}

// ListCreditPackages implements store.Billing.
//
// ListCreditPackages 实现 store.Billing。
func (s *Store) ListCreditPackages(_ context.Context, activeOnly bool) ([]model.CreditPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.CreditPackage, 0, len(s.billing.packages))
	for _, pkg := range s.billing.packages {
		if activeOnly && !pkg.Active {
			continue
		}
		out = append(out, pkg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ListModelPrices implements store.Billing.
//
// ListModelPrices 实现 store.Billing。
func (s *Store) ListModelPrices(_ context.Context) ([]model.ModelPrice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.ModelPrice, 0, len(s.billing.prices))
	for _, p := range s.billing.prices {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

// PutModelPrice implements store.Billing.
//
// PutModelPrice 实现 store.Billing。
func (s *Store) PutModelPrice(_ context.Context, price model.ModelPrice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.billing.prices[price.Model] = price
	return nil
}

// DeleteModelPrice implements store.Billing.
//
// DeleteModelPrice 实现 store.Billing。
func (s *Store) DeleteModelPrice(_ context.Context, modelAlias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.billing.prices[modelAlias]; !ok {
		return store.ErrNotFound
	}
	delete(s.billing.prices, modelAlias)
	return nil
}

// CreateCheckoutOrder implements store.Billing.
//
// CreateCheckoutOrder 实现 store.Billing。
func (s *Store) CreateCheckoutOrder(_ context.Context, order *model.CheckoutOrder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.billing.orders[order.ID]; ok {
		return store.ErrConflict
	}
	s.billing.orders[order.ID] = *order
	return nil
}

// SetCheckoutOrderSession implements store.Billing.
//
// SetCheckoutOrderSession 实现 store.Billing。
func (s *Store) SetCheckoutOrderSession(_ context.Context, orderID, sessionID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, ok := s.billing.orders[orderID]
	if !ok {
		return store.ErrNotFound
	}
	order.DodoSessionID, order.UpdatedAt = sessionID, now
	s.billing.orders[orderID] = order
	return nil
}

// ListCheckoutOrders implements store.Billing.
//
// ListCheckoutOrders 实现 store.Billing。
func (s *Store) ListCheckoutOrders(_ context.Context, query store.ListQuery, filter store.CheckoutOrderFilter) (store.Page[model.CheckoutOrder], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []model.CheckoutOrder
	for _, o := range s.billing.orders {
		if (filter.TenantID == "" || o.TenantID == filter.TenantID) && (filter.Status == "" || o.Status == filter.Status) {
			items = append(items, o)
		}
	}
	key := func(o model.CheckoutOrder) (time.Time, string) { return o.CreatedAt, o.ID }
	sortNewestFirst(items, key)
	return paginate(items, query, key)
}

// ListCreditLedger implements store.Billing.
//
// ListCreditLedger 实现 store.Billing。
func (s *Store) ListCreditLedger(_ context.Context, query store.ListQuery, filter store.CreditLedgerFilter) (store.Page[model.CreditLedgerEntry], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []model.CreditLedgerEntry
	for _, e := range s.billing.ledger {
		if (filter.TenantID == "" || e.TenantID == filter.TenantID) && (filter.Kind == "" || e.Kind == filter.Kind) {
			items = append(items, e)
		}
	}
	key := func(e model.CreditLedgerEntry) (time.Time, string) { return e.CreatedAt, e.ID }
	sortNewestFirst(items, key)
	return paginate(items, query, key)
}

// billingRow returns a tenant's billing row, an exempt zero-balance one when
// it has none, as gormstore's lockTenantBilling does. The caller holds s.mu.
//
// billingRow 返回租户的计费行；没有时返回一行零余额的 exempt 记录，与 gormstore 的
// lockTenantBilling 相同。调用方须持有 s.mu。
func (s *Store) billingRow(tenantID string, now time.Time) model.TenantBilling {
	row, ok := s.billing.tenants[tenantID]
	if !ok {
		row = model.TenantBilling{TenantID: tenantID, Mode: model.BillingModeExempt, CreatedAt: now, UpdatedAt: now}
	}
	return row
}

// applyCredit is gormstore's applyCredit over the maps. The caller holds s.mu.
//
// applyCredit 是 gormstore 中 applyCredit 在映射上的对应实现。调用方须持有 s.mu。
func (s *Store) applyCredit(entry *model.CreditLedgerEntry) (changed, duplicate bool, err error) {
	if entry.IdempotencyKey != nil {
		if _, seen := s.ledgerByKey(*entry.IdempotencyKey); seen {
			return false, true, nil
		}
	}
	row := s.billingRow(entry.TenantID, entry.CreatedAt)
	if (entry.AmountMicro > 0 && row.BalanceMicro > math.MaxInt64-entry.AmountMicro) ||
		(entry.AmountMicro < 0 && row.BalanceMicro < math.MinInt64-entry.AmountMicro) {
		return false, false, errBalanceOverflow
	}
	before := row.Blocked()
	row.BalanceMicro += entry.AmountMicro
	row.UpdatedAt = entry.CreatedAt
	s.billing.tenants[entry.TenantID] = row
	entry.BalanceAfterMicro = row.BalanceMicro
	s.billing.ledger = append(s.billing.ledger, *entry)
	return before != row.Blocked(), false, nil
}

func (s *Store) ledgerByKey(key string) (model.CreditLedgerEntry, bool) {
	for _, e := range s.billing.ledger {
		if e.IdempotencyKey != nil && *e.IdempotencyKey == key {
			return e, true
		}
	}
	return model.CreditLedgerEntry{}, false
}

// memLedger adapts a locked Store to store.BillingTx.
//
// memLedger 把一个已加锁的 Store 适配为 store.BillingTx。
type memLedger struct{ s *Store }

var _ store.BillingTx = memLedger{}

func (l memLedger) OrderByID(id string) (model.CheckoutOrder, error) {
	order, ok := l.s.billing.orders[id]
	if !ok {
		return model.CheckoutOrder{}, store.ErrNotFound
	}
	return order, nil
}

func (l memLedger) OrderByPayment(paymentID string) (model.CheckoutOrder, error) {
	for _, order := range l.s.billing.orders {
		if order.DodoPaymentID != nil && *order.DodoPaymentID == paymentID {
			return order, nil
		}
	}
	return model.CheckoutOrder{}, store.ErrNotFound
}

func (l memLedger) SaveOrder(order model.CheckoutOrder) error {
	if order.DodoPaymentID != nil {
		for id, other := range l.s.billing.orders {
			if id != order.ID && other.DodoPaymentID != nil && *other.DodoPaymentID == *order.DodoPaymentID {
				return store.ErrConflict
			}
		}
	}
	l.s.billing.orders[order.ID] = order
	return nil
}

func (l memLedger) Credit(entry *model.CreditLedgerEntry) (bool, bool, error) {
	return l.s.applyCredit(entry)
}

func (l memLedger) LedgerAmount(key string) (int64, bool, error) {
	e, ok := l.s.ledgerByKey(key)
	return e.AmountMicro, ok, nil
}

func requestsRef(n int) string {
	return fmt.Sprintf("%d requests", n)
}
