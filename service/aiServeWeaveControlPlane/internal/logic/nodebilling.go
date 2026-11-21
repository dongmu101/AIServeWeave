package logic

import (
	"context"
	"time"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// NodeBillingEntry is one platform node marked free.
//
// NodeBillingEntry 是一台被标记为免费的平台节点。
type NodeBillingEntry struct {
	NodeID    string
	UpdatedBy string
	UpdatedAt time.Time
}

// ListNodeBilling returns every free platform node. A node absent from it
// is charged.
//
// ListNodeBilling 返回全部免费平台节点。不在其中的节点收费。
func (s *Service) ListNodeBilling(ctx context.Context, actor Actor) ([]NodeBillingEntry, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return nil, err
	}
	rows, err := s.store.ListNodeBilling(ctx)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]NodeBillingEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, NodeBillingEntry{NodeID: row.NodeID, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

// SetNodeBilling makes a platform node free or charged. A tenant-owned node
// is always free to its owner and charged to no one else, so it is refused
// with ErrConflict. The change applies to usage charged after it, whenever
// the request was made.
//
// SetNodeBilling 把一台平台节点设为免费或收费。商户自有节点对其所有者始终免费、也不服务
// 其他人，因此以 ErrConflict 拒绝。变更对此后扣费的用量生效，与请求发生的时间无关。
func (s *Service) SetNodeBilling(ctx context.Context, actor Actor, nodeID string, free bool) (NodeBillingEntry, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return NodeBillingEntry{}, err
	}
	if actor.UserID == "" {
		return NodeBillingEntry{}, ErrForbidden
	}
	if !nodeaccess.ValidNodeID(nodeID) {
		return NodeBillingEntry{}, ErrInvalidInput
	}
	if _, owned := nodeaccess.OwnerOf(nodeID); owned {
		return NodeBillingEntry{}, ErrConflict
	}
	if !free {
		if err := s.store.DeleteNodeBilling(ctx, nodeID); err != nil {
			return NodeBillingEntry{}, translate(err)
		}
		s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeBillingUpdate, nodeID, "free=false", actor.IP)
		return NodeBillingEntry{NodeID: nodeID}, nil
	}
	rows, err := s.store.ListNodeBilling(ctx)
	if err != nil {
		return NodeBillingEntry{}, translate(err)
	}
	if len(rows) >= nodeaccess.MaxDedicatedNodes && !containsNodeBilling(rows, nodeID) {
		return NodeBillingEntry{}, ErrConflict
	}
	now := s.clock.Now().UTC()
	row := model.NodeBilling{NodeID: nodeID, UpdatedBy: actor.UserID, UpdatedAt: now}
	if err := s.store.PutNodeBilling(ctx, &row); err != nil {
		return NodeBillingEntry{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeBillingUpdate, nodeID, "free=true", actor.IP)
	return NodeBillingEntry{NodeID: nodeID, UpdatedBy: actor.UserID, UpdatedAt: now}, nil
}

// freeNodes reads the free platform nodes, only when some record names a
// node that is not tenant-owned.
//
// freeNodes 读取免费平台节点；只有当某条记录带有非商户自有节点时才读取。
func (s *Service) freeNodes(ctx context.Context, records []model.UsageRecord) (map[string]bool, error) {
	needed := false
	for _, r := range records {
		if _, owned := nodeaccess.OwnerOf(r.NodeID); r.NodeID != "" && !owned {
			needed = true
			break
		}
	}
	if !needed {
		return nil, nil
	}
	rows, err := s.store.ListNodeBilling(ctx)
	if err != nil {
		return nil, translate(err)
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.NodeID] = true
	}
	return out, nil
}

func containsNodeBilling(rows []model.NodeBilling, nodeID string) bool {
	for _, row := range rows {
		if row.NodeID == nodeID {
			return true
		}
	}
	return false
}
