package logic

import (
	"context"
	"time"
)

// PlanPrices is one price plan's public face: its name and the routed
// aliases' sale prices under it.
//
// PlanPrices 是一个价格方案的公开内容：名称，以及路由别名在该方案下的售价。
type PlanPrices struct {
	ID      string
	Name    string
	Default bool
	Prices  []EffectivePrice
}

// PublicPlanPrices is every plan's sale prices at one moment. UpdatedAt is
// the later of when the pricing revision in force took effect and when the
// official catalog last changed: the last moment a sale price could have
// moved. It is zero when neither is known.
//
// PublicPlanPrices 是某一时刻全部方案的售价。UpdatedAt 取生效中定价版本的生效时间与
// 官方价目表最后一次变化时间中较晚者，即售价最后一次可能变化的时刻；两者都未知时为零值。
type PublicPlanPrices struct {
	UpdatedAt time.Time
	Plans     []PlanPrices
}

// PublicPlanPrices lists, for anyone without signing in, every price plan
// with the routed aliases priced under it as of now, the default plan first
// and the rest by id. It reads the stored plans as they are: a plan is shown
// under its own name with the prices charging would use. Aliases nothing
// prices are left out. It exists only when payment billing is enabled.
//
// PublicPlanPrices 供任何人无需登录读取：列出全部价格方案及路由别名在各方案下当前的
// 售价，默认方案在前，其余按 id 排序。它按原样读取已保存的方案：每个方案以自己的名称
// 展示扣费时会用的价格。没有任何单价的别名不列出。仅在开启支付计费时存在。
func (s *Service) PublicPlanPrices(ctx context.Context) (PublicPlanPrices, error) {
	if !s.billingEnabled() {
		return PublicPlanPrices{}, ErrNotFound
	}
	snap, err := s.pricingSnapshot(ctx)
	if err != nil {
		return PublicPlanPrices{}, translate(err)
	}
	routed, err := s.routedAliases(ctx)
	if err != nil {
		return PublicPlanPrices{}, err
	}
	doc := snap.config.Document
	now := s.clock.Now()
	out := PublicPlanPrices{Plans: []PlanPrices{}}
	for _, id := range doc.SortedPlanIDs() {
		prices, err := s.publicPrices(ctx, snap, routed, id, now)
		if err != nil {
			return PublicPlanPrices{}, err
		}
		out.Plans = append(out.Plans, PlanPrices{ID: id, Name: doc.Plans[id].Name, Default: id == doc.DefaultPlan, Prices: prices})
	}
	out.UpdatedAt = snap.config.Revision.EffectiveAt
	if snap.catalogChangedAt.After(out.UpdatedAt) {
		out.UpdatedAt = snap.catalogChangedAt
	}
	if !out.UpdatedAt.IsZero() {
		out.UpdatedAt = out.UpdatedAt.UTC()
	}
	return out, nil
}
