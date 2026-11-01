package gormstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateAgentInstallCode implements store.AgentInstallCodes.
//
// CreateAgentInstallCode 实现 store.AgentInstallCodes。
func (s *Store) CreateAgentInstallCode(ctx context.Context, row *model.AgentInstallCode) error {
	return translate(s.db.WithContext(ctx).Create(row).Error)
}

// RedeemAgentInstallCode implements store.AgentInstallCodes with one
// conditional update, so concurrent redemptions mark the row once and the
// others see ErrNotFound.
//
// RedeemAgentInstallCode 以一次条件更新实现 store.AgentInstallCodes，使并发兑换只标记
// 一次，其余的看到 ErrNotFound。
func (s *Store) RedeemAgentInstallCode(ctx context.Context, codeHash string, now time.Time) (model.AgentInstallCode, error) {
	res := s.db.WithContext(ctx).Model(&model.AgentInstallCode{}).
		Where("code_hash = ? AND redeemed_at IS NULL AND expires_at > ?", codeHash, now).
		Update("redeemed_at", now)
	if res.Error != nil {
		return model.AgentInstallCode{}, translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return model.AgentInstallCode{}, store.ErrNotFound
	}
	var row model.AgentInstallCode
	err := s.db.WithContext(ctx).Where("code_hash = ?", codeHash).Take(&row).Error
	return row, translate(err)
}

// UnredeemAgentInstallCode implements store.AgentInstallCodes.
//
// UnredeemAgentInstallCode 实现 store.AgentInstallCodes。
func (s *Store) UnredeemAgentInstallCode(ctx context.Context, codeHash string) error {
	return translate(s.db.WithContext(ctx).Model(&model.AgentInstallCode{}).
		Where("code_hash = ?", codeHash).Update("redeemed_at", nil).Error)
}
