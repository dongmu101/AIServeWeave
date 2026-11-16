package gormstore

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// catalogBatch bounds one IN list or one batched insert.
//
// catalogBatch 限制一个 IN 列表或一次批量插入的规模。
const catalogBatch = 500

// catalogTxOptions runs catalog writes at READ COMMITTED on both databases,
// as ChargeUsage does: every write first locks the sync-state row, so the
// reads after it see everything the previous holder committed.
//
// catalogTxOptions 让价目表写入在两种数据库上都以 READ COMMITTED 运行，与 ChargeUsage
// 相同：每次写入都先锁定同步状态行，因此其后的读取能看到前一个持锁者提交的全部内容。
var catalogTxOptions = &sql.TxOptions{Isolation: sql.LevelReadCommitted}

// CatalogSyncState implements store.PriceCatalog.
//
// CatalogSyncState 实现 store.PriceCatalog。
func (s *Store) CatalogSyncState(ctx context.Context) (model.CatalogSyncState, error) {
	var state model.CatalogSyncState
	err := s.db.WithContext(ctx).Where("id = ?", model.CatalogSyncStateID).Take(&state).Error
	return state, translate(err)
}

// ApplyCatalogSnapshot implements store.PriceCatalog.
//
// ApplyCatalogSnapshot 实现 store.PriceCatalog。
func (s *Store) ApplyCatalogSnapshot(ctx context.Context, snap pricecatalog.Snapshot, opts store.CatalogApplyOptions, now time.Time) (store.CatalogApplyResult, error) {
	var result store.CatalogApplyResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = store.ApplyCatalogSnapshotRules(gormCatalogTx{tx}, snap, opts, now)
		return err
	}, catalogTxOptions)
	return result, translate(err)
}

// RecordCatalogSyncError implements store.PriceCatalog.
//
// RecordCatalogSyncError 实现 store.PriceCatalog。
func (s *Store) RecordCatalogSyncError(ctx context.Context, message string, now time.Time) error {
	err := s.db.WithContext(ctx).Model(&model.CatalogSyncState{}).Where("id = ?", model.CatalogSyncStateID).
		Updates(map[string]any{"last_error": truncateCatalog(message, 512), "last_error_at": now, "checked_at": now}).Error
	return translate(err)
}

// ApproveCatalogPending implements store.PriceCatalog.
//
// ApproveCatalogPending 实现 store.PriceCatalog。
func (s *Store) ApproveCatalogPending(ctx context.Context, name string, now time.Time) (store.CatalogDecision, error) {
	var decision store.CatalogDecision
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		decision, err = store.ApproveCatalogPendingRules(gormCatalogTx{tx}, name, now)
		return err
	}, catalogTxOptions)
	return decision, translate(err)
}

// RejectCatalogPending implements store.PriceCatalog.
//
// RejectCatalogPending 实现 store.PriceCatalog。
func (s *Store) RejectCatalogPending(ctx context.Context, name string, now time.Time) (store.CatalogDecision, error) {
	var decision store.CatalogDecision
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		decision, err = store.RejectCatalogPendingRules(gormCatalogTx{tx}, name, now)
		return err
	}, catalogTxOptions)
	return decision, translate(err)
}

// ListCatalogEntries implements store.PriceCatalog.
//
// ListCatalogEntries 实现 store.PriceCatalog。
func (s *Store) ListCatalogEntries(ctx context.Context, query store.ListQuery, filter store.CatalogFilter) (store.Page[store.CatalogEntry], error) {
	after, err := store.DecodeNameCursor(query.Cursor)
	if err != nil {
		return store.Page[store.CatalogEntry]{}, err
	}
	size := query.Size()
	db := s.db.WithContext(ctx).Model(&model.CatalogModel{}).Omit("raw_json")
	if filter.Prefix != "" {
		db = db.Where("model LIKE ? ESCAPE '!'", likePrefix(filter.Prefix))
	}
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if after != "" {
		db = db.Where("model > ?", after)
	}
	var rows []model.CatalogModel
	if err := db.Order("model").Limit(size + 1).Find(&rows).Error; err != nil {
		return store.Page[store.CatalogEntry]{}, err
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		next = store.EncodeNameCursor(rows[size-1].Model)
	}
	entries, err := s.catalogEntries(ctx, rows)
	if err != nil {
		return store.Page[store.CatalogEntry]{}, err
	}
	return store.Page[store.CatalogEntry]{Items: entries, NextCursor: next}, nil
}

// GetCatalogEntry implements store.PriceCatalog.
//
// GetCatalogEntry 实现 store.PriceCatalog。
func (s *Store) GetCatalogEntry(ctx context.Context, name string) (store.CatalogEntry, error) {
	var row model.CatalogModel
	if err := s.db.WithContext(ctx).Where("model = ?", name).Take(&row).Error; err != nil {
		return store.CatalogEntry{}, translate(err)
	}
	entries, err := s.catalogEntries(ctx, []model.CatalogModel{row})
	if err != nil {
		return store.CatalogEntry{}, err
	}
	return entries[0], nil
}

// catalogEntries attaches the prices in effect and the waiting review kind
// to rows.
//
// catalogEntries 为 rows 附上生效中的单价与等待中的审核类型。
func (s *Store) catalogEntries(ctx context.Context, rows []model.CatalogModel) ([]store.CatalogEntry, error) {
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Model
	}
	prices := map[string][]model.CatalogPrice{}
	pending := map[string]string{}
	for _, chunk := range chunkStrings(names) {
		var priceRows []model.CatalogPrice
		if err := s.db.WithContext(ctx).Where("valid_to IS NULL AND model IN ?", chunk).Find(&priceRows).Error; err != nil {
			return nil, err
		}
		for _, p := range priceRows {
			prices[p.Model] = append(prices[p.Model], p)
		}
		var pendingRows []model.CatalogPending
		if err := s.db.WithContext(ctx).Select("model", "kind").Where("model IN ?", chunk).Find(&pendingRows).Error; err != nil {
			return nil, err
		}
		for _, p := range pendingRows {
			pending[p.Model] = p.Kind
		}
	}
	out := make([]store.CatalogEntry, len(rows))
	for i, row := range rows {
		list := prices[row.Model]
		store.SortCatalogPrices(list)
		out[i] = store.CatalogEntry{Model: row, Prices: list, Pending: pending[row.Model]}
	}
	return out, nil
}

// ListCatalogPending implements store.PriceCatalog.
//
// ListCatalogPending 实现 store.PriceCatalog。
func (s *Store) ListCatalogPending(ctx context.Context, query store.ListQuery) (store.Page[store.CatalogPendingEntry], error) {
	after, err := store.DecodeNameCursor(query.Cursor)
	if err != nil {
		return store.Page[store.CatalogPendingEntry]{}, err
	}
	size := query.Size()
	db := s.db.WithContext(ctx).Model(&model.CatalogPending{})
	if after != "" {
		db = db.Where("model > ?", after)
	}
	var rows []model.CatalogPending
	if err := db.Order("model").Limit(size + 1).Find(&rows).Error; err != nil {
		return store.Page[store.CatalogPendingEntry]{}, err
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		next = store.EncodeNameCursor(rows[size-1].Model)
	}
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Model
	}
	status := map[string]string{}
	prices := map[string][]model.CatalogPrice{}
	for _, chunk := range chunkStrings(names) {
		var models []model.CatalogModel
		if err := s.db.WithContext(ctx).Select("model", "status").Where("model IN ?", chunk).Find(&models).Error; err != nil {
			return store.Page[store.CatalogPendingEntry]{}, err
		}
		for _, m := range models {
			status[m.Model] = m.Status
		}
		var priceRows []model.CatalogPrice
		if err := s.db.WithContext(ctx).Where("valid_to IS NULL AND model IN ?", chunk).Find(&priceRows).Error; err != nil {
			return store.Page[store.CatalogPendingEntry]{}, err
		}
		for _, p := range priceRows {
			prices[p.Model] = append(prices[p.Model], p)
		}
	}
	items := make([]store.CatalogPendingEntry, len(rows))
	for i, row := range rows {
		list := prices[row.Model]
		store.SortCatalogPrices(list)
		items[i] = store.CatalogPendingEntry{Pending: row, Status: status[row.Model], Current: list, Proposed: store.DecodeProposedEntry(row)}
	}
	return store.Page[store.CatalogPendingEntry]{Items: items, NextCursor: next}, nil
}

// CatalogPricesAt implements store.PriceCatalog.
//
// CatalogPricesAt 实现 store.PriceCatalog。
func (s *Store) CatalogPricesAt(ctx context.Context, name string, at time.Time) ([]model.CatalogPrice, error) {
	var rows []model.CatalogPrice
	err := s.db.WithContext(ctx).
		Where("model = ? AND valid_from <= ? AND (valid_to IS NULL OR valid_to > ?)", name, at, at).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, store.ErrNotFound
	}
	store.SortCatalogPrices(rows)
	return rows, nil
}

// catalogReadAttempts bounds CurrentCatalogPrices' retries when a catalog
// write lands between its reads.
//
// catalogReadAttempts 限制 CurrentCatalogPrices 在两次读取之间遇到价目表写入时的重试次数。
const catalogReadAttempts = 5

// CurrentCatalogPrices implements store.PriceCatalog. The version is read
// before and after the prices; a write in between retries the read, so the
// prices returned belong to the version returned on either database's
// default isolation.
//
// CurrentCatalogPrices 实现 store.PriceCatalog。在读取单价之前与之后各读一次版本号；
// 中间发生写入就重读，因此在两种数据库的默认隔离级别下，返回的单价都属于返回的版本。
func (s *Store) CurrentCatalogPrices(ctx context.Context) (store.CatalogPriceSet, error) {
	for attempt := 1; ; attempt++ {
		before, err := s.CatalogSyncState(ctx)
		if err != nil {
			return store.CatalogPriceSet{}, err
		}
		var rows []model.CatalogPrice
		if err := s.db.WithContext(ctx).Where("valid_to IS NULL").Find(&rows).Error; err != nil {
			return store.CatalogPriceSet{}, err
		}
		after, err := s.CatalogSyncState(ctx)
		if err != nil {
			return store.CatalogPriceSet{}, err
		}
		if before.Version != after.Version {
			if attempt >= catalogReadAttempts {
				return store.CatalogPriceSet{}, store.ErrConflict
			}
			continue
		}
		prices := map[string][]model.CatalogPrice{}
		for _, row := range rows {
			prices[row.Model] = append(prices[row.Model], row)
		}
		for name := range prices {
			store.SortCatalogPrices(prices[name])
		}
		return store.CatalogPriceSet{Version: after.Version, Prices: prices}, nil
	}
}

// gormCatalogTx is store.CatalogTx inside one database transaction.
//
// gormCatalogTx 是运行在一个数据库事务内的 store.CatalogTx。
type gormCatalogTx struct{ tx *gorm.DB }

var _ store.CatalogTx = gormCatalogTx{}

func (c gormCatalogTx) LockCatalogSyncState() (model.CatalogSyncState, error) {
	var state model.CatalogSyncState
	err := c.tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", model.CatalogSyncStateID).Take(&state).Error
	return state, translate(err)
}

func (c gormCatalogTx) SaveCatalogSyncState(state model.CatalogSyncState) error {
	return c.tx.Model(&model.CatalogSyncState{}).Where("id = ?", model.CatalogSyncStateID).Updates(map[string]any{
		"source": state.Source, "content_hash": state.ContentHash, "embedded_hash": state.EmbeddedHash,
		"entry_count": state.EntryCount, "version": state.Version, "synced_at": state.SyncedAt,
		"checked_at": state.CheckedAt, "last_error": state.LastError, "last_error_at": state.LastErrorAt,
	}).Error
}

func (c gormCatalogTx) CatalogModels() ([]model.CatalogModel, error) {
	var rows []model.CatalogModel
	return rows, c.tx.Find(&rows).Error
}

func (c gormCatalogTx) CatalogModel(name string) (model.CatalogModel, error) {
	var row model.CatalogModel
	err := c.tx.Where("model = ?", name).Take(&row).Error
	return row, translate(err)
}

func (c gormCatalogTx) SaveCatalogModel(row model.CatalogModel) error {
	return c.tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&row).Error
}

func (c gormCatalogTx) CurrentCatalogPriceRows(name string) ([]model.CatalogPrice, error) {
	db := c.tx.Where("valid_to IS NULL")
	if name != "" {
		db = db.Where("model = ?", name)
	}
	var rows []model.CatalogPrice
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	store.SortCatalogPrices(rows)
	return rows, nil
}

func (c gormCatalogTx) CloseCatalogPrices(ids []int64, at time.Time) error {
	for start := 0; start < len(ids); start += catalogBatch {
		end := min(start+catalogBatch, len(ids))
		if err := c.tx.Model(&model.CatalogPrice{}).Where("id IN ?", ids[start:end]).Update("valid_to", at).Error; err != nil {
			return err
		}
	}
	return nil
}

func (c gormCatalogTx) InsertCatalogPrices(rows []model.CatalogPrice) error {
	if len(rows) == 0 {
		return nil
	}
	return c.tx.CreateInBatches(&rows, catalogBatch).Error
}

func (c gormCatalogTx) CatalogPendingRows() ([]model.CatalogPending, error) {
	var rows []model.CatalogPending
	return rows, c.tx.Find(&rows).Error
}

func (c gormCatalogTx) CatalogPending(name string) (model.CatalogPending, error) {
	var row model.CatalogPending
	err := c.tx.Where("model = ?", name).Take(&row).Error
	return row, translate(err)
}

func (c gormCatalogTx) SaveCatalogPending(row model.CatalogPending) error {
	return c.tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&row).Error
}

func (c gormCatalogTx) DeleteCatalogPending(names []string) error {
	for _, chunk := range chunkStrings(names) {
		if err := c.tx.Where("model IN ?", chunk).Delete(&model.CatalogPending{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// chunkStrings splits names into IN lists of at most catalogBatch.
//
// chunkStrings 把 names 切成不超过 catalogBatch 的 IN 列表。
func chunkStrings(names []string) [][]string {
	var out [][]string
	for start := 0; start < len(names); start += catalogBatch {
		out = append(out, names[start:min(start+catalogBatch, len(names))])
	}
	return out
}

// likePrefix escapes a LIKE prefix with '!' as the escape character, which
// both databases accept in an ESCAPE clause without backslash rules.
//
// likePrefix 以 '!' 作为转义字符转义 LIKE 前缀；两种数据库都能在 ESCAPE 子句中接受它，
// 且不受反斜杠规则影响。
func likePrefix(prefix string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return r.Replace(prefix) + "%"
}

// truncateCatalog shortens s to at most n bytes on a rune boundary.
//
// truncateCatalog 在字符边界上把 s 截短到不超过 n 字节。
func truncateCatalog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
