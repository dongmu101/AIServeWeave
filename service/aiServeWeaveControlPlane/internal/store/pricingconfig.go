package store

import (
	"context"
	"fmt"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// PricingConfig persists the versioned pricing configuration: whole-document
// revisions, tenants' plan assignments over time, and the version number
// charging watches. Every write takes the pricing_config_state row lock, so
// writes are serialized and each one bumps the version in its own
// transaction. Documents are opaque here; logic parses and validates them.
//
// PricingConfig 持久化带版本的定价配置：整份文档的版本、租户随时间变化的方案归属，
// 以及扣费所关注的版本号。每次写入都先拿 pricing_config_state 的行锁，因此写入彼此
// 串行，并在各自的事务里让版本号加一。这里不解析文档；由 logic 解析并校验。
type PricingConfig interface {
	// LatestPricingRevision returns the newest revision, or ErrNotFound
	// when there is none yet.
	// LatestPricingRevision 返回最新的版本；还没有任何版本时返回 ErrNotFound。
	LatestPricingRevision(ctx context.Context) (model.PricingRevision, error)
	// PricingRevisionAt returns the revision in effect at: the newest with
	// EffectiveAt not after it, or the earliest when at precedes them all
	// (usage from before the first revision is priced by the first). It
	// returns ErrNotFound when there is no revision.
	// PricingRevisionAt 返回在 at 时刻生效的版本：EffectiveAt 不晚于 at 的最新版本；
	// at 早于所有版本时返回最早的版本（第一个版本之前的用量按第一个版本计价）。没有
	// 任何版本时返回 ErrNotFound。
	PricingRevisionAt(ctx context.Context, at time.Time) (model.PricingRevision, error)
	// GetPricingRevision returns one revision with its document.
	// GetPricingRevision 返回一个版本及其文档。
	GetPricingRevision(ctx context.Context, id int64) (model.PricingRevision, error)
	// ListPricingRevisions pages through revisions newest first, leaving
	// each Document empty.
	// ListPricingRevisions 从新到旧分页读取版本，每个版本的 Document 留空。
	ListPricingRevisions(ctx context.Context, query ListQuery) (Page[model.PricingRevision], error)
	// CreateInitialPricingRevision writes rev as the first revision when
	// there is none, bumping the version, and reports whether it did. When
	// one already exists it writes nothing and returns the latest, so
	// replicas racing to bootstrap agree on one first revision.
	// CreateInitialPricingRevision 在还没有任何版本时把 rev 写为第一个版本并让版本号
	// 加一，同时报告是否写入。已经有版本时什么也不写，返回最新的版本，使同时初始化的
	// 多个副本对唯一的第一个版本达成一致。
	CreateInitialPricingRevision(ctx context.Context, rev model.PricingRevision) (model.PricingRevision, bool, error)
	// AppendPricingRevision appends a revision based on in.BaseRevisionID.
	// See AppendPricingRevision for the checks it makes under its lock.
	// AppendPricingRevision 基于 in.BaseRevisionID 追加一个版本。它在锁内所做的检查见
	// AppendPricingRevision 类型。
	AppendPricingRevision(ctx context.Context, in AppendPricingRevision) (model.PricingRevision, error)
	// SetTenantPricePlan moves a tenant to in.PlanID ("" is the default plan)
	// from in.Now on, and reports whether anything changed.
	// SetTenantPricePlan 让租户从 in.Now 起改属 in.PlanID（"" 为默认方案），并报告是否
	// 有变化。
	SetTenantPricePlan(ctx context.Context, in SetTenantPricePlan) (bool, error)
	// TenantPricePlansAt returns, for those of tenantIDs that belonged to a
	// plan other than the default at the given moment, that plan's id.
	// Tenants absent from the result belonged to the default plan.
	// TenantPricePlansAt 对 tenantIDs 中在给定时刻属于非默认方案的租户，返回该方案的
	// id。结果中没有的租户在那个时刻属于默认方案。
	TenantPricePlansAt(ctx context.Context, tenantIDs []string, at time.Time) (map[string]string, error)
	// CountTenantPricePlans returns how many tenants currently belong to
	// each plan other than the default.
	// CountTenantPricePlans 返回当前属于各个非默认方案的租户数。
	CountTenantPricePlans(ctx context.Context) (map[string]int64, error)
	// PricingConfigVersion returns the version number, 0 before any write.
	// PricingConfigVersion 返回版本号；任何写入之前为 0。
	PricingConfigVersion(ctx context.Context) (int64, error)
}

// AppendPricingRevision is a new revision and the checks guarding it. Under
// the pricing_config_state lock the store refuses with
// ErrPricingRevisionChanged when the latest revision is not BaseRevisionID,
// and with ErrPricePlanInUse when a tenant still belongs to one of
// UnusedPlans. EffectiveAt is Now, or the latest revision's EffectiveAt when
// a replica's clock lags it, so revisions stay in effect order.
//
// AppendPricingRevision 是一个新版本及守护它的检查。在 pricing_config_state 的锁内，
// 最新版本不是 BaseRevisionID 时 store 以 ErrPricingRevisionChanged 拒绝；仍有租户属于
// UnusedPlans 中的某个方案时以 ErrPricePlanInUse 拒绝。EffectiveAt 取 Now；副本时钟
// 落后于最新版本的 EffectiveAt 时取后者，使版本始终按生效顺序排列。
type AppendPricingRevision struct {
	BaseRevisionID int64
	Document       string
	CreatedBy      string
	Summary        string
	Now            time.Time
	UnusedPlans    []string
}

// SetTenantPricePlan is a tenant's move to another plan. Under the
// pricing_config_state lock the store refuses with ErrPricingRevisionChanged
// when the latest revision is not ExpectRevisionID, the revision logic
// checked PlanID against, so a plan deleted meanwhile cannot be assigned.
// The change takes effect at Now, or at the open assignment's ValidFrom when
// a replica's clock lags it.
//
// SetTenantPricePlan 是租户改属另一个方案。在 pricing_config_state 的锁内，最新版本
// 不是 ExpectRevisionID（logic 据以检查 PlanID 的那个版本）时，store 以
// ErrPricingRevisionChanged 拒绝，使期间被删除的方案无法被分配。变更在 Now 生效；副本
// 时钟落后于当前归属的 ValidFrom 时取后者。
type SetTenantPricePlan struct {
	TenantID         string
	PlanID           string
	ExpectRevisionID int64
	CreatedBy        string
	Now              time.Time
}

// Pricing-configuration conflicts. Both wrap ErrConflict, so a caller that
// only maps conflicts needs nothing more; logic retries a plan assignment on
// ErrPricingRevisionChanged.
//
// 定价配置的冲突。两者都包装了 ErrConflict，只映射冲突的调用方无需额外处理；logic 在
// ErrPricingRevisionChanged 时重试方案分配。
var (
	ErrPricingRevisionChanged = fmt.Errorf("%w: the pricing configuration changed", ErrConflict)
	ErrPricePlanInUse         = fmt.Errorf("%w: a tenant still belongs to the price plan", ErrConflict)
)
