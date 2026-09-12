// alertrules.go implements STATUS.md's P09/C29 alert rule CRUD: platform-
// actor authorization, input validation against the closed Metric/Operator
// enums, and audit logging on every write — the same pattern
// CreatePlatformOperatorAs/ApproveNode already establish for platform
// writes in this service.
//
// alertrules.go 实现 STATUS.md P09/C29 的告警规则 CRUD：平台身份授权、对
// 封闭 Metric/Operator 枚举的输入校验，以及每次写入都记审计——与本服务里
// CreatePlatformOperatorAs、ApproveNode 已经确立的平台写入模式相同。
package logic

import (
	"context"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

var validAlertMetrics = map[string]bool{
	model.MetricRequestRate: true, model.MetricSuccessRate: true, model.MetricLatencyP95: true,
	model.MetricTokenUsage: true, model.MetricCapacity: true,
}
var validAlertOperators = map[string]bool{
	model.OperatorLessThan: true, model.OperatorLessThanOrEqual: true,
	model.OperatorGreaterThan: true, model.OperatorGreaterThanOrEqual: true,
}

// CreateAlertRuleParams is one rule to create.
//
// CreateAlertRuleParams 是要创建的一条规则。
type CreateAlertRuleParams struct {
	Name               string
	Metric             string
	Operator           string
	Threshold          float64
	ConsecutiveBuckets int
	WebhookURL         string
	Enabled            bool
}

func validateAlertRuleFields(name, metric, operator string, consecutiveBuckets int) error {
	if name == "" || !validAlertMetrics[metric] || !validAlertOperators[operator] || consecutiveBuckets <= 0 {
		return ErrInvalidInput
	}
	return nil
}

// CreateAlertRule creates one alert rule as the authenticated platform
// actor, recording the audit entry in the same transaction as the insert.
//
// CreateAlertRule 以已认证的平台身份创建一条告警规则，并在与插入同一事务中
// 记录审计。
func (s *Service) CreateAlertRule(ctx context.Context, actor Actor, p CreateAlertRuleParams) (model.AlertRule, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return model.AlertRule{}, err
	}
	if err := validateAlertRuleFields(p.Name, p.Metric, p.Operator, p.ConsecutiveBuckets); err != nil {
		return model.AlertRule{}, err
	}
	now := s.clock.Now()
	rule := model.AlertRule{
		ID: model.NewID(model.PrefixAlertRule), Name: p.Name, Metric: p.Metric, Operator: p.Operator,
		Threshold: p.Threshold, ConsecutiveBuckets: p.ConsecutiveBuckets, WebhookURL: p.WebhookURL,
		Enabled: p.Enabled, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateAlertRule(ctx, &rule); err != nil {
		return model.AlertRule{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionAlertRuleCreate, rule.ID, "", actor.IP)
	return rule, nil
}

// GetAlertRule reads one rule by id.
//
// GetAlertRule 按 id 读取一条规则。
func (s *Service) GetAlertRule(ctx context.Context, actor Actor, id string) (model.AlertRule, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return model.AlertRule{}, err
	}
	rule, err := s.store.GetAlertRule(ctx, id)
	return rule, translate(err)
}

// ListAlertRules reads one page of rules.
//
// ListAlertRules 读取一页规则。
func (s *Service) ListAlertRules(ctx context.Context, actor Actor, query store.ListQuery, filter store.AlertRuleFilter) (store.Page[model.AlertRule], error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return store.Page[model.AlertRule]{}, err
	}
	return s.store.ListAlertRules(ctx, query, filter)
}

// UpdateAlertRuleParams is CreateAlertRuleParams's shape, reused verbatim
// for a full-replace update — see store.AlertRuleUpdate's doc comment for
// why this resource uses full-replace rather than partial-field PATCH
// semantics.
//
// UpdateAlertRuleParams 与 CreateAlertRuleParams 形状相同，原样用于一次
// 整体替换式更新——为什么这个资源用整体替换而不是局部字段的 PATCH 语义，
// 见 store.AlertRuleUpdate 的文档注释。
type UpdateAlertRuleParams = CreateAlertRuleParams

// UpdateAlertRule replaces one rule's mutable fields.
//
// UpdateAlertRule 替换一条规则的可变字段。
func (s *Service) UpdateAlertRule(ctx context.Context, actor Actor, id string, p UpdateAlertRuleParams) (model.AlertRule, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return model.AlertRule{}, err
	}
	if err := validateAlertRuleFields(p.Name, p.Metric, p.Operator, p.ConsecutiveBuckets); err != nil {
		return model.AlertRule{}, err
	}
	rule, err := s.store.UpdateAlertRule(ctx, id, store.AlertRuleUpdate{
		Name: p.Name, Metric: p.Metric, Operator: p.Operator, Threshold: p.Threshold,
		ConsecutiveBuckets: p.ConsecutiveBuckets, WebhookURL: p.WebhookURL, Enabled: p.Enabled,
	})
	if err != nil {
		return model.AlertRule{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionAlertRuleUpdate, id, "", actor.IP)
	return rule, nil
}

// DeleteAlertRule removes one rule.
//
// DeleteAlertRule 移除一条规则。
func (s *Service) DeleteAlertRule(ctx context.Context, actor Actor, id string) error {
	if err := s.requirePlatformActor(actor); err != nil {
		return err
	}
	if err := s.store.DeleteAlertRule(ctx, id); err != nil {
		return translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionAlertRuleDelete, id, "", actor.IP)
	return nil
}
