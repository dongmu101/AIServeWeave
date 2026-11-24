package gormstore

import (
	"context"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// GetBillingSettings implements store.Billing.
//
// GetBillingSettings 实现 store.Billing。
func (s *Store) GetBillingSettings(ctx context.Context) (model.BillingSettings, error) {
	var row model.BillingSettings
	err := s.db.WithContext(ctx).Where("id = ?", model.SettingsRowID).Take(&row).Error
	return row, translate(err)
}

// PutBillingSettings implements store.Billing. The single row is upserted.
//
// PutBillingSettings 实现 store.Billing。唯一的一行以 upsert 写入。
func (s *Store) PutBillingSettings(ctx context.Context, settings model.BillingSettings) error {
	settings.ID = model.SettingsRowID
	return translate(s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"signup_grant_micro", "updated_by", "updated_at"}),
	}).Create(&settings).Error)
}
