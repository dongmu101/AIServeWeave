package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"AIServeWeave/common/modelaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ModelAccessList is one stored common/modelaccess list as an operator sees
// it. Models is empty, never nil, when the scope is unrestricted, and
// UpdatedBy/UpdatedAt are zero when nobody has ever set it.
//
// ModelAccessList 是运维看到的一份已存储的 common/modelaccess 列表。scope 不受限制
// 时 Models 为空（绝不为 nil）；从未有人设置过时 UpdatedBy/UpdatedAt 为零值。
type ModelAccessList struct {
	Scope     string
	Models    []string
	UpdatedBy string
	UpdatedAt time.Time
}

// ListTenants returns one filtered page of every tenant to a platform
// operator, who needs it to choose whose model access to set.
//
// ListTenants 向平台运维返回一页经过筛选的全部租户，运维要据此挑选为哪个租户设置
// 模型授权。
func (s *Service) ListTenants(ctx context.Context, actor Actor, query store.ListQuery, filter store.TenantFilter) (store.Page[model.Tenant], error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return store.Page[model.Tenant]{}, err
	}
	if filter.Status != "" && filter.Status != model.StatusActive && filter.Status != model.StatusSuspended {
		return store.Page[model.Tenant]{}, ErrInvalidInput
	}
	page, err := s.store.ListTenants(ctx, query, filter)
	return page, translate(err)
}

// GetModelAccess returns the list stored under scope, which is either
// model.PlatformScope or a tenant id. A tenant id that does not exist is
// ErrNotFound rather than an empty list, so a typo in the Console is not
// mistaken for "this tenant is unrestricted".
//
// GetModelAccess 返回 scope 下存储的列表，scope 要么是 model.PlatformScope，要么是
// 一个租户 id。不存在的租户 id 返回 ErrNotFound 而不是空列表，免得 Console 里的一个
// 笔误被误读成「这个租户不受限制」。
func (s *Service) GetModelAccess(ctx context.Context, actor Actor, scope string) (ModelAccessList, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return ModelAccessList{}, err
	}
	if err := s.checkModelAccessScope(ctx, scope); err != nil {
		return ModelAccessList{}, err
	}
	rows, err := s.store.GetModelAccess(ctx, scope)
	if err != nil {
		return ModelAccessList{}, translate(err)
	}
	out := ModelAccessList{Scope: scope, Models: []string{}}
	for _, row := range rows {
		models, err := decodeModelAccess(row)
		if err != nil {
			return ModelAccessList{}, err
		}
		out.Models, out.UpdatedBy, out.UpdatedAt = models, row.UpdatedBy, row.UpdatedAt
	}
	return out, nil
}

// SetModelAccess replaces the list stored under scope. An empty list removes
// the restriction at that level. Every cached key verification is dropped
// afterwards, so Gateways stop serving a newly excluded model now rather than
// when their cached verifications expire.
//
// SetModelAccess 替换 scope 下存储的列表。空列表表示解除这一级的限制。之后会丢弃
// 全部已缓存的 Key 校验结果，使 Gateway 立即停止提供刚被排除的模型，而不是等各自
// 缓存的校验结果过期。
func (s *Service) SetModelAccess(ctx context.Context, actor Actor, scope string, models []string) (ModelAccessList, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return ModelAccessList{}, err
	}
	if actor.UserID == "" {
		return ModelAccessList{}, ErrForbidden
	}
	if err := s.checkModelAccessScope(ctx, scope); err != nil {
		return ModelAccessList{}, err
	}
	normalized, err := modelaccess.Normalize(models)
	if err != nil {
		return ModelAccessList{}, fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	now := s.clock.Now().UTC()
	out := ModelAccessList{Scope: scope, Models: normalized}
	if len(normalized) == 0 {
		err = s.store.DeleteModelAccess(ctx, scope)
	} else {
		body, marshalErr := json.Marshal(normalized)
		if marshalErr != nil {
			return ModelAccessList{}, marshalErr
		}
		out.UpdatedBy, out.UpdatedAt = actor.UserID, now
		err = s.store.PutModelAccess(ctx, &model.ModelAccess{Scope: scope, ModelsJSON: string(body), UpdatedBy: actor.UserID, UpdatedAt: now})
	}
	if err != nil {
		return ModelAccessList{}, translate(err)
	}
	if s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionModelAccessUpdate, scope,
		fmt.Sprintf("models=%d", len(normalized)), actor.IP)
	return out, nil
}

// TenantModelFilter returns the predicate a tenant's model inventory is
// filtered by, or nil when the tenant is unrestricted. The inventory lists
// node-reported model IDs while access is granted by alias, so a reported
// model is visible when it is itself allowed — the name an unrouted request
// would send — or when it is a target of an allowed alias in the current
// routing publication.
//
// TenantModelFilter 返回租户模型清单所用的过滤谓词；租户不受限制时返回 nil。清单
// 列出的是节点上报的模型 ID，而授权按别名给出，因此一个上报的模型在两种情况下可见：
// 它本身被允许——未经路由的请求发送的正是这个名字；或者它是当前路由发布中某个被允许
// 别名的目标。
func (s *Service) TenantModelFilter(ctx context.Context, actor Actor) (func(string) bool, error) {
	if actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return nil, ErrForbidden
	}
	policy, err := s.modelPolicy(ctx, actor.TenantID)
	if err != nil {
		return nil, err
	}
	if policy.Unrestricted() {
		return nil, nil
	}
	routes, err := s.CurrentRoutes(ctx)
	if err != nil {
		return nil, err
	}
	viaAlias := make(map[string]bool)
	for _, route := range routes.Routes {
		if !policy.Allows(route.Model) {
			continue
		}
		for _, target := range route.Targets {
			viaAlias[target.RuntimeModel] = true
		}
	}
	return func(id string) bool { return viaAlias[id] || policy.Allows(id) }, nil
}

// modelPolicy reads the platform list and tenantID's list in one store call.
//
// modelPolicy 用一次 store 调用读取平台列表与 tenantID 的列表。
func (s *Service) modelPolicy(ctx context.Context, tenantID string) (modelaccess.Policy, error) {
	rows, err := s.store.GetModelAccess(ctx, model.PlatformScope, tenantID)
	if err != nil {
		return modelaccess.Policy{}, translate(err)
	}
	var policy modelaccess.Policy
	for _, row := range rows {
		models, err := decodeModelAccess(row)
		if err != nil {
			return modelaccess.Policy{}, err
		}
		switch row.Scope {
		case model.PlatformScope:
			policy.Platform = models
		case tenantID:
			policy.Tenant = models
		}
	}
	return policy, nil
}

// checkModelAccessScope accepts PlatformScope or an existing tenant's id.
//
// checkModelAccessScope 接受 PlatformScope 或一个已存在租户的 id。
func (s *Service) checkModelAccessScope(ctx context.Context, scope string) error {
	if scope == model.PlatformScope {
		return nil
	}
	if scope == "" {
		return ErrInvalidInput
	}
	if _, err := s.store.GetTenant(ctx, scope); err != nil {
		return translate(err)
	}
	return nil
}

// decodeModelAccess reads a stored list back. A row that does not decode is
// an error rather than "unrestricted": failing open on a corrupt row would
// silently hand every model to the tenant it was meant to restrict.
//
// decodeModelAccess 读回一份已存储的列表。解码失败的行是错误，而不是「不限制」：
// 在损坏的行上失效放开，会悄悄把全部模型交给本该被限制的租户。
func decodeModelAccess(row model.ModelAccess) ([]string, error) {
	var models []string
	if err := json.Unmarshal([]byte(row.ModelsJSON), &models); err != nil {
		return nil, errors.New("logic: invalid stored model access")
	}
	if models == nil {
		models = []string{}
	}
	return models, nil
}
