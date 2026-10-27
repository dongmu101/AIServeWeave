package memstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateAlertRule implements store.Alerting.
//
// CreateAlertRule 实现 store.Alerting。
func (s *Store) CreateAlertRule(_ context.Context, rule *model.AlertRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alertRules[rule.ID] = *rule
	return nil
}

// GetAlertRule implements store.Alerting.
//
// GetAlertRule 实现 store.Alerting。
func (s *Store) GetAlertRule(_ context.Context, id string) (model.AlertRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, ok := s.alertRules[id]
	if !ok {
		return model.AlertRule{}, store.ErrNotFound
	}
	return rule, nil
}

// ListAlertRules implements store.Alerting.
//
// ListAlertRules 实现 store.Alerting。
func (s *Store) ListAlertRules(_ context.Context, query store.ListQuery, filter store.AlertRuleFilter) (store.Page[model.AlertRule], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.AlertRule
	for _, r := range s.alertRules {
		if filter.Enabled != nil && r.Enabled != *filter.Enabled {
			continue
		}
		out = append(out, r)
	}
	sortNewestFirst(out, func(r model.AlertRule) (time.Time, string) { return r.CreatedAt, r.ID })
	return paginate(out, query, func(r model.AlertRule) (time.Time, string) { return r.CreatedAt, r.ID })
}

// ListEnabledAlertRules implements store.Alerting.
//
// ListEnabledAlertRules 实现 store.Alerting。
func (s *Store) ListEnabledAlertRules(_ context.Context) ([]model.AlertRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.AlertRule
	for _, r := range s.alertRules {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out, nil
}

// UpdateAlertRule implements store.Alerting.
//
// UpdateAlertRule 实现 store.Alerting。
func (s *Store) UpdateAlertRule(_ context.Context, id string, update store.AlertRuleUpdate) (model.AlertRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, ok := s.alertRules[id]
	if !ok {
		return model.AlertRule{}, store.ErrNotFound
	}
	rule.Name, rule.Metric, rule.Operator = update.Name, update.Metric, update.Operator
	rule.Threshold, rule.ConsecutiveBuckets = update.Threshold, update.ConsecutiveBuckets
	rule.WebhookURL, rule.Enabled = update.WebhookURL, update.Enabled
	rule.UpdatedAt = time.Now().UTC()
	s.alertRules[id] = rule
	return rule, nil
}

// DeleteAlertRule implements store.Alerting.
//
// DeleteAlertRule 实现 store.Alerting。
func (s *Store) DeleteAlertRule(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.alertRules[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.alertRules, id)
	return nil
}

// CreateAlertInstance implements store.Alerting.
//
// CreateAlertInstance 实现 store.Alerting。
func (s *Store) CreateAlertInstance(_ context.Context, instance *model.AlertInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alertInstances[instance.ID] = *instance
	return nil
}

// GetOpenAlertInstance implements store.Alerting. It scans every instance for
// ruleID's most recent firing or acknowledged row by CreatedAt, rather than
// returning the first match a map iteration happens to yield — the interface
// promises "most recent" even though the evaluation loop's own invariant
// means at most one such row exists at a time.
//
// GetOpenAlertInstance 实现 store.Alerting。它扫描全部实例，按 CreatedAt
// 找出 ruleID 下最近一条 firing 或 acknowledged 的行，而不是直接返回 map
// 遍历顺序碰巧给出的第一条匹配——尽管评估循环自身的不变式保证同一时刻至多
// 存在一行这样的记录，接口承诺的仍是「最近一条」。
func (s *Store) GetOpenAlertInstance(_ context.Context, ruleID string) (model.AlertInstance, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found model.AlertInstance
	var ok bool
	for _, inst := range s.alertInstances {
		if inst.RuleID != ruleID {
			continue
		}
		if inst.Status != model.AlertStatusFiring && inst.Status != model.AlertStatusAcknowledged {
			continue
		}
		if !ok || inst.CreatedAt.After(found.CreatedAt) {
			found, ok = inst, true
		}
	}
	return found, ok, nil
}

// TouchAlertInstance implements store.Alerting.
//
// TouchAlertInstance 实现 store.Alerting。
func (s *Store) TouchAlertInstance(_ context.Context, id string, lastEvaluatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.alertInstances[id]
	if !ok {
		return store.ErrNotFound
	}
	inst.LastEvaluatedAt = lastEvaluatedAt
	s.alertInstances[id] = inst
	return nil
}

// ResolveAlertInstance implements store.Alerting.
//
// ResolveAlertInstance 实现 store.Alerting。
func (s *Store) ResolveAlertInstance(_ context.Context, id string, resolvedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.alertInstances[id]
	if !ok {
		return store.ErrNotFound
	}
	inst.Status = model.AlertStatusResolved
	inst.ResolvedAt, inst.LastEvaluatedAt = &resolvedAt, resolvedAt
	s.alertInstances[id] = inst
	return nil
}

// AcknowledgeAlertInstance implements store.Alerting, matching gormstore's
// AcknowledgeAlertInstance error contract exactly: ErrNotFound when the row
// does not exist at all, ErrConflict when it exists but is already resolved
// — an operator cannot acknowledge a closed alert.
//
// AcknowledgeAlertInstance 实现 store.Alerting，其错误契约与 gormstore 的
// AcknowledgeAlertInstance 完全一致：行根本不存在时返回 ErrNotFound，行存在
// 但已经 resolved 时返回 ErrConflict——运维不能确认一个已经关闭的告警。
func (s *Store) AcknowledgeAlertInstance(_ context.Context, id, actorID string, at time.Time) (model.AlertInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.alertInstances[id]
	if !ok {
		return model.AlertInstance{}, store.ErrNotFound
	}
	if inst.Status == model.AlertStatusResolved {
		return model.AlertInstance{}, store.ErrConflict
	}
	inst.Status, inst.AcknowledgedBy, inst.AcknowledgedAt = model.AlertStatusAcknowledged, actorID, &at
	s.alertInstances[id] = inst
	return inst, nil
}

// SetAlertInstanceNotifyResult implements store.Alerting.
//
// SetAlertInstanceNotifyResult 实现 store.Alerting。
func (s *Store) SetAlertInstanceNotifyResult(_ context.Context, id, status string, attempts int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.alertInstances[id]
	if !ok {
		return store.ErrNotFound
	}
	inst.NotifyStatus, inst.NotifyAttempts = status, attempts
	s.alertInstances[id] = inst
	return nil
}

// ListAlertInstances implements store.Alerting.
//
// ListAlertInstances 实现 store.Alerting。
func (s *Store) ListAlertInstances(_ context.Context, query store.ListQuery, filter store.AlertInstanceFilter) (store.Page[model.AlertInstance], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.AlertInstance
	for _, inst := range s.alertInstances {
		if filter.Status != "" && inst.Status != filter.Status {
			continue
		}
		if filter.RuleID != "" && inst.RuleID != filter.RuleID {
			continue
		}
		if !filter.Since.IsZero() && inst.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !inst.CreatedAt.Before(filter.Until) {
			continue
		}
		out = append(out, inst)
	}
	sortNewestFirst(out, func(a model.AlertInstance) (time.Time, string) { return a.CreatedAt, a.ID })
	return paginate(out, query, func(a model.AlertInstance) (time.Time, string) { return a.CreatedAt, a.ID })
}
