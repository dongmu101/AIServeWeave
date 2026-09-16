package gormstore

import (
	"context"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// -----------------------------------------------------------------------
// ResponseTurns
// -----------------------------------------------------------------------

// CreateResponseTurn inserts one turn.
//
// CreateResponseTurn 插入一轮。
func (s *Store) CreateResponseTurn(ctx context.Context, turn *model.ResponseTurn) error {
	return translate(s.db.WithContext(ctx).Create(turn).Error)
}

// GetResponseTurn reads one turn by id, scoped to its tenant.
//
// GetResponseTurn 按 id 读取一轮，并限定在其租户范围内。
func (s *Store) GetResponseTurn(ctx context.Context, tenantID, id string) (model.ResponseTurn, error) {
	var out model.ResponseTurn
	err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, tenantID).Take(&out).Error
	return out, translate(err)
}
