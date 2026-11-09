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

// NodeAccessEntry is one operator-controlled Agent access policy.
type NodeAccessEntry struct {
	NodeID    string
	Mode      string
	Tenants   []string
	UpdatedBy string
	UpdatedAt time.Time
}

// ListNodeAccess returns all explicitly configured Agents. Absent nodes are private.
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
		out = append(out, NodeAccessEntry{NodeID: row.NodeID, Mode: nodeAccessMode(row), Tenants: tenants, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

// SetNodeAccess preserves the original dedicated-binding API. Empty tenants
// explicitly opts into sharing; an absent row no longer means shared.
func (s *Service) SetNodeAccess(ctx context.Context, actor Actor, nodeID string, tenants []string) (NodeAccessEntry, error) {
	mode := nodeaccess.ModeDedicated
	if len(tenants) == 0 {
		mode = nodeaccess.ModeShared
	}
	return s.SetNodeAccessMode(ctx, actor, nodeID, tenants, mode)
}

func (s *Service) SetNodeAccessMode(ctx context.Context, actor Actor, nodeID string, tenants []string, mode string) (NodeAccessEntry, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return NodeAccessEntry{}, err
	}
	if actor.UserID == "" {
		return NodeAccessEntry{}, ErrForbidden
	}
	if !nodeaccess.ValidNodeID(nodeID) {
		return NodeAccessEntry{}, ErrInvalidInput
	}
	if _, owned := nodeaccess.OwnerOf(nodeID); owned {
		return NodeAccessEntry{}, ErrConflict
	}
	normalized, err := nodeaccess.NormalizeTenants(tenants)
	if err != nil {
		return NodeAccessEntry{}, fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	if !validNodeAccessMode(mode, normalized) {
		return NodeAccessEntry{}, ErrInvalidInput
	}
	for _, tenantID := range normalized {
		if _, err := s.store.GetTenant(ctx, tenantID); err != nil {
			return NodeAccessEntry{}, translate(err)
		}
	}
	if err := s.checkDedicatedCapacity(ctx, nodeID); err != nil {
		return NodeAccessEntry{}, err
	}
	now := s.clock.Now().UTC()
	body, err := json.Marshal(normalized)
	if err != nil {
		return NodeAccessEntry{}, err
	}
	row := model.NodeAccess{NodeID: nodeID, Mode: mode, TenantsJSON: string(body), UpdatedBy: actor.UserID, UpdatedAt: now}
	if err := s.store.PutNodeAccess(ctx, &row); err != nil {
		return NodeAccessEntry{}, translate(err)
	}
	if s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeAccessUpdate, nodeID,
		fmt.Sprintf("mode=%s tenants=%d", mode, len(normalized)), actor.IP)
	return NodeAccessEntry{NodeID: nodeID, Mode: mode, Tenants: normalized, UpdatedBy: actor.UserID, UpdatedAt: now}, nil
}

// setEnrollmentSharing persists policy before exposing an enrollment credential.
func (s *Service) setEnrollmentSharing(ctx context.Context, actor Actor, nodeID string, shared bool) error {
	mode := nodeaccess.ModePrivate
	if shared {
		mode = nodeaccess.ModeShared
	}
	_, err := s.SetNodeAccessMode(ctx, actor, nodeID, []string{}, mode)
	return err
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
		bindings = append(bindings, nodeaccess.Binding{NodeID: row.NodeID, Mode: nodeAccessMode(row), Tenants: tenants})
	}
	return nodeaccess.PolicyFor(bindings, tenantID), nil
}

// TenantNodeFilter checks hard ownership/binding exclusions first. The model
// catalog then applies TenantAccountFilter for sharing and per-runtime leases.
func (s *Service) TenantNodeFilter(ctx context.Context, actor Actor) (func(string) bool, error) {
	if actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return nil, ErrForbidden
	}
	policy, err := s.nodePolicy(ctx, actor.TenantID)
	if err != nil {
		return nil, err
	}
	tenantID := actor.TenantID
	return func(nodeID string) bool { return nodeaccess.CanConsider(tenantID, policy, nodeID) }, nil
}

func nodeAccessMode(row model.NodeAccess) string {
	if row.Mode == "" {
		return nodeaccess.ModeDedicated
	}
	return row.Mode
}

func validNodeAccessMode(mode string, tenants []string) bool {
	switch mode {
	case nodeaccess.ModeShared, nodeaccess.ModePrivate:
		return len(tenants) == 0
	case nodeaccess.ModeDedicated:
		return len(tenants) > 0
	default:
		return false
	}
}

func decodeNodeAccess(row model.NodeAccess) ([]string, error) {
	var tenants []string
	if err := json.Unmarshal([]byte(row.TenantsJSON), &tenants); err != nil || tenants == nil || !validNodeAccessMode(nodeAccessMode(row), tenants) {
		return nil, errors.New("logic: invalid stored node access")
	}
	if _, err := nodeaccess.NormalizeTenants(tenants); err != nil {
		return nil, errors.New("logic: invalid stored node access")
	}
	return tenants, nil
}
