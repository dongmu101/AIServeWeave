package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// NodeAccessEntry is one dedicated node as an operator sees it. Tenants is
// empty, never nil, for a node that is shared, and UpdatedBy/UpdatedAt are zero
// when nobody has ever bound it.
//
// NodeAccessEntry 是运维看到的一台专属节点。共享节点的 Tenants 为空（绝不为 nil）；
// 从未有人绑定过时 UpdatedBy/UpdatedAt 为零值。
type NodeAccessEntry struct {
	NodeID    string
	Tenants   []string
	UpdatedBy string
	UpdatedAt time.Time
}

// ListNodeAccess returns every dedicated node, ordered by node id. A node
// absent from the result is shared.
//
// ListNodeAccess 按节点 id 顺序返回每台专属节点。不在结果中的节点是共享节点。
func (s *Service) ListNodeAccess(ctx context.Context, actor Actor) ([]NodeAccessEntry, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return nil, err
	}
	rows, err := s.store.ListNodeAccess(ctx)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]NodeAccessEntry, 0, len(rows))
	for _, row := range rows {
		tenants, err := decodeNodeAccess(row)
		if err != nil {
			return nil, err
		}
		out = append(out, NodeAccessEntry{NodeID: row.NodeID, Tenants: tenants, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

// SetNodeAccess replaces the tenants nodeID is dedicated to. An empty list
// makes the node shared again. Every tenant named must exist: a typo that
// bound a node to nobody would close it to every tenant at once. A node that
// is not yet dedicated is refused with ErrConflict once
// nodeaccess.MaxDedicatedNodes nodes already are. Every cached key
// verification is dropped afterwards, so Gateways stop sending other tenants'
// work to a newly dedicated node now rather than when their cached
// verifications expire. A tenant-owned node is refused with ErrConflict.
//
// SetNodeAccess 替换 nodeID 专属的租户。空列表让该节点恢复共享。点名的每个租户都
// 必须存在：一个把节点绑给「没有人」的笔误，会一次性对所有租户关闭这台节点。已有
// nodeaccess.MaxDedicatedNodes 台专属节点时，再把一台尚未专属的节点设为专属会以
// ErrConflict 拒绝。之后会丢弃全部已缓存的 Key 校验结果，使 Gateway 立即停止把其他
// 租户的工作发往刚设为专属的节点，而不是等各自缓存的校验结果过期。商户自有节点以
// ErrConflict 拒绝。
func (s *Service) SetNodeAccess(ctx context.Context, actor Actor, nodeID string, tenants []string) (NodeAccessEntry, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return NodeAccessEntry{}, err
	}
	if actor.UserID == "" {
		return NodeAccessEntry{}, ErrForbidden
	}
	if !nodeaccess.ValidNodeID(nodeID) {
		return NodeAccessEntry{}, ErrInvalidInput
	}
	// A tenant-owned node already admits exactly its owner; a binding could
	// only open the owner's hardware to other tenants.
	// 商户自有节点本来就只接纳其所有者；绑定只可能把所有者的硬件开放给其他商户。
	if _, owned := nodeaccess.OwnerOf(nodeID); owned {
		return NodeAccessEntry{}, ErrConflict
	}
	normalized, err := nodeaccess.NormalizeTenants(tenants)
	if err != nil {
		return NodeAccessEntry{}, fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	for _, tenantID := range normalized {
		if _, err := s.store.GetTenant(ctx, tenantID); err != nil {
			return NodeAccessEntry{}, translate(err)
		}
	}

	now := s.clock.Now().UTC()
	out := NodeAccessEntry{NodeID: nodeID, Tenants: normalized}
	if len(normalized) == 0 {
		err = s.store.DeleteNodeAccess(ctx, nodeID)
	} else {
		if err := s.checkDedicatedCapacity(ctx, nodeID); err != nil {
			return NodeAccessEntry{}, err
		}
		body, marshalErr := json.Marshal(normalized)
		if marshalErr != nil {
			return NodeAccessEntry{}, marshalErr
		}
		out.UpdatedBy, out.UpdatedAt = actor.UserID, now
		err = s.store.PutNodeAccess(ctx, &model.NodeAccess{NodeID: nodeID, TenantsJSON: string(body), UpdatedBy: actor.UserID, UpdatedAt: now})
	}
	if err != nil {
		return NodeAccessEntry{}, translate(err)
	}
	if s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeAccessUpdate, nodeID,
		fmt.Sprintf("tenants=%d", len(normalized)), actor.IP)
	return out, nil
}

// checkDedicatedCapacity refuses to dedicate one more node once the cap is
// reached. Rebinding a node that is already dedicated never counts against
// it. Two operators dedicating different nodes at the same instant can each
// pass the check, so the cap may be exceeded by the number of concurrent
// writers; it bounds payload size, not a security property, and that slack is
// accepted rather than taking a table lock for it.
//
// checkDedicatedCapacity 在达到上限后拒绝再新增一台专属节点。重新绑定一台已经专属的
// 节点永远不计入上限。两位运维在同一时刻专属不同节点时可能都通过检查，因此上限可能被
// 并发写入者的数量超出；它约束的是载荷大小而非安全属性，这点余量是可接受的，不值得
// 为它加表锁。
func (s *Service) checkDedicatedCapacity(ctx context.Context, nodeID string) error {
	rows, err := s.store.ListNodeAccess(ctx)
	if err != nil {
		return translate(err)
	}
	if len(rows) < nodeaccess.MaxDedicatedNodes {
		return nil
	}
	for _, row := range rows {
		if row.NodeID == nodeID {
			return nil
		}
	}
	return ErrConflict
}

// nodePolicy derives the nodes closed to tenantID from every stored binding.
//
// nodePolicy 由全部已存储的绑定推导出对 tenantID 关闭的节点。
func (s *Service) nodePolicy(ctx context.Context, tenantID string) (nodeaccess.Policy, error) {
	rows, err := s.store.ListNodeAccess(ctx)
	if err != nil {
		return nodeaccess.Policy{}, translate(err)
	}
	bindings := make([]nodeaccess.Binding, 0, len(rows))
	for _, row := range rows {
		tenants, err := decodeNodeAccess(row)
		if err != nil {
			return nodeaccess.Policy{}, err
		}
		bindings = append(bindings, nodeaccess.Binding{NodeID: row.NodeID, Tenants: tenants})
	}
	return nodeaccess.PolicyFor(bindings, tenantID), nil
}

// TenantNodeFilter returns the predicate a tenant's node inventory is filtered
// by: nodeaccess.Permits for the actor's tenant. It is never nil, since a node
// another tenant owns is closed to every tenant but its owner.
//
// TenantNodeFilter 返回租户节点清单所用的过滤谓词，即针对 actor 所属租户的
// nodeaccess.Permits。它从不为 nil，因为另一商户自有的节点对除所有者之外的每个租户都
// 是关闭的。
func (s *Service) TenantNodeFilter(ctx context.Context, actor Actor) (func(string) bool, error) {
	if actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return nil, ErrForbidden
	}
	policy, err := s.nodePolicy(ctx, actor.TenantID)
	if err != nil {
		return nil, err
	}
	tenantID := actor.TenantID
	return func(nodeID string) bool { return nodeaccess.Permits(tenantID, policy, nodeID) }, nil
}

// decodeNodeAccess reads a stored tenant list back. A row that does not decode,
// or decodes to no tenants, is an error rather than "shared": failing open on a
// corrupt row would silently hand a dedicated node to every tenant.
//
// decodeNodeAccess 读回一份已存储的租户列表。解码失败或解码出空列表的行是错误，而
// 不是「共享」：在损坏的行上失效放开，会悄悄把一台专属节点交给所有租户。
func decodeNodeAccess(row model.NodeAccess) ([]string, error) {
	var tenants []string
	if err := json.Unmarshal([]byte(row.TenantsJSON), &tenants); err != nil || len(tenants) == 0 {
		return nil, errors.New("logic: invalid stored node access")
	}
	return tenants, nil
}
