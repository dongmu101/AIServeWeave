package logic

import (
	"context"
	"errors"
	"fmt"
	"time"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/agentenrollment"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// MaxTenantNodesListed bounds one tenant's node listing, deleted nodes
// included. MaxCreatesPerDay keeps a tenant's rows growing slowly, so this
// is ample for the nodes anyone still looks at.
//
// MaxTenantNodesListed 限制一个商户节点列表的条数（包括已删除的节点）。
// MaxCreatesPerDay 让商户的行增长缓慢，因此这个数足以覆盖仍有人关心的节点。
const MaxTenantNodesListed = 200

// TenantNodeConfig enables tenant-owned nodes: what issues the bundles, the
// external addresses written into each and the two per-tenant bounds, as
// TenantNodesConf documents them.
//
// TenantNodeConfig 启用商户自有节点：由谁签发接入包、写进每个接入包的对外地址，以及两项
// 按商户的上限，含义见 TenantNodesConf。
type TenantNodeConfig struct {
	// Enrollment issues each bundle.
	//
	// Enrollment 签发每个接入包。
	Enrollment       NodeEnrollment
	RegistryAddr     string
	GatewayAddr      string
	MaxPerTenant     int
	MaxCreatesPerDay int
}

// WithTenantNodes turns tenant-owned nodes on. Without it every tenant node
// method returns ErrNotFound, matching the routes that are not mounted.
//
// WithTenantNodes 开启商户自有节点。没有它时每个商户节点方法都返回 ErrNotFound，
// 与未挂载的路由一致。
func WithTenantNodes(cfg TenantNodeConfig) Option {
	return func(s *Service) { s.tenantNodes = &cfg }
}

// TenantNodeLimits returns the per-tenant bounds, both zero when the feature
// is off.
//
// TenantNodeLimits 返回按商户的上限；本功能关闭时两者都为零。
func (s *Service) TenantNodeLimits() (maxPerTenant, maxCreatesPerDay int) {
	if s.tenantNodes == nil {
		return 0, 0
	}
	return s.tenantNodes.MaxPerTenant, s.tenantNodes.MaxCreatesPerDay
}

// canManageNodes reports whether the actor's role may create and delete the
// tenant's nodes: installing hardware that serves every key of the tenant is
// an owner's or admin's decision.
//
// canManageNodes 报告 actor 的角色能否生成与删除商户节点：接入一台为本商户所有 Key
// 服务的机器，是 owner 或 admin 的决定。
func (a Actor) canManageNodes() bool { return a.Role == model.RoleOwner || a.Role == model.RoleAdmin }

// requireTenantNodes checks the feature is on and the actor is a tenant.
//
// requireTenantNodes 检查本功能已开启且 actor 是租户。
func (s *Service) requireTenantNodes(actor Actor) error {
	if s.tenantNodes == nil {
		return ErrNotFound
	}
	if actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return ErrForbidden
	}
	return nil
}

// ListTenantNodes returns the actor's tenant's nodes, newest first, at most
// MaxTenantNodesListed of them. Every role may read it: the list holds no
// credential.
//
// ListTenantNodes 返回 actor 所属商户的节点，最新的在前，至多 MaxTenantNodesListed
// 条。所有角色都可读：列表里没有凭据。
func (s *Service) ListTenantNodes(ctx context.Context, actor Actor) ([]model.TenantNode, error) {
	if err := s.requireTenantNodes(actor); err != nil {
		return nil, err
	}
	rows, err := s.store.ListTenantNodes(ctx, actor.TenantID, MaxTenantNodesListed)
	return rows, translate(err)
}

// CreateTenantNode records a new node owned by the actor's tenant and issues
// its enrollment bundle, which is returned once and never stored. It returns
// ErrConflict when the tenant already has MaxPerTenant nodes, or created
// MaxCreatesPerDay in the last 24 hours; concurrent creations may each pass
// that check, the slack checkDedicatedCapacity documents. The row is written
// before the token is minted and removed again if issuing fails, so a token
// never exists for a node the tenant cannot see and delete.
//
// CreateTenantNode 为 actor 所属商户记录一台新节点并签发其接入包；接入包只返回一次，
// 从不存储。商户已有 MaxPerTenant 台节点，或过去 24 小时内已生成 MaxCreatesPerDay 个
// 时返回 ErrConflict；并发生成可能都通过检查，余量同 checkDedicatedCapacity 所述。行在
// 签发令牌之前写入、签发失败时再删除，因此绝不会存在一枚对应商户看不到也删不掉的节点的
// 令牌。
func (s *Service) CreateTenantNode(ctx context.Context, actor Actor, ttl time.Duration) (agentenrollment.Bundle, error) {
	if err := s.requireTenantNodes(actor); err != nil {
		return agentenrollment.Bundle{}, err
	}
	if !actor.canManageNodes() || actor.UserID == "" {
		return agentenrollment.Bundle{}, ErrForbidden
	}
	if s.tenantNodes.Enrollment == nil {
		return agentenrollment.Bundle{}, ErrUnavailable
	}
	now := s.clock.Now().UTC()
	active, created, err := s.store.CountTenantNodes(ctx, actor.TenantID, now.Add(-24*time.Hour))
	if err != nil {
		return agentenrollment.Bundle{}, translate(err)
	}
	if active >= int64(s.tenantNodes.MaxPerTenant) || created >= int64(s.tenantNodes.MaxCreatesPerDay) {
		return agentenrollment.Bundle{}, ErrConflict
	}
	nodeID, err := nodeaccess.NewTenantNodeID(actor.TenantID)
	if err != nil {
		return agentenrollment.Bundle{}, err
	}
	row := model.TenantNode{NodeID: nodeID, TenantID: actor.TenantID, CreatedBy: actor.UserID, CreatedAt: now, EnrollBy: now.Add(ttl)}
	if err := s.store.CreateTenantNode(ctx, &row); err != nil {
		return agentenrollment.Bundle{}, translate(err)
	}
	bundle, err := s.tenantNodes.Enrollment.CreateNode(ctx, nodeID, s.tenantNodes.RegistryAddr, s.tenantNodes.GatewayAddr, ttl)
	if err != nil {
		// The caller may have gone away; the row must still go.
		// 调用方可能已经离开；这一行仍然必须删除。
		_ = s.store.DeleteTenantNode(context.WithoutCancel(ctx), nodeID)
		if errors.Is(err, agentenrollment.ErrInvalidInput) {
			return agentenrollment.Bundle{}, ErrInvalidInput
		}
		return agentenrollment.Bundle{}, ErrUnavailable
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionTenantNodeCreate, nodeID,
		"expires_at="+bundle.ExpiresAt.UTC().Format(time.RFC3339), actor.IP)
	return bundle, nil
}

// RevokeTenantNode deletes one of the actor's tenant's nodes: it disables the
// identity at the Registry, which also cuts the node's open connections, and
// only then marks the row deleted, so a row never reads deleted while the node
// can still connect. A node that is not the tenant's, or already deleted, is
// ErrNotFound; a Registry failure is ErrUnavailable and leaves the row as it
// was, so the deletion can simply be retried.
//
// RevokeTenantNode 删除 actor 所属商户的一台节点：先在 Registry 停用该身份（这也会切断
// 节点已打开的连接），之后才把行标记为已删除，因此绝不会出现行显示已删除、节点却仍能连接
// 的情形。不属于本商户或已删除的节点返回 ErrNotFound；Registry 失败返回 ErrUnavailable，
// 行保持原样，删除可以直接重试。
func (s *Service) RevokeTenantNode(ctx context.Context, actor Actor, nodeID string) error {
	if err := s.requireTenantNodes(actor); err != nil {
		return err
	}
	if !actor.canManageNodes() || actor.UserID == "" {
		return ErrForbidden
	}
	if owner, owned := nodeaccess.OwnerOf(nodeID); !owned || owner != actor.TenantID {
		return ErrNotFound
	}
	row, err := s.store.GetTenantNode(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (row.TenantID != actor.TenantID || row.RevokedAt != nil)) {
		return ErrNotFound
	}
	if err != nil {
		return translate(err)
	}
	if s.registryClient == nil {
		return ErrUnavailable
	}
	if err := s.registryClient.DisableNode(ctx, nodeID); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := s.store.RevokeTenantNode(ctx, actor.TenantID, nodeID, actor.UserID, s.clock.Now().UTC()); err != nil {
		return translate(err)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionTenantNodeRevoke, nodeID, "", actor.IP)
	return nil
}
