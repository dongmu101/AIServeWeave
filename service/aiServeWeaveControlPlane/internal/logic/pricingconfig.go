// pricingconfig.go implements the operator's pricing configuration: the
// default price, each alias's catalog reference, override price and model
// rate, the price plans, and which plan each tenant belongs to. Every change
// appends a whole revision based on the revision the operator saw, so two
// operators editing at once cannot overwrite each other. See
// .scratch/token-pricing/spec.md「定价与价格方案」.
//
// pricingconfig.go 实现运维的定价配置：默认价，各别名的参照模型、覆盖价与模型倍率，
// 价格方案，以及每个租户所属的方案。每次修改都基于运维看到的那个版本追加一个完整版本，
// 使同时编辑的两位运维无法互相覆盖。见 .scratch/token-pricing/spec.md「定价与价格方案」。
package logic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricingconfig"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// PricingConfigView is one revision with its parsed document.
//
// PricingConfigView 是一个版本及其解析后的文档。
type PricingConfigView struct {
	Revision model.PricingRevision
	Document pricingconfig.Document
}

// PricingModelInput is an operator's settings for one alias. They replace
// the alias's settings wholesale. A nil RatePPM is pricing.RateOne; empty
// Prices means no override; an empty CatalogModel means the alias's own
// name.
//
// PricingModelInput 是运维为一个别名填写的设置，整体替换该别名原有的设置。RatePPM 为
// nil 表示 pricing.RateOne；Prices 为空表示没有覆盖价；CatalogModel 为空表示用别名本身。
type PricingModelInput struct {
	Model        string
	CatalogModel string
	Prices       pricing.PriceList
	RatePPM      *int64
	BaseRevision int64
}

// PricePlanInput is an operator's plan definition. On update a nil
// DefaultRatePPM or Models keeps the current value; on create they default
// to pricing.RateOne and no per-alias terms.
//
// PricePlanInput 是运维的方案定义。更新时 DefaultRatePPM 或 Models 为 nil 表示保持原值；
// 新建时它们分别默认为 pricing.RateOne 与没有按别名的条款。
type PricePlanInput struct {
	Name           string
	DefaultRatePPM *int64
	Models         map[string]pricingconfig.PlanModel
	BaseRevision   int64
}

// PricePlanView is one plan for the operator. TenantCount is how many
// tenants are assigned to it; it is nil for the default plan, which every
// unassigned tenant belongs to.
//
// PricePlanView 是供运维查看的一个方案。TenantCount 是分配到它的租户数；默认方案为 nil，
// 因为所有未分配的租户都属于它。
type PricePlanView struct {
	ID          string
	Plan        pricingconfig.Plan
	Default     bool
	TenantCount *int64
}

// PricePlansView is every plan as of one revision.
//
// PricePlansView 是某个版本下的全部方案。
type PricePlansView struct {
	Revision model.PricingRevision
	Plans    []PricePlanView
}

// PricePlanResult is a saved plan and the revision that saved it.
//
// PricePlanResult 是保存后的方案及保存它的版本。
type PricePlanResult struct {
	Plan     PricePlanView
	Revision model.PricingRevision
}

// maxPlanAssignAttempts bounds how often a plan assignment re-reads the
// configuration after another write moved it on.
//
// maxPlanAssignAttempts 限制在其他写入推进了配置之后，方案分配重新读取配置的次数。
const maxPlanAssignAttempts = 3

// EnsurePricingConfig returns the latest revision, writing the first one
// from the legacy model_prices table when there is none. Replicas racing to
// do so agree on one first revision. It checks no actor: it is the entry
// point charging uses too.
//
// EnsurePricingConfig 返回最新的版本；还没有任何版本时，先由旧的 model_prices 表写入
// 第一个版本。同时这样做的多个副本会对唯一的第一个版本达成一致。它不检查 actor：扣费也
// 从这里进入。
func (s *Service) EnsurePricingConfig(ctx context.Context) (PricingConfigView, error) {
	rev, err := s.store.LatestPricingRevision(ctx)
	if errors.Is(err, store.ErrNotFound) {
		rev, err = s.bootstrapPricingConfig(ctx)
	}
	if err != nil {
		return PricingConfigView{}, translate(err)
	}
	return parseRevision(rev)
}

// PricingConfigAt returns the revision in effect at the given moment, the
// first one for a moment before it. Charging prices a request by the
// revision in effect when the request was made.
//
// PricingConfigAt 返回在给定时刻生效的版本；时刻早于第一个版本时返回第一个版本。扣费按
// 请求发生时生效的版本为请求计价。
func (s *Service) PricingConfigAt(ctx context.Context, at time.Time) (PricingConfigView, error) {
	if _, err := s.EnsurePricingConfig(ctx); err != nil {
		return PricingConfigView{}, err
	}
	rev, err := s.store.PricingRevisionAt(ctx, at)
	if err != nil {
		return PricingConfigView{}, translate(err)
	}
	return parseRevision(rev)
}

// bootstrapPricingConfig converts model_prices into the first revision.
//
// bootstrapPricingConfig 把 model_prices 转换为第一个版本。
func (s *Service) bootstrapPricingConfig(ctx context.Context) (model.PricingRevision, error) {
	rows, err := s.store.ListModelPrices(ctx)
	if err != nil {
		return model.PricingRevision{}, err
	}
	doc, skipped := pricingconfig.FromModelPrices(rows)
	raw, err := pricingconfig.Encode(doc)
	if err != nil {
		return model.PricingRevision{}, err
	}
	summary := fmt.Sprintf("initial revision from model_prices: %d models", len(doc.Models))
	if len(doc.DefaultPrices) > 0 {
		summary += ", default price"
	}
	if skipped > 0 {
		summary += fmt.Sprintf(", %d invalid rows skipped", skipped)
	}
	now := s.clock.Now()
	rev, _, err := s.store.CreateInitialPricingRevision(ctx, model.PricingRevision{
		EffectiveAt: now, Document: raw, CreatedBy: model.PricingRevisionSystemActor, Summary: summary, CreatedAt: now,
	})
	return rev, err
}

// parseRevision parses a stored revision. A document this build cannot read
// is an internal error, not the caller's.
//
// parseRevision 解析一个已保存的版本。本程序读不懂的文档是内部错误，不是调用方的错。
func parseRevision(rev model.PricingRevision) (PricingConfigView, error) {
	doc, err := pricingconfig.Parse(rev.Document)
	if err != nil {
		return PricingConfigView{}, fmt.Errorf("pricing revision %d: %w", rev.ID, err)
	}
	rev.Document = ""
	return PricingConfigView{Revision: rev, Document: doc}, nil
}

// GetPricingConfig returns the current configuration, for the operator.
//
// GetPricingConfig 返回当前配置，供运维使用。
func (s *Service) GetPricingConfig(ctx context.Context, actor Actor) (PricingConfigView, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return PricingConfigView{}, err
	}
	return s.EnsurePricingConfig(ctx)
}

// PutPricingModel replaces one alias's settings.
//
// PutPricingModel 替换一个别名的设置。
func (s *Service) PutPricingModel(ctx context.Context, actor Actor, in PricingModelInput) (PricingConfigView, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return PricingConfigView{}, err
	}
	alias := strings.TrimSpace(in.Model)
	cfg := pricingconfig.ModelConfig{CatalogModel: strings.TrimSpace(in.CatalogModel), Prices: in.Prices, RatePPM: pricing.RateOne}
	if in.RatePPM != nil {
		cfg.RatePPM = *in.RatePPM
	}
	if err := s.checkCatalogReference(ctx, cfg.CatalogModel); err != nil {
		return PricingConfigView{}, err
	}
	view, err := s.appendPricing(ctx, actor, in.BaseRevision, nil, func(doc pricingconfig.Document) (pricingconfig.Document, string, error) {
		next, err := doc.SetModel(alias, cfg)
		return next, "set model " + alias, err
	})
	if err != nil {
		return PricingConfigView{}, err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPricingModelSet, truncate(alias, 64),
		fmt.Sprintf("revision=%d catalog_model=%s override_prices=%d rate_ppm=%d", view.Revision.ID, truncate(cfg.CatalogModel, 64), len(cfg.Prices), cfg.RatePPM), actor.IP)
	return view, nil
}

// checkCatalogReference refuses a reference model the catalog does not
// price: an unknown entry, or one whose removal an operator approved. An
// entry removed upstream but still awaiting review keeps its prices, so it
// may still be referenced. An empty name references nothing.
//
// checkCatalogReference 拒绝价目表不为之定价的参照模型：不存在的条目，或运维已批准
// 删除的条目。上游已删除、但仍在等待审核的条目保留原价，因此仍可被参照。名称为空表示
// 不参照任何条目。
func (s *Service) checkCatalogReference(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	entry, err := s.store.GetCatalogEntry(ctx, name)
	if errors.Is(err, store.ErrNotFound) || (err == nil && entry.Model.Status == pricecatalog.StatusRemoved) {
		return fmt.Errorf("%w: the catalog has no entry %q", ErrInvalidInput, name)
	}
	return err
}

// DeletePricingModel removes one alias's settings: it is then priced by its
// own name in the catalog at a 1x model rate.
//
// DeletePricingModel 删除一个别名的设置：此后它按自身名称在价目表中定价，模型倍率为 1。
func (s *Service) DeletePricingModel(ctx context.Context, actor Actor, alias string, baseRevision int64) (PricingConfigView, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return PricingConfigView{}, err
	}
	alias = strings.TrimSpace(alias)
	view, err := s.appendPricing(ctx, actor, baseRevision, nil, func(doc pricingconfig.Document) (pricingconfig.Document, string, error) {
		next, err := doc.DeleteModel(alias)
		return next, "delete model " + alias, err
	})
	if err != nil {
		return PricingConfigView{}, err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPricingModelDelete, truncate(alias, 64),
		fmt.Sprintf("revision=%d", view.Revision.ID), actor.IP)
	return view, nil
}

// PutDefaultPrices replaces the default price; an empty list removes it.
//
// PutDefaultPrices 替换默认价；空表表示去掉默认价。
func (s *Service) PutDefaultPrices(ctx context.Context, actor Actor, prices pricing.PriceList, baseRevision int64) (PricingConfigView, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return PricingConfigView{}, err
	}
	view, err := s.appendPricing(ctx, actor, baseRevision, nil, func(doc pricingconfig.Document) (pricingconfig.Document, string, error) {
		summary := "set default prices"
		if len(prices) == 0 {
			summary = "clear default prices"
		}
		next, err := doc.SetDefaultPrices(prices)
		return next, summary, err
	})
	if err != nil {
		return PricingConfigView{}, err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPricingDefaultSet, "default",
		fmt.Sprintf("revision=%d prices=%d", view.Revision.ID, len(prices)), actor.IP)
	return view, nil
}

// ListPricePlans returns every plan with how many tenants each holds.
//
// ListPricePlans 返回全部方案及各自的租户数。
func (s *Service) ListPricePlans(ctx context.Context, actor Actor) (PricePlansView, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return PricePlansView{}, err
	}
	current, err := s.EnsurePricingConfig(ctx)
	if err != nil {
		return PricePlansView{}, err
	}
	counts, err := s.store.CountTenantPricePlans(ctx)
	if err != nil {
		return PricePlansView{}, translate(err)
	}
	out := PricePlansView{Revision: current.Revision, Plans: make([]PricePlanView, 0, len(current.Document.Plans))}
	for _, id := range current.Document.SortedPlanIDs() {
		view := planView(current.Document, id)
		if !view.Default {
			n := counts[id]
			view.TenantCount = &n
		}
		out.Plans = append(out.Plans, view)
	}
	return out, nil
}

// SavePricePlan creates a plan (create) or replaces plan id.
//
// SavePricePlan 新建一个方案（create），或替换方案 id。
func (s *Service) SavePricePlan(ctx context.Context, actor Actor, id string, in PricePlanInput, create bool) (PricePlanResult, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return PricePlanResult{}, err
	}
	if create {
		id = model.NewID(model.PrefixPricePlan)
	}
	name := strings.TrimSpace(in.Name)
	view, err := s.appendPricing(ctx, actor, in.BaseRevision, nil, func(doc pricingconfig.Document) (pricingconfig.Document, string, error) {
		plan := pricingconfig.Plan{DefaultRatePPM: pricing.RateOne, Models: map[string]pricingconfig.PlanModel{}}
		summary := "create plan " + id
		if !create {
			current, ok := doc.Plans[id]
			if !ok {
				return pricingconfig.Document{}, "", pricingconfig.ErrNotFound
			}
			plan, summary = current, "update plan "+id
		}
		plan.Name = name
		if in.DefaultRatePPM != nil {
			plan.DefaultRatePPM = *in.DefaultRatePPM
		}
		if in.Models != nil {
			plan.Models = in.Models
		}
		next, err := doc.PutPlan(id, plan)
		return next, summary, err
	})
	if err != nil {
		return PricePlanResult{}, err
	}
	result := PricePlanResult{Plan: planView(view.Document, id), Revision: view.Revision}
	action := model.ActionBillingPlanUpdate
	if create {
		action = model.ActionBillingPlanCreate
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, action, id,
		fmt.Sprintf("revision=%d default_rate_ppm=%d models=%d", view.Revision.ID, result.Plan.Plan.DefaultRatePPM, len(result.Plan.Plan.Models)), actor.IP)
	return result, nil
}

// DeletePricePlan removes a plan no tenant belongs to. The default plan and
// a plan still in use are conflicts.
//
// DeletePricePlan 删除一个没有租户所属的方案。默认方案与仍在使用的方案都是冲突。
func (s *Service) DeletePricePlan(ctx context.Context, actor Actor, id string, baseRevision int64) (model.PricingRevision, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.PricingRevision{}, err
	}
	view, err := s.appendPricing(ctx, actor, baseRevision, []string{id}, func(doc pricingconfig.Document) (pricingconfig.Document, string, error) {
		next, err := doc.DeletePlan(id)
		return next, "delete plan " + id, err
	})
	if err != nil {
		return model.PricingRevision{}, err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPlanDelete, id,
		fmt.Sprintf("revision=%d", view.Revision.ID), actor.IP)
	return view.Revision, nil
}

// SetTenantPricePlan moves a tenant to planID from now on; an empty id or
// pricingconfig.DefaultPlanID returns it to the default plan. An unknown
// plan is invalid input. Charges already made keep the plan they were made
// under.
//
// SetTenantPricePlan 让租户从现在起改属 planID；id 为空或为 pricingconfig.DefaultPlanID
// 时回到默认方案。未知方案属于无效输入。已经发生的扣费保持当时所属的方案。
func (s *Service) SetTenantPricePlan(ctx context.Context, actor Actor, tenantID, planID string) error {
	if err := s.requireBillingOperator(actor); err != nil {
		return err
	}
	if _, err := s.store.GetTenant(ctx, tenantID); err != nil {
		return translate(err)
	}
	planID = strings.TrimSpace(planID)
	if planID == pricingconfig.DefaultPlanID {
		planID = ""
	}
	for attempt := 0; attempt < maxPlanAssignAttempts; attempt++ {
		current, err := s.EnsurePricingConfig(ctx)
		if err != nil {
			return err
		}
		if _, ok := current.Document.Plans[planID]; planID != "" && !ok {
			return ErrInvalidInput
		}
		changed, err := s.store.SetTenantPricePlan(ctx, store.SetTenantPricePlan{
			TenantID: tenantID, PlanID: planID, ExpectRevisionID: current.Revision.ID,
			CreatedBy: actor.UserID, Now: s.clock.Now(),
		})
		if errors.Is(err, store.ErrPricingRevisionChanged) {
			continue
		}
		if err != nil {
			return translate(err)
		}
		if changed {
			shown := planID
			if shown == "" {
				shown = pricingconfig.DefaultPlanID
			}
			s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingTenantPlan, tenantID, "plan="+shown, actor.IP)
		}
		return nil
	}
	return ErrConflict
}

// ListPricingRevisions pages through the configuration's history, newest
// first.
//
// ListPricingRevisions 从新到旧分页读取配置的历史。
func (s *Service) ListPricingRevisions(ctx context.Context, actor Actor, query store.ListQuery) (store.Page[model.PricingRevision], error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return store.Page[model.PricingRevision]{}, err
	}
	if _, err := s.EnsurePricingConfig(ctx); err != nil {
		return store.Page[model.PricingRevision]{}, err
	}
	page, err := s.store.ListPricingRevisions(ctx, query)
	return page, translate(err)
}

// tenantPricePlans returns each tenant's current plan id, the default plan
// for tenants without an assignment.
//
// tenantPricePlans 返回每个租户当前所属的方案 id；没有分配的租户为默认方案。
func (s *Service) tenantPricePlans(ctx context.Context, tenantIDs []string) (map[string]string, error) {
	plans, err := s.store.TenantPricePlansAt(ctx, tenantIDs, s.clock.Now())
	if err != nil {
		return nil, translate(err)
	}
	for _, id := range tenantIDs {
		if plans[id] == "" {
			plans[id] = pricingconfig.DefaultPlanID
		}
	}
	return plans, nil
}

// appendPricing applies edit to the latest revision and appends the result.
// baseRevision must be that latest revision; a stale one is a conflict, so
// an operator never overwrites a change they have not seen. unusedPlans are
// plan ids the store must find unassigned under its lock.
//
// appendPricing 把 edit 应用到最新版本上，并追加结果。baseRevision 必须就是那个最新
// 版本；过期的版本号属于冲突，使运维绝不会覆盖自己没看到的修改。unusedPlans 是 store 必须
// 在锁内确认无人分配的方案 id。
func (s *Service) appendPricing(ctx context.Context, actor Actor, baseRevision int64, unusedPlans []string,
	edit func(pricingconfig.Document) (pricingconfig.Document, string, error)) (PricingConfigView, error) {
	if baseRevision <= 0 {
		return PricingConfigView{}, ErrInvalidInput
	}
	current, err := s.EnsurePricingConfig(ctx)
	if err != nil {
		return PricingConfigView{}, err
	}
	if current.Revision.ID != baseRevision {
		return PricingConfigView{}, ErrConflict
	}
	next, summary, err := edit(current.Document)
	if err != nil {
		return PricingConfigView{}, translatePricingConfig(err)
	}
	raw, err := pricingconfig.Encode(next)
	if err != nil {
		return PricingConfigView{}, translatePricingConfig(err)
	}
	rev, err := s.store.AppendPricingRevision(ctx, store.AppendPricingRevision{
		BaseRevisionID: baseRevision, Document: raw, CreatedBy: actor.UserID,
		Summary: truncate(summary, 512), Now: s.clock.Now(), UnusedPlans: unusedPlans,
	})
	if err != nil {
		return PricingConfigView{}, translate(err)
	}
	return parseRevision(rev)
}

// translatePricingConfig maps the document's edit errors onto logic errors.
//
// translatePricingConfig 把文档修改的错误映射为 logic 错误。
func translatePricingConfig(err error) error {
	switch {
	case errors.Is(err, pricingconfig.ErrInvalid):
		return ErrInvalidInput
	case errors.Is(err, pricingconfig.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, pricingconfig.ErrDefaultPlan):
		return ErrConflict
	default:
		return err
	}
}

func planView(doc pricingconfig.Document, id string) PricePlanView {
	return PricePlanView{ID: id, Plan: doc.Plans[id], Default: id == doc.DefaultPlan}
}
