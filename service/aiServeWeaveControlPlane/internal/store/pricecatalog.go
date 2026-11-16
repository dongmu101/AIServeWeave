package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
)

// PriceCatalog keeps the official unit prices imported from the upstream
// price list, their history, the upstream changes waiting for an operator,
// and the sync state. Every write locks the sync-state row first, so
// replicas syncing or reviewing at once serialize on it. See
// .scratch/token-pricing/spec.md「价目表」.
//
// PriceCatalog 保存从上游价目表导入的官方单价及其历史、等待运维审核的上游变动，以及
// 同步状态。每次写入都先锁定同步状态行，因此同时同步或审核的各副本会在它上面串行。
// 见 .scratch/token-pricing/spec.md「价目表」。
type PriceCatalog interface {
	// CatalogSyncState reads the sync-state row.
	//
	// CatalogSyncState 读取同步状态行。
	CatalogSyncState(ctx context.Context) (model.CatalogSyncState, error)
	// ApplyCatalogSnapshot compares snap with the catalog and applies the
	// result under ApplyCatalogSnapshotRules. A snapshot whose content was
	// already applied is skipped.
	//
	// ApplyCatalogSnapshot 比较 snap 与价目表，并按 ApplyCatalogSnapshotRules 执行比较
	// 结果。内容已经采用过的快照会被跳过。
	ApplyCatalogSnapshot(ctx context.Context, snap pricecatalog.Snapshot, opts CatalogApplyOptions, now time.Time) (CatalogApplyResult, error)
	// RecordCatalogSyncError records a failed fetch or parse on the sync
	// state; the catalog and the last applied source are left as they were.
	//
	// RecordCatalogSyncError 在同步状态上记录一次失败的拉取或解析；价目表与最近一次采用的
	// 来源保持不变。
	RecordCatalogSyncError(ctx context.Context, message string, now time.Time) error
	// ApproveCatalogPending applies the waiting review of one entry under
	// ApproveCatalogPendingRules. ErrNotFound when nothing waits.
	//
	// ApproveCatalogPending 按 ApproveCatalogPendingRules 采用某个条目等待中的审核；
	// 没有等待中的审核时返回 ErrNotFound。
	ApproveCatalogPending(ctx context.Context, name string, now time.Time) (CatalogDecision, error)
	// RejectCatalogPending discards the waiting review of one entry under
	// RejectCatalogPendingRules. ErrNotFound when nothing waits.
	//
	// RejectCatalogPending 按 RejectCatalogPendingRules 驳回某个条目等待中的审核；没有
	// 等待中的审核时返回 ErrNotFound。
	RejectCatalogPending(ctx context.Context, name string, now time.Time) (CatalogDecision, error)
	// ListCatalogEntries lists entries in name order with their prices in
	// effect, without the raw upstream entry.
	//
	// ListCatalogEntries 按名称顺序列出条目及其生效中的单价，不含上游原始条目。
	ListCatalogEntries(ctx context.Context, query ListQuery, filter CatalogFilter) (Page[CatalogEntry], error)
	// GetCatalogEntry reads one entry, raw upstream entry included.
	// ErrNotFound when there is none.
	//
	// GetCatalogEntry 读取一个条目（含上游原始条目）；不存在时返回 ErrNotFound。
	GetCatalogEntry(ctx context.Context, name string) (CatalogEntry, error)
	// ListCatalogPending lists the waiting reviews in name order.
	//
	// ListCatalogPending 按名称顺序列出等待中的审核。
	ListCatalogPending(ctx context.Context, query ListQuery) (Page[CatalogPendingEntry], error)
	// CatalogPricesAt returns the prices of one entry in effect at the given
	// moment, in canonical order. ErrNotFound when none was.
	//
	// CatalogPricesAt 返回某个条目在给定时刻生效的单价，按规范顺序排列；当时没有生效的
	// 单价时返回 ErrNotFound。
	CatalogPricesAt(ctx context.Context, name string, at time.Time) ([]model.CatalogPrice, error)
	// CurrentCatalogPrices returns every price in effect now, by entry, with
	// the catalog version they belong to, read consistently.
	//
	// CurrentCatalogPrices 一致地返回当前生效的全部单价（按条目分组）及其所属的价目表
	// 版本号。
	CurrentCatalogPrices(ctx context.Context) (CatalogPriceSet, error)
}

// CatalogApplyOptions says where a snapshot came from and how much an
// existing price may move on its own.
//
// CatalogApplyOptions 说明快照的来源，以及已有单价可以自动变动的幅度。
type CatalogApplyOptions struct {
	Source           string
	Embedded         bool
	MaxChangePercent int
}

// CatalogChange names one entry and the largest relative change among its
// prices, in basis points (pricecatalog.UnboundedChange when unbounded).
//
// CatalogChange 指出一个条目及其单价中最大的相对变动（基点；无界时为
// pricecatalog.UnboundedChange）。
type CatalogChange struct {
	Model       string
	MaxChangeBP int64
}

// CatalogApplyResult is what one ApplyCatalogSnapshot did.
//
// CatalogApplyResult 是一次 ApplyCatalogSnapshot 所做的事。
type CatalogApplyResult struct {
	// Skipped is true when the snapshot's content was already applied.
	// Skipped 在快照内容已经采用过时为 true。
	Skipped    bool
	Added      []string
	Applied    []CatalogChange
	Reviewed   []CatalogChange
	Removed    []string
	Metadata   int
	Unpended   int
	Suppressed int
	// Version is the catalog version after the call.
	// Version 是调用之后的价目表版本号。
	Version int64
}

// CatalogDecision is what an approval or rejection did: the entry, the
// kind of review, the prices in effect before and after, and the catalog
// version after.
//
// CatalogDecision 是一次批准或拒绝所做的事：条目、审核类型、之前与之后生效的单价，以及
// 之后的价目表版本号。
type CatalogDecision struct {
	Model   string
	Kind    string
	Before  []model.CatalogPrice
	After   []model.CatalogPrice
	Version int64
}

// CatalogFilter narrows an entry list. Prefix matches the start of the
// name; an empty field does not filter.
//
// CatalogFilter 收窄条目列表。Prefix 匹配名称开头；字段为空表示不筛选。
type CatalogFilter struct {
	Prefix string
	Status string
}

// CatalogEntry is one entry with its prices in effect and the kind of
// review waiting on it, if any.
//
// CatalogEntry 是一个条目、其生效中的单价，以及等待中的审核类型（如有）。
type CatalogEntry struct {
	Model   model.CatalogModel
	Prices  []model.CatalogPrice
	Pending string
}

// CatalogPendingEntry is one waiting review with the entry's status and
// prices in effect; Proposed is the decoded proposal of a price change.
//
// CatalogPendingEntry 是一个等待中的审核，附带条目状态与生效中的单价；Proposed 是改价
// 提案解码后的内容。
type CatalogPendingEntry struct {
	Pending  model.CatalogPending
	Status   string
	Current  []model.CatalogPrice
	Proposed pricecatalog.Entry
}

// CatalogPriceSet is every price in effect, by entry, at one catalog
// version.
//
// CatalogPriceSet 是某个价目表版本下生效的全部单价，按条目分组。
type CatalogPriceSet struct {
	Version int64
	Prices  map[string][]model.CatalogPrice
	// ChangedAt is the latest valid_from or valid_to of any price, current
	// or closed: the set is the catalog in effect at every moment from
	// ChangedAt on, so an entry absent from it had no price then either.
	// Zero for an empty catalog.
	// ChangedAt 是所有单价（生效中或已关闭）中最晚的 valid_from 或 valid_to：从
	// ChangedAt 起的每一刻，生效中的价目表都就是这个集合，因此集合中没有的条目在那时也
	// 没有单价。价目表为空时为零值。
	ChangedAt time.Time
}

// CatalogTx is the transaction-scoped access the catalog rules need. The
// gormstore implementation runs inside one database transaction; memstore
// works on a copy it keeps only on success.
//
// CatalogTx 是价目表规则所需的事务内访问能力。gormstore 的实现运行在一个数据库事务里；
// memstore 在副本上操作，只在成功时保留。
type CatalogTx interface {
	// LockCatalogSyncState reads and locks the sync-state row.
	//
	// LockCatalogSyncState 读取并锁定同步状态行。
	LockCatalogSyncState() (model.CatalogSyncState, error)
	SaveCatalogSyncState(state model.CatalogSyncState) error
	// CatalogModels reads every entry, raw entry included.
	//
	// CatalogModels 读取全部条目（含原始条目）。
	CatalogModels() ([]model.CatalogModel, error)
	// CatalogModel reads one entry. ErrNotFound when there is none.
	//
	// CatalogModel 读取一个条目；不存在时返回 ErrNotFound。
	CatalogModel(name string) (model.CatalogModel, error)
	// SaveCatalogModel inserts or replaces one entry.
	//
	// SaveCatalogModel 插入或替换一个条目。
	SaveCatalogModel(row model.CatalogModel) error
	// CurrentCatalogPriceRows reads the prices in effect: of one entry, or
	// of every entry when name is empty.
	//
	// CurrentCatalogPriceRows 读取生效中的单价：某个条目的，或 name 为空时全部条目的。
	CurrentCatalogPriceRows(name string) ([]model.CatalogPrice, error)
	// CloseCatalogPrices ends the given price rows at the given moment.
	//
	// CloseCatalogPrices 在给定时刻结束给定的单价行。
	CloseCatalogPrices(ids []int64, at time.Time) error
	InsertCatalogPrices(rows []model.CatalogPrice) error
	// CatalogPendingRows reads every waiting review.
	//
	// CatalogPendingRows 读取全部等待中的审核。
	CatalogPendingRows() ([]model.CatalogPending, error)
	// CatalogPending reads one waiting review. ErrNotFound when there is
	// none.
	//
	// CatalogPending 读取一个等待中的审核；不存在时返回 ErrNotFound。
	CatalogPending(name string) (model.CatalogPending, error)
	// SaveCatalogPending inserts or replaces one waiting review.
	//
	// SaveCatalogPending 插入或替换一个等待中的审核。
	SaveCatalogPending(row model.CatalogPending) error
	DeleteCatalogPending(names []string) error
}

// ApplyCatalogSnapshotRules locks the sync state and, unless snap's content
// was already applied, compares snap with the catalog through
// pricecatalog.Diff and applies the plan: added and automatically applied
// entries take effect at now, changes needing review and removals wait in
// catalog_pending, and the version is incremented when any price took
// effect or stopped.
//
// ApplyCatalogSnapshotRules 锁定同步状态；除非 snap 的内容已经采用过，否则经
// pricecatalog.Diff 比较 snap 与价目表并执行比较结果：新增与自动生效的条目在 now 生效，
// 需要审核的变动与删除在 catalog_pending 中等待；有单价生效或失效时版本号加一。
func ApplyCatalogSnapshotRules(tx CatalogTx, snap pricecatalog.Snapshot, opts CatalogApplyOptions, now time.Time) (CatalogApplyResult, error) {
	state, err := tx.LockCatalogSyncState()
	if err != nil {
		return CatalogApplyResult{}, err
	}
	if state.ContentHash == snap.ContentHash {
		state.CheckedAt = &now
		if opts.Embedded {
			state.EmbeddedHash = snap.ContentHash
		}
		if err := tx.SaveCatalogSyncState(state); err != nil {
			return CatalogApplyResult{}, err
		}
		return CatalogApplyResult{Skipped: true, Version: state.Version}, nil
	}
	models, err := tx.CatalogModels()
	if err != nil {
		return CatalogApplyResult{}, err
	}
	priceRows, err := tx.CurrentCatalogPriceRows("")
	if err != nil {
		return CatalogApplyResult{}, err
	}
	pendingRows, err := tx.CatalogPendingRows()
	if err != nil {
		return CatalogApplyResult{}, err
	}
	rowsByModel := map[string][]model.CatalogPrice{}
	for _, row := range priceRows {
		rowsByModel[row.Model] = append(rowsByModel[row.Model], row)
	}
	pendingByModel := map[string]model.CatalogPending{}
	for _, row := range pendingRows {
		pendingByModel[row.Model] = row
	}
	modelsByName := make(map[string]model.CatalogModel, len(models))
	current := make(map[string]pricecatalog.Current, len(models))
	for _, m := range models {
		modelsByName[m.Model] = m
		pending := pendingByModel[m.Model]
		current[m.Model] = pricecatalog.Current{
			Model: m.Model, Provider: m.Provider, Mode: m.Mode, Raw: []byte(m.RawJSON), Status: m.Status,
			Prices: catalogPrices(rowsByModel[m.Model]), RejectedHash: m.RejectedHash,
			Pending: pending.Kind, PendingHash: pending.PricesHash,
		}
	}
	plan := pricecatalog.Diff(current, snap, pricecatalog.Options{MaxChangePercent: opts.MaxChangePercent})

	result := CatalogApplyResult{Metadata: len(plan.Metadata) + len(plan.Reactivate), Unpended: len(plan.Unpend), Suppressed: plan.Suppressed}
	for _, entry := range plan.Add {
		row := entryModel(entry, modelsByName[entry.Model], now)
		if err := tx.SaveCatalogModel(row); err != nil {
			return CatalogApplyResult{}, err
		}
		if err := replacePrices(tx, entry.Model, rowsByModel[entry.Model], entry.Prices, now); err != nil {
			return CatalogApplyResult{}, err
		}
		result.Added = append(result.Added, entry.Model)
	}
	for _, change := range plan.Apply {
		row := entryModel(change.Entry, modelsByName[change.Entry.Model], now)
		if err := tx.SaveCatalogModel(row); err != nil {
			return CatalogApplyResult{}, err
		}
		if err := replacePrices(tx, change.Entry.Model, rowsByModel[change.Entry.Model], change.Entry.Prices, now); err != nil {
			return CatalogApplyResult{}, err
		}
		result.Applied = append(result.Applied, CatalogChange{Model: change.Entry.Model, MaxChangeBP: change.MaxChangeBP})
	}
	for _, entry := range plan.Metadata {
		row := modelsByName[entry.Model]
		row.Provider, row.Mode, row.RawJSON = entry.Provider, entry.Mode, string(entry.Raw)
		row.Status, row.UpdatedAt = pricecatalog.StatusActive, now
		if err := tx.SaveCatalogModel(row); err != nil {
			return CatalogApplyResult{}, err
		}
	}
	for _, name := range plan.Reactivate {
		row := modelsByName[name]
		row.Status, row.UpdatedAt = pricecatalog.StatusActive, now
		if err := tx.SaveCatalogModel(row); err != nil {
			return CatalogApplyResult{}, err
		}
	}
	for _, change := range plan.Review {
		proposed, err := json.Marshal(change.Entry)
		if err != nil {
			return CatalogApplyResult{}, err
		}
		hash := change.Entry.PricesHash()
		detected := now
		if previous, waiting := pendingByModel[change.Entry.Model]; waiting && previous.Kind == pricecatalog.PendingChange && previous.PricesHash == hash {
			detected = previous.DetectedAt
		}
		if err := tx.SaveCatalogPending(model.CatalogPending{
			Model: change.Entry.Model, Kind: pricecatalog.PendingChange, ProposedJSON: string(proposed),
			PricesHash: hash, MaxChangeBP: change.MaxChangeBP, DetectedAt: detected, UpdatedAt: now,
		}); err != nil {
			return CatalogApplyResult{}, err
		}
		result.Reviewed = append(result.Reviewed, CatalogChange{Model: change.Entry.Model, MaxChangeBP: change.MaxChangeBP})
	}
	for _, name := range plan.Remove {
		row := modelsByName[name]
		row.Status, row.UpdatedAt = pricecatalog.StatusRemovedUpstream, now
		if err := tx.SaveCatalogModel(row); err != nil {
			return CatalogApplyResult{}, err
		}
		if err := tx.SaveCatalogPending(model.CatalogPending{
			Model: name, Kind: pricecatalog.PendingRemove, ProposedJSON: "{}",
			MaxChangeBP: pricecatalog.UnboundedChange, DetectedAt: now, UpdatedAt: now,
		}); err != nil {
			return CatalogApplyResult{}, err
		}
		result.Removed = append(result.Removed, name)
	}
	if len(plan.Unpend) > 0 {
		if err := tx.DeleteCatalogPending(plan.Unpend); err != nil {
			return CatalogApplyResult{}, err
		}
	}
	if len(plan.Add)+len(plan.Apply) > 0 {
		state.Version++
	}
	state.Source, state.ContentHash, state.EntryCount = truncateBytes(opts.Source, 512), snap.ContentHash, int64(len(snap.Entries))
	if opts.Embedded {
		state.EmbeddedHash = snap.ContentHash
	}
	state.SyncedAt, state.CheckedAt = &now, &now
	state.LastError, state.LastErrorAt = "", nil
	if err := tx.SaveCatalogSyncState(state); err != nil {
		return CatalogApplyResult{}, err
	}
	result.Version = state.Version
	return result, nil
}

// ApproveCatalogPendingRules applies the waiting review of name: a price
// change takes effect at now, prices it drops included; a removal ends every
// price of the entry and marks it removed. The review is deleted and the
// version incremented.
//
// ApproveCatalogPendingRules 采用 name 等待中的审核：改价在 now 生效（包括其中被删除的
// 单价）；删除则结束该条目的全部单价并把它标为 removed。审核被删除，版本号加一。
func ApproveCatalogPendingRules(tx CatalogTx, name string, now time.Time) (CatalogDecision, error) {
	state, err := tx.LockCatalogSyncState()
	if err != nil {
		return CatalogDecision{}, err
	}
	pending, err := tx.CatalogPending(name)
	if err != nil {
		return CatalogDecision{}, err
	}
	row, err := tx.CatalogModel(name)
	if err != nil {
		return CatalogDecision{}, err
	}
	before, err := tx.CurrentCatalogPriceRows(name)
	if err != nil {
		return CatalogDecision{}, err
	}
	decision := CatalogDecision{Model: name, Kind: pending.Kind, Before: before}
	switch pending.Kind {
	case pricecatalog.PendingChange:
		var entry pricecatalog.Entry
		if err := json.Unmarshal([]byte(pending.ProposedJSON), &entry); err != nil || entry.Model != name {
			return CatalogDecision{}, errors.New("store: the waiting price change is unreadable")
		}
		if err := tx.SaveCatalogModel(entryModel(entry, row, now)); err != nil {
			return CatalogDecision{}, err
		}
		if err := replacePrices(tx, name, before, entry.Prices, now); err != nil {
			return CatalogDecision{}, err
		}
	case pricecatalog.PendingRemove:
		ids := make([]int64, len(before))
		for i, p := range before {
			ids[i] = p.ID
		}
		if len(ids) > 0 {
			if err := tx.CloseCatalogPrices(ids, now); err != nil {
				return CatalogDecision{}, err
			}
		}
		row.Status, row.RejectedHash, row.UpdatedAt = pricecatalog.StatusRemoved, "", now
		if err := tx.SaveCatalogModel(row); err != nil {
			return CatalogDecision{}, err
		}
	default:
		return CatalogDecision{}, fmt.Errorf("store: unknown catalog review kind %q", pending.Kind)
	}
	if err := tx.DeleteCatalogPending([]string{name}); err != nil {
		return CatalogDecision{}, err
	}
	after, err := tx.CurrentCatalogPriceRows(name)
	if err != nil {
		return CatalogDecision{}, err
	}
	state.Version++
	if err := tx.SaveCatalogSyncState(state); err != nil {
		return CatalogDecision{}, err
	}
	decision.After, decision.Version = after, state.Version
	return decision, nil
}

// RejectCatalogPendingRules discards the waiting review of name. The prices
// in effect stay; a rejected price change is remembered by its hash, so the
// same upstream prices are not raised again, and a rejected removal leaves
// the entry removed upstream with its prices still in effect.
//
// RejectCatalogPendingRules 驳回 name 等待中的审核。生效中的单价保持不变；被拒绝的改价
// 以其哈希记住，相同的上游单价不会再次提出；被拒绝的删除使条目保持「上游已删除」，其单价
// 继续生效。
func RejectCatalogPendingRules(tx CatalogTx, name string, now time.Time) (CatalogDecision, error) {
	state, err := tx.LockCatalogSyncState()
	if err != nil {
		return CatalogDecision{}, err
	}
	pending, err := tx.CatalogPending(name)
	if err != nil {
		return CatalogDecision{}, err
	}
	row, err := tx.CatalogModel(name)
	if err != nil {
		return CatalogDecision{}, err
	}
	if pending.Kind == pricecatalog.PendingChange {
		row.RejectedHash, row.UpdatedAt = pending.PricesHash, now
		if err := tx.SaveCatalogModel(row); err != nil {
			return CatalogDecision{}, err
		}
	}
	if err := tx.DeleteCatalogPending([]string{name}); err != nil {
		return CatalogDecision{}, err
	}
	current, err := tx.CurrentCatalogPriceRows(name)
	if err != nil {
		return CatalogDecision{}, err
	}
	return CatalogDecision{Model: name, Kind: pending.Kind, Before: current, After: current, Version: state.Version}, nil
}

// replacePrices closes the rows of current whose key is gone or whose price
// or mark changed, and inserts rows of entry name for the new and changed
// keys, all at now. Unchanged rows stay as they are, so their ValidFrom
// keeps saying since when they applied.
//
// replacePrices 关闭 current 中键已消失、或单价与标记有变化的行，并为新增与变化的键插入
// 条目 name 的新行，时间都是 now。未变化的行保持原样，使其 ValidFrom 仍然说明它从何时起
// 生效。
func replacePrices(tx CatalogTx, name string, current []model.CatalogPrice, next []pricecatalog.Price, now time.Time) error {
	nextIndex := pricecatalog.Index(next)
	kept := map[pricing.PriceKey]bool{}
	var closing []int64
	for _, row := range current {
		key := pricing.PriceKey{Meter: row.Meter, Variant: row.Variant, AboveInputTokens: row.AboveInputTokens}
		if p, ok := nextIndex[key]; ok && p.PriceMicroPerM == row.PriceMicroPerM && p.Derived == row.Derived {
			kept[key] = true
			continue
		}
		closing = append(closing, row.ID)
	}
	if len(closing) > 0 {
		if err := tx.CloseCatalogPrices(closing, now); err != nil {
			return err
		}
	}
	var inserting []model.CatalogPrice
	for _, p := range next {
		if kept[p.Key()] {
			continue
		}
		inserting = append(inserting, model.CatalogPrice{
			Model: name, Meter: p.Meter, Variant: p.Variant, AboveInputTokens: p.AboveInputTokens,
			PriceMicroPerM: p.PriceMicroPerM, Derived: p.Derived, ValidFrom: now,
		})
	}
	if len(inserting) == 0 {
		return nil
	}
	return tx.InsertCatalogPrices(inserting)
}

// entryModel builds the stored row for an entry taking effect, keeping the
// creation time of a row it replaces.
//
// entryModel 为一个即将生效的条目构造存储行，保留被替换行的创建时间。
func entryModel(entry pricecatalog.Entry, previous model.CatalogModel, now time.Time) model.CatalogModel {
	created := previous.CreatedAt
	if created.IsZero() {
		created = now
	}
	return model.CatalogModel{
		Model: entry.Model, Provider: entry.Provider, Mode: entry.Mode, Status: pricecatalog.StatusActive,
		PricesHash: entry.PricesHash(), RawJSON: string(entry.Raw), CreatedAt: created, UpdatedAt: now,
	}
}

// catalogPrices converts stored rows to pricecatalog prices.
//
// catalogPrices 把存储行转换为 pricecatalog 单价。
func catalogPrices(rows []model.CatalogPrice) []pricecatalog.Price {
	out := make([]pricecatalog.Price, len(rows))
	for i, row := range rows {
		out[i] = pricecatalog.Price{
			PriceItem: pricing.PriceItem{Meter: row.Meter, Variant: row.Variant, AboveInputTokens: row.AboveInputTokens, PriceMicroPerM: row.PriceMicroPerM},
			Derived:   row.Derived,
		}
	}
	return out
}

// SortCatalogPrices orders rows the way pricing.PriceList.Sorted does.
//
// SortCatalogPrices 按 pricing.PriceList.Sorted 的顺序排列行。
func SortCatalogPrices(rows []model.CatalogPrice) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Meter != b.Meter {
			return a.Meter < b.Meter
		}
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		return a.AboveInputTokens < b.AboveInputTokens
	})
}

// catalogCursorPrefix tags a name cursor, so a time cursor from another list
// is refused rather than misread.
//
// catalogCursorPrefix 标记名称游标，使来自其他列表的时间游标被拒绝而不是被误读。
const catalogCursorPrefix = "name|"

// EncodeNameCursor builds the cursor of a list in name order.
//
// EncodeNameCursor 为按名称排序的列表构造游标。
func EncodeNameCursor(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(catalogCursorPrefix + name))
}

// DecodeNameCursor reads a name cursor back; an empty cursor decodes to ""
// (start from the first name).
//
// DecodeNameCursor 读回名称游标；空游标解码为 ""（从第一个名称开始）。
func DecodeNameCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", ErrInvalidCursor
	}
	name, ok := strings.CutPrefix(string(raw), catalogCursorPrefix)
	if !ok || name == "" {
		return "", ErrInvalidCursor
	}
	return name, nil
}

// DecodeProposedEntry decodes a waiting price change's proposal; a removal
// or an unreadable proposal decodes to the zero entry.
//
// DecodeProposedEntry 解码等待中的改价提案；删除或无法读取的提案解码为零值条目。
func DecodeProposedEntry(row model.CatalogPending) pricecatalog.Entry {
	var entry pricecatalog.Entry
	if row.Kind != pricecatalog.PendingChange || json.Unmarshal([]byte(row.ProposedJSON), &entry) != nil {
		return pricecatalog.Entry{}
	}
	return entry
}

// truncateBytes shortens s to at most n bytes on a rune boundary.
//
// truncateBytes 在字符边界上把 s 截短到不超过 n 字节。
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
