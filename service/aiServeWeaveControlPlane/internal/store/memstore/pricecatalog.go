package memstore

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// priceCatalogState is the in-memory price catalog: entries, every price
// row ever written (closed rows included), waiting reviews and the sync
// state.
//
// priceCatalogState 是内存中的价目表：条目、写入过的全部单价行（包括已关闭的行）、等待中的
// 审核与同步状态。
type priceCatalogState struct {
	models  map[string]model.CatalogModel
	prices  []model.CatalogPrice
	nextID  int64
	pending map[string]model.CatalogPending
	sync    model.CatalogSyncState
}

// catalog returns the catalog state, creating it on first use. The caller
// holds s.mu.
//
// catalog 返回价目表状态，首次使用时创建。调用方须持有 s.mu。
func (s *Store) catalog() *priceCatalogState {
	if s.priceCatalog == nil {
		s.priceCatalog = &priceCatalogState{
			models:  map[string]model.CatalogModel{},
			pending: map[string]model.CatalogPending{},
			nextID:  1,
			sync:    model.CatalogSyncState{ID: model.CatalogSyncStateID},
		}
	}
	return s.priceCatalog
}

// clone copies the state, so a failed rule leaves the original untouched.
//
// clone 复制状态，使失败的规则不改动原状态。
func (c *priceCatalogState) clone() *priceCatalogState {
	return &priceCatalogState{
		models:  maps.Clone(c.models),
		prices:  slices.Clone(c.prices),
		nextID:  c.nextID,
		pending: maps.Clone(c.pending),
		sync:    c.sync,
	}
}

// inCatalogTx runs fn on a copy of the catalog and keeps the copy only when
// fn succeeds: this store's transaction.
//
// inCatalogTx 在价目表的副本上运行 fn，只在 fn 成功时保留副本：这就是本 store 的事务。
func (s *Store) inCatalogTx(fn func(store.CatalogTx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	work := s.catalog().clone()
	if err := fn(memCatalogTx{work}); err != nil {
		return err
	}
	s.priceCatalog = work
	return nil
}

// CatalogSyncState implements store.PriceCatalog.
//
// CatalogSyncState 实现 store.PriceCatalog。
func (s *Store) CatalogSyncState(context.Context) (model.CatalogSyncState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog().sync, nil
}

// ApplyCatalogSnapshot implements store.PriceCatalog.
//
// ApplyCatalogSnapshot 实现 store.PriceCatalog。
func (s *Store) ApplyCatalogSnapshot(_ context.Context, snap pricecatalog.Snapshot, opts store.CatalogApplyOptions, now time.Time) (store.CatalogApplyResult, error) {
	var result store.CatalogApplyResult
	err := s.inCatalogTx(func(tx store.CatalogTx) error {
		var err error
		result, err = store.ApplyCatalogSnapshotRules(tx, snap, opts, now)
		return err
	})
	return result, err
}

// RecordCatalogSyncError implements store.PriceCatalog.
//
// RecordCatalogSyncError 实现 store.PriceCatalog。
func (s *Store) RecordCatalogSyncError(_ context.Context, message string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := &s.catalog().sync
	if len(message) > 512 {
		message = message[:512]
	}
	state.LastError, state.LastErrorAt, state.CheckedAt = message, &now, &now
	return nil
}

// ApproveCatalogPending implements store.PriceCatalog.
//
// ApproveCatalogPending 实现 store.PriceCatalog。
func (s *Store) ApproveCatalogPending(_ context.Context, name string, now time.Time) (store.CatalogDecision, error) {
	var decision store.CatalogDecision
	err := s.inCatalogTx(func(tx store.CatalogTx) error {
		var err error
		decision, err = store.ApproveCatalogPendingRules(tx, name, now)
		return err
	})
	return decision, err
}

// RejectCatalogPending implements store.PriceCatalog.
//
// RejectCatalogPending 实现 store.PriceCatalog。
func (s *Store) RejectCatalogPending(_ context.Context, name string, now time.Time) (store.CatalogDecision, error) {
	var decision store.CatalogDecision
	err := s.inCatalogTx(func(tx store.CatalogTx) error {
		var err error
		decision, err = store.RejectCatalogPendingRules(tx, name, now)
		return err
	})
	return decision, err
}

// ListCatalogEntries implements store.PriceCatalog.
//
// ListCatalogEntries 实现 store.PriceCatalog。
func (s *Store) ListCatalogEntries(_ context.Context, query store.ListQuery, filter store.CatalogFilter) (store.Page[store.CatalogEntry], error) {
	after, err := store.DecodeNameCursor(query.Cursor)
	if err != nil {
		return store.Page[store.CatalogEntry]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.catalog()
	var names []string
	for name, row := range c.models {
		if (filter.Prefix == "" || strings.HasPrefix(name, filter.Prefix)) &&
			(filter.Status == "" || row.Status == filter.Status) && name > after {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	size := query.Size()
	next := ""
	if len(names) > size {
		names = names[:size]
		next = store.EncodeNameCursor(names[size-1])
	}
	items := make([]store.CatalogEntry, len(names))
	for i, name := range names {
		row := c.models[name]
		row.RawJSON = ""
		items[i] = store.CatalogEntry{Model: row, Prices: c.current(name), Pending: c.pending[name].Kind}
	}
	return store.Page[store.CatalogEntry]{Items: items, NextCursor: next}, nil
}

// GetCatalogEntry implements store.PriceCatalog.
//
// GetCatalogEntry 实现 store.PriceCatalog。
func (s *Store) GetCatalogEntry(_ context.Context, name string) (store.CatalogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.catalog()
	row, ok := c.models[name]
	if !ok {
		return store.CatalogEntry{}, store.ErrNotFound
	}
	return store.CatalogEntry{Model: row, Prices: c.current(name), Pending: c.pending[name].Kind}, nil
}

// ListCatalogPending implements store.PriceCatalog.
//
// ListCatalogPending 实现 store.PriceCatalog。
func (s *Store) ListCatalogPending(_ context.Context, query store.ListQuery) (store.Page[store.CatalogPendingEntry], error) {
	after, err := store.DecodeNameCursor(query.Cursor)
	if err != nil {
		return store.Page[store.CatalogPendingEntry]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.catalog()
	var names []string
	for name := range c.pending {
		if name > after {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	size := query.Size()
	next := ""
	if len(names) > size {
		names = names[:size]
		next = store.EncodeNameCursor(names[size-1])
	}
	items := make([]store.CatalogPendingEntry, len(names))
	for i, name := range names {
		row := c.pending[name]
		items[i] = store.CatalogPendingEntry{Pending: row, Status: c.models[name].Status, Current: c.current(name), Proposed: store.DecodeProposedEntry(row)}
	}
	return store.Page[store.CatalogPendingEntry]{Items: items, NextCursor: next}, nil
}

// CatalogPricesAt implements store.PriceCatalog.
//
// CatalogPricesAt 实现 store.PriceCatalog。
func (s *Store) CatalogPricesAt(_ context.Context, name string, at time.Time) ([]model.CatalogPrice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.CatalogPrice
	for _, row := range s.catalog().prices {
		if row.Model == name && !row.ValidFrom.After(at) && (row.ValidTo == nil || row.ValidTo.After(at)) {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return nil, store.ErrNotFound
	}
	store.SortCatalogPrices(out)
	return out, nil
}

// CurrentCatalogPrices implements store.PriceCatalog.
//
// CurrentCatalogPrices 实现 store.PriceCatalog。
func (s *Store) CurrentCatalogPrices(context.Context) (store.CatalogPriceSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.catalog()
	set := store.CatalogPriceSet{Version: c.sync.Version, Prices: map[string][]model.CatalogPrice{}}
	for _, row := range c.prices {
		if row.ValidTo == nil {
			set.Prices[row.Model] = append(set.Prices[row.Model], row)
		} else if row.ValidTo.After(set.ChangedAt) {
			set.ChangedAt = *row.ValidTo
		}
		if row.ValidFrom.After(set.ChangedAt) {
			set.ChangedAt = row.ValidFrom
		}
	}
	for name := range set.Prices {
		store.SortCatalogPrices(set.Prices[name])
	}
	return set, nil
}

// current returns the prices of name in effect, sorted.
//
// current 返回 name 生效中的单价，已排序。
func (c *priceCatalogState) current(name string) []model.CatalogPrice {
	var out []model.CatalogPrice
	for _, row := range c.prices {
		if row.Model == name && row.ValidTo == nil {
			out = append(out, row)
		}
	}
	store.SortCatalogPrices(out)
	return out
}

// memCatalogTx is store.CatalogTx over a working copy of the catalog.
//
// memCatalogTx 是基于价目表工作副本的 store.CatalogTx。
type memCatalogTx struct{ c *priceCatalogState }

var _ store.CatalogTx = memCatalogTx{}

func (t memCatalogTx) LockCatalogSyncState() (model.CatalogSyncState, error) { return t.c.sync, nil }

func (t memCatalogTx) SaveCatalogSyncState(state model.CatalogSyncState) error {
	state.ID = model.CatalogSyncStateID
	t.c.sync = state
	return nil
}

func (t memCatalogTx) CatalogModels() ([]model.CatalogModel, error) {
	return slices.Collect(maps.Values(t.c.models)), nil
}

func (t memCatalogTx) CatalogModel(name string) (model.CatalogModel, error) {
	row, ok := t.c.models[name]
	if !ok {
		return model.CatalogModel{}, store.ErrNotFound
	}
	return row, nil
}

func (t memCatalogTx) SaveCatalogModel(row model.CatalogModel) error {
	t.c.models[row.Model] = row
	return nil
}

func (t memCatalogTx) CurrentCatalogPriceRows(name string) ([]model.CatalogPrice, error) {
	var out []model.CatalogPrice
	for _, row := range t.c.prices {
		if row.ValidTo == nil && (name == "" || row.Model == name) {
			out = append(out, row)
		}
	}
	store.SortCatalogPrices(out)
	return out, nil
}

func (t memCatalogTx) CloseCatalogPrices(ids []int64, at time.Time) error {
	closing := make(map[int64]bool, len(ids))
	for _, id := range ids {
		closing[id] = true
	}
	for i := range t.c.prices {
		if closing[t.c.prices[i].ID] {
			end := at
			t.c.prices[i].ValidTo = &end
		}
	}
	return nil
}

func (t memCatalogTx) InsertCatalogPrices(rows []model.CatalogPrice) error {
	for _, row := range rows {
		row.ID = t.c.nextID
		t.c.nextID++
		t.c.prices = append(t.c.prices, row)
	}
	return nil
}

func (t memCatalogTx) CatalogPendingRows() ([]model.CatalogPending, error) {
	return slices.Collect(maps.Values(t.c.pending)), nil
}

func (t memCatalogTx) CatalogPending(name string) (model.CatalogPending, error) {
	row, ok := t.c.pending[name]
	if !ok {
		return model.CatalogPending{}, store.ErrNotFound
	}
	return row, nil
}

func (t memCatalogTx) SaveCatalogPending(row model.CatalogPending) error {
	t.c.pending[row.Model] = row
	return nil
}

func (t memCatalogTx) DeleteCatalogPending(names []string) error {
	for _, name := range names {
		delete(t.c.pending, name)
	}
	return nil
}
