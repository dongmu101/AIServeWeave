package gormstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateAlertRule implements store.Alerting.
//
// CreateAlertRule 实现 store.Alerting。
func (s *Store) CreateAlertRule(ctx context.Context, rule *model.AlertRule) error {
	return translate(s.db.WithContext(ctx).Create(rule).Error)
}

// GetAlertRule implements store.Alerting.
//
// GetAlertRule 实现 store.Alerting。
func (s *Store) GetAlertRule(ctx context.Context, id string) (model.AlertRule, error) {
	var out model.AlertRule
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

// ListAlertRules implements store.Alerting.
//
// ListAlertRules 实现 store.Alerting。
func (s *Store) ListAlertRules(ctx context.Context, query store.ListQuery, filter store.AlertRuleFilter) (store.Page[model.AlertRule], error) {
	db := s.db.WithContext(ctx).Model(&model.AlertRule{})
	if filter.Enabled != nil {
		db = db.Where("enabled = ?", *filter.Enabled)
	}
	return readPage(db, query, func(r model.AlertRule) (time.Time, string) { return r.CreatedAt, r.ID })
}

// ListEnabledAlertRules implements store.Alerting.
//
// ListEnabledAlertRules 实现 store.Alerting。
func (s *Store) ListEnabledAlertRules(ctx context.Context) ([]model.AlertRule, error) {
	var out []model.AlertRule
	err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&out).Error
	return out, translate(err)
}

// UpdateAlertRule implements store.Alerting.
//
// UpdateAlertRule 实现 store.Alerting。
func (s *Store) UpdateAlertRule(ctx context.Context, id string, update store.AlertRuleUpdate) (model.AlertRule, error) {
	res := s.db.WithContext(ctx).Model(&model.AlertRule{}).Where("id = ?", id).Updates(map[string]any{
		"name": update.Name, "metric": update.Metric, "operator": update.Operator,
		"threshold": update.Threshold, "consecutive_buckets": update.ConsecutiveBuckets,
		"webhook_url": update.WebhookURL, "enabled": update.Enabled, "updated_at": time.Now().UTC(),
	})
	if res.Error != nil {
		return model.AlertRule{}, translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return model.AlertRule{}, store.ErrNotFound
	}
	return s.GetAlertRule(ctx, id)
}

// DeleteAlertRule implements store.Alerting.
//
// DeleteAlertRule 实现 store.Alerting。
func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.AlertRule{})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// CreateAlertInstance implements store.Alerting.
//
// CreateAlertInstance 实现 store.Alerting。
func (s *Store) CreateAlertInstance(ctx context.Context, instance *model.AlertInstance) error {
	return translate(s.db.WithContext(ctx).Create(instance).Error)
}

// GetOpenAlertInstance implements store.Alerting.
//
// GetOpenAlertInstance 实现 store.Alerting。
func (s *Store) GetOpenAlertInstance(ctx context.Context, ruleID string) (model.AlertInstance, bool, error) {
	var out model.AlertInstance
	err := s.db.WithContext(ctx).
		Where("rule_id = ? AND status IN ?", ruleID, []string{model.AlertStatusFiring, model.AlertStatusAcknowledged}).
		Order("created_at DESC").Take(&out).Error
	if err != nil {
		translated := translate(err)
		if translated == store.ErrNotFound {
			return model.AlertInstance{}, false, nil
		}
		return model.AlertInstance{}, false, translated
	}
	return out, true, nil
}

// TouchAlertInstance implements store.Alerting.
//
// TouchAlertInstance 实现 store.Alerting。
func (s *Store) TouchAlertInstance(ctx context.Context, id string, lastEvaluatedAt time.Time) error {
	return translate(s.db.WithContext(ctx).Model(&model.AlertInstance{}).Where("id = ?", id).
		Update("last_evaluated_at", lastEvaluatedAt).Error)
}

// ResolveAlertInstance implements store.Alerting.
//
// ResolveAlertInstance 实现 store.Alerting。
func (s *Store) ResolveAlertInstance(ctx context.Context, id string, resolvedAt time.Time) error {
	return translate(s.db.WithContext(ctx).Model(&model.AlertInstance{}).Where("id = ?", id).Updates(map[string]any{
		"status": model.AlertStatusResolved, "resolved_at": resolvedAt, "last_evaluated_at": resolvedAt,
	}).Error)
}

// AcknowledgeAlertInstance implements store.Alerting. The WHERE clause
// excludes an already-resolved instance so the database's own row lock,
// not a read-then-write in this process, decides whether the transition is
// still valid — the same reasoning UpdateJobState's ObservedSeq gate uses.
// When RowsAffected is 0, the inner Take disambiguates why: it either finds
// the row (excluded because it was already resolved, so ErrConflict) or
// fails with not-found, which translate() maps the same way every other
// read in this file does.
//
// AcknowledgeAlertInstance 实现 store.Alerting。WHERE 子句排除已经
// resolved 的实例，让数据库自身的行锁而不是本进程里的先读后写来裁定这次
// 转换是否仍然有效——与 UpdateJobState 的 ObservedSeq 门槛同一个道理。
// RowsAffected 为 0 时，内层 Take 用来判断原因：要么找到该行（因为已经
// resolved 而被排除，返回 ErrConflict），要么本身不存在，交给 translate()
// 按本文件其余读取相同的方式映射。
func (s *Store) AcknowledgeAlertInstance(ctx context.Context, id, actorID string, at time.Time) (model.AlertInstance, error) {
	res := s.db.WithContext(ctx).Model(&model.AlertInstance{}).
		Where("id = ? AND status <> ?", id, model.AlertStatusResolved).
		Updates(map[string]any{"status": model.AlertStatusAcknowledged, "acknowledged_by": actorID, "acknowledged_at": at})
	if res.Error != nil {
		return model.AlertInstance{}, translate(res.Error)
	}
	if res.RowsAffected == 0 {
		var existing model.AlertInstance
		if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&existing).Error; err != nil {
			return model.AlertInstance{}, translate(err)
		}
		return model.AlertInstance{}, store.ErrConflict
	}
	var out model.AlertInstance
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

// SetAlertInstanceNotifyResult implements store.Alerting.
//
// SetAlertInstanceNotifyResult 实现 store.Alerting。
func (s *Store) SetAlertInstanceNotifyResult(ctx context.Context, id, status string, attempts int) error {
	return translate(s.db.WithContext(ctx).Model(&model.AlertInstance{}).Where("id = ?", id).Updates(map[string]any{
		"notify_status": status, "notify_attempts": attempts,
	}).Error)
}

// ListAlertInstances implements store.Alerting.
//
// ListAlertInstances 实现 store.Alerting。
func (s *Store) ListAlertInstances(ctx context.Context, query store.ListQuery, filter store.AlertInstanceFilter) (store.Page[model.AlertInstance], error) {
	db := s.db.WithContext(ctx).Model(&model.AlertInstance{})
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if filter.RuleID != "" {
		db = db.Where("rule_id = ?", filter.RuleID)
	}
	if !filter.Since.IsZero() {
		db = db.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		db = db.Where("created_at < ?", filter.Until)
	}
	return readPage(db, query, func(a model.AlertInstance) (time.Time, string) { return a.CreatedAt, a.ID })
}
