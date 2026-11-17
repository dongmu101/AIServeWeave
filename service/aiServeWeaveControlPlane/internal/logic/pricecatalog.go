// pricecatalog.go keeps the official price list in sync: the built-in
// snapshot imported when the catalog is empty, the periodic and manual
// fetches of the upstream list, and the operator's review of the changes
// that do not take effect on their own. See
// .scratch/token-pricing/spec.md「价目表」.
//
// pricecatalog.go 让官方价目表保持同步：价目表为空时导入内置快照、定时与手动拉取上游
// 价目表，以及运维对不能自动生效的变动的审核。见 .scratch/token-pricing/spec.md「价目表」。
package logic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// PriceCatalogConfig configures the price list sync.
//
// PriceCatalogConfig 配置价目表同步。
type PriceCatalogConfig struct {
	// Remote fetches the upstream list, for the periodic and manual syncs.
	// Remote 拉取上游价目表，供定时与手动同步使用。
	Remote pricecatalog.Source
	// Embedded is the built-in snapshot (pricecatalog.Embedded()).
	// Embedded 是内置快照（pricecatalog.Embedded()）。
	Embedded []byte
	// RefreshInterval is how often Remote is fetched; zero turns the
	// periodic fetch off, and the built-in snapshot of a newer binary is
	// then applied at start through the same review rules.
	//
	// RefreshInterval 是拉取 Remote 的间隔；为零时关闭定时拉取，此时较新二进制的内置快照
	// 会在启动时经同一套审核规则采用。
	RefreshInterval time.Duration
	// MaxChangePercent is the largest relative change of an existing price
	// that takes effect without review.
	// MaxChangePercent 是已有单价无需审核即可生效的最大相对变动。
	MaxChangePercent int
}

// WithPriceCatalog turns the price list sync on. It takes effect only with
// billing on.
//
// WithPriceCatalog 开启价目表同步。只有在计费开启时才生效。
func WithPriceCatalog(cfg PriceCatalogConfig) Option {
	return func(s *Service) {
		c := cfg
		s.priceCatalog = &c
	}
}

// priceCatalogRetryInterval is how soon a failed import of the built-in
// snapshot is retried when there is no periodic fetch to retry it with.
//
// priceCatalogRetryInterval 是没有定时拉取可以顺带重试时，内置快照导入失败后的重试间隔。
const priceCatalogRetryInterval = 10 * time.Minute

// maxCatalogChangeAudits bounds the per-entry audit records of one sync;
// the summary record still counts every change.
//
// maxCatalogChangeAudits 限制一次同步按条目写入的审计记录数；摘要记录仍会统计全部变动。
const maxCatalogChangeAudits = 200

// priceCatalogEnabled reports whether the price list sync is on.
//
// priceCatalogEnabled 报告价目表同步是否开启。
func (s *Service) priceCatalogEnabled() bool {
	return s.billingEnabled() && s.priceCatalog != nil
}

// requireCatalogOperator gates the operator's catalog endpoints.
//
// requireCatalogOperator 守卫运维的价目表端点。
func (s *Service) requireCatalogOperator(actor Actor) error {
	if !s.priceCatalogEnabled() {
		return ErrNotFound
	}
	return s.requirePlatformActor(actor)
}

// InitPriceCatalog applies the built-in snapshot when the catalog is empty,
// or, with the periodic fetch off, when the binary carries a snapshot the
// catalog has not seen; otherwise it does nothing. The first import takes
// effect whole; later ones go through the review rules.
//
// InitPriceCatalog 在价目表为空时，或者在定时拉取关闭、且二进制携带的快照尚未被价目表
// 采用过时，采用内置快照；否则什么也不做。首次导入整体立即生效；之后的导入经过审核规则。
func (s *Service) InitPriceCatalog(ctx context.Context) error {
	if !s.priceCatalogEnabled() {
		return nil
	}
	state, err := s.store.CatalogSyncState(ctx)
	if err != nil {
		return err
	}
	snap, err := pricecatalog.Parse(s.priceCatalog.Embedded)
	if err != nil {
		return fmt.Errorf("parsing the built-in price catalog: %w", err)
	}
	first := state.ContentHash == ""
	if !first && (s.priceCatalog.RefreshInterval > 0 || state.EmbeddedHash == snap.ContentHash) {
		return nil
	}
	_, err = s.applyCatalog(ctx, nil, pricecatalog.EmbeddedLabel, true, snap)
	return err
}

// SyncPriceCatalog fetches the upstream list once and applies it, for an
// operator (actor non-nil) or the periodic loop. A fetch or parse failure is
// recorded on the sync state and returned as ErrUnavailable; the catalog is
// left as it was.
//
// SyncPriceCatalog 拉取一次上游价目表并采用它，供运维（actor 非 nil）或定时循环使用。
// 拉取或解析失败会记录在同步状态上，并以 ErrUnavailable 返回；价目表保持不变。
func (s *Service) SyncPriceCatalog(ctx context.Context, actor *Actor) (store.CatalogApplyResult, error) {
	if actor != nil {
		if err := s.requireCatalogOperator(*actor); err != nil {
			return store.CatalogApplyResult{}, err
		}
	} else if !s.priceCatalogEnabled() {
		return store.CatalogApplyResult{}, ErrNotFound
	}
	remote := s.priceCatalog.Remote
	if remote == nil {
		return store.CatalogApplyResult{}, ErrNotFound
	}
	data, err := remote.Fetch(ctx)
	var snap pricecatalog.Snapshot
	if err == nil {
		snap, err = pricecatalog.Parse(data)
	}
	if err != nil {
		if recordErr := s.store.RecordCatalogSyncError(ctx, remote.Label()+": "+err.Error(), s.clock.Now()); recordErr != nil {
			slog.Warn("recording a price catalog sync failure failed", slog.Any("error", recordErr))
		}
		return store.CatalogApplyResult{}, errors.Join(ErrUnavailable, err)
	}
	return s.applyCatalog(ctx, actor, remote.Label(), false, snap)
}

// applyCatalog applies snap and audits what took effect or now waits.
//
// applyCatalog 采用 snap，并审计已生效与正在等待的内容。
func (s *Service) applyCatalog(ctx context.Context, actor *Actor, source string, embedded bool, snap pricecatalog.Snapshot) (store.CatalogApplyResult, error) {
	result, err := s.store.ApplyCatalogSnapshot(ctx, snap, store.CatalogApplyOptions{
		Source: source, Embedded: embedded, MaxChangePercent: s.priceCatalog.MaxChangePercent,
	}, s.clock.Now())
	if err != nil {
		return store.CatalogApplyResult{}, translate(err)
	}
	if result.Skipped {
		return result, nil
	}
	actorID, ip := "", ""
	if actor != nil {
		actorID, ip = actor.UserID, actor.IP
	}
	s.audit(ctx, model.PlatformScope, actorID, model.ActionBillingCatalogSync, truncate(source, 64),
		fmt.Sprintf("added=%d applied=%d review=%d removed=%d suppressed=%d version=%d",
			len(result.Added), len(result.Applied), len(result.Reviewed), len(result.Removed), result.Suppressed, result.Version), ip)
	for i, change := range result.Applied {
		if i == maxCatalogChangeAudits {
			break
		}
		s.audit(ctx, model.PlatformScope, actorID, model.ActionBillingCatalogAutoApply, truncate(change.Model, 64),
			truncate(fmt.Sprintf("max_change=%s version=%d model=%s", formatChangeBP(change.MaxChangeBP), result.Version, change.Model), 512), ip)
	}
	return result, nil
}

// RunPriceCatalogSync imports the built-in snapshot if needed, then fetches
// the upstream list at once and every RefreshInterval until ctx ends. With
// the periodic fetch off it only retries a failed import.
//
// RunPriceCatalogSync 按需导入内置快照，然后立即拉取一次上游价目表，并每隔
// RefreshInterval 再拉取，直到 ctx 结束。定时拉取关闭时，它只重试失败的导入。
func (s *Service) RunPriceCatalogSync(ctx context.Context) {
	if !s.priceCatalogEnabled() {
		return
	}
	ready := s.initPriceCatalogLogged(ctx)
	periodic := s.priceCatalog.Remote != nil && s.priceCatalog.RefreshInterval > 0
	if periodic {
		s.syncPriceCatalogLogged(ctx)
	}
	for {
		interval := s.priceCatalog.RefreshInterval
		if !periodic {
			if ready {
				return
			}
			interval = priceCatalogRetryInterval
		}
		fired, stop := s.clock.NewTimer(interval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-fired:
		}
		if !ready {
			ready = s.initPriceCatalogLogged(ctx)
		}
		if periodic {
			s.syncPriceCatalogLogged(ctx)
		}
	}
}

// initPriceCatalogLogged runs InitPriceCatalog and logs a failure.
//
// initPriceCatalogLogged 执行 InitPriceCatalog 并记录失败。
func (s *Service) initPriceCatalogLogged(ctx context.Context) bool {
	if err := s.InitPriceCatalog(ctx); err != nil {
		slog.Error("importing the built-in price catalog failed; it will be retried", slog.Any("error", err))
		return false
	}
	return true
}

// syncPriceCatalogLogged runs one background sync and logs its outcome.
//
// syncPriceCatalogLogged 执行一次后台同步并记录结果。
func (s *Service) syncPriceCatalogLogged(ctx context.Context) {
	result, err := s.SyncPriceCatalog(ctx, nil)
	switch {
	case err != nil:
		slog.Warn("price catalog sync failed; the catalog is unchanged", slog.Any("error", err))
	case !result.Skipped:
		slog.Info("price catalog synced", slog.Int("added", len(result.Added)), slog.Int("applied", len(result.Applied)),
			slog.Int("review", len(result.Reviewed)), slog.Int("removed", len(result.Removed)), slog.Int64("version", result.Version))
	}
}

// ListPriceCatalog returns a page of catalog entries and the sync state,
// for the operator.
//
// ListPriceCatalog 返回一页价目表条目与同步状态，供运维使用。
func (s *Service) ListPriceCatalog(ctx context.Context, actor Actor, query store.ListQuery, filter store.CatalogFilter) (store.Page[store.CatalogEntry], model.CatalogSyncState, error) {
	if err := s.requireCatalogOperator(actor); err != nil {
		return store.Page[store.CatalogEntry]{}, model.CatalogSyncState{}, err
	}
	filter.Prefix = strings.TrimSpace(filter.Prefix)
	if len(filter.Prefix) > pricecatalog.MaxModelLen {
		return store.Page[store.CatalogEntry]{}, model.CatalogSyncState{}, ErrInvalidInput
	}
	switch filter.Status {
	case "", pricecatalog.StatusActive, pricecatalog.StatusRemovedUpstream, pricecatalog.StatusRemoved:
	default:
		return store.Page[store.CatalogEntry]{}, model.CatalogSyncState{}, ErrInvalidInput
	}
	page, err := s.store.ListCatalogEntries(ctx, query, filter)
	if err != nil {
		return store.Page[store.CatalogEntry]{}, model.CatalogSyncState{}, translate(err)
	}
	state, err := s.store.CatalogSyncState(ctx)
	if err != nil {
		return store.Page[store.CatalogEntry]{}, model.CatalogSyncState{}, translate(err)
	}
	return page, state, nil
}

// ListCatalogPending returns a page of the changes waiting for review.
//
// ListCatalogPending 返回一页等待审核的变动。
func (s *Service) ListCatalogPending(ctx context.Context, actor Actor, query store.ListQuery) (store.Page[store.CatalogPendingEntry], error) {
	if err := s.requireCatalogOperator(actor); err != nil {
		return store.Page[store.CatalogPendingEntry]{}, err
	}
	page, err := s.store.ListCatalogPending(ctx, query)
	return page, translate(err)
}

// ApproveCatalogPending applies the change waiting on one entry: a price
// change takes effect now, a removal ends the entry's prices.
//
// ApproveCatalogPending 采用某个条目上等待中的变动：改价立即生效，删除则结束该条目的单价。
func (s *Service) ApproveCatalogPending(ctx context.Context, actor Actor, name string) (store.CatalogDecision, error) {
	if err := s.requireCatalogOperator(actor); err != nil {
		return store.CatalogDecision{}, err
	}
	if name == "" || len(name) > pricecatalog.MaxModelLen {
		return store.CatalogDecision{}, ErrInvalidInput
	}
	decision, err := s.store.ApproveCatalogPending(ctx, name, s.clock.Now())
	if err != nil {
		return store.CatalogDecision{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingCatalogApprove, truncate(name, 64),
		truncate(fmt.Sprintf("kind=%s %s version=%d model=%s", decision.Kind, describeCatalogChange(decision), decision.Version, name), 512), actor.IP)
	return decision, nil
}

// RejectCatalogPending discards the change waiting on one entry; its prices
// stay, and the same upstream prices are not raised again.
//
// RejectCatalogPending 驳回某个条目上等待中的变动；其单价保持不变，相同的上游单价不会再次
// 提出。
func (s *Service) RejectCatalogPending(ctx context.Context, actor Actor, name string) (store.CatalogDecision, error) {
	if err := s.requireCatalogOperator(actor); err != nil {
		return store.CatalogDecision{}, err
	}
	if name == "" || len(name) > pricecatalog.MaxModelLen {
		return store.CatalogDecision{}, ErrInvalidInput
	}
	decision, err := s.store.RejectCatalogPending(ctx, name, s.clock.Now())
	if err != nil {
		return store.CatalogDecision{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingCatalogReject, truncate(name, 64),
		truncate(fmt.Sprintf("kind=%s model=%s", decision.Kind, name), 512), actor.IP)
	return decision, nil
}

// describeCatalogChange summarizes the base input and output prices before
// and after a decision, for the audit detail.
//
// describeCatalogChange 概括一次决定前后的基础输入价与输出价，用于审计详情。
func describeCatalogChange(d store.CatalogDecision) string {
	price := func(rows []model.CatalogPrice, meter string) string {
		for _, row := range rows {
			if row.Meter == meter && row.Variant == "" && row.AboveInputTokens == 0 {
				return fmt.Sprint(row.PriceMicroPerM)
			}
		}
		return "-"
	}
	return fmt.Sprintf("input=%s→%s output=%s→%s", price(d.Before, "input"), price(d.After, "input"),
		price(d.Before, "output"), price(d.After, "output"))
}

// formatChangeBP renders a change in basis points as a percentage.
//
// formatChangeBP 把以基点表示的变动渲染为百分比。
func formatChangeBP(bp int64) string {
	if bp == pricecatalog.UnboundedChange {
		return "unbounded"
	}
	return fmt.Sprintf("%d.%02d%%", bp/100, bp%100)
}
