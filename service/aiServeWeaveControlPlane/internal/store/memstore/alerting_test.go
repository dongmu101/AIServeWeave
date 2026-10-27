package memstore_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func TestAlertRuleCRUD(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	rule := model.AlertRule{
		ID: "rule_1", Name: "high error rate", Metric: model.MetricSuccessRate,
		Operator: model.OperatorLessThan, Threshold: 0.9, ConsecutiveBuckets: 3,
		Enabled: true, CreatedAt: time.Now(),
	}
	if err := s.CreateAlertRule(ctx, &rule); err != nil {
		t.Fatalf("CreateAlertRule() error = %v, want nil", err)
	}

	got, err := s.GetAlertRule(ctx, "rule_1")
	if err != nil {
		t.Fatalf("GetAlertRule() error = %v, want nil", err)
	}
	if got.Name != "high error rate" {
		t.Fatalf("GetAlertRule().Name = %q, want %q", got.Name, "high error rate")
	}

	if _, err := s.GetAlertRule(ctx, "no_such_rule"); err != store.ErrNotFound {
		t.Fatalf("GetAlertRule(missing) error = %v, want store.ErrNotFound", err)
	}

	updated, err := s.UpdateAlertRule(ctx, "rule_1", store.AlertRuleUpdate{
		Name: "high error rate v2", Metric: model.MetricSuccessRate, Operator: model.OperatorLessThan,
		Threshold: 0.95, ConsecutiveBuckets: 5, WebhookURL: "https://example.com/hook", Enabled: false,
	})
	if err != nil {
		t.Fatalf("UpdateAlertRule() error = %v, want nil", err)
	}
	if updated.Name != "high error rate v2" || updated.Enabled {
		t.Fatalf("UpdateAlertRule() = %+v, want updated name and Enabled=false", updated)
	}

	if _, err := s.UpdateAlertRule(ctx, "no_such_rule", store.AlertRuleUpdate{}); err != store.ErrNotFound {
		t.Fatalf("UpdateAlertRule(missing) error = %v, want store.ErrNotFound", err)
	}

	// Second rule stays enabled so the enabled filter has something to exclude.
	// 第二条规则保持启用，让 enabled 过滤有东西可以排除。
	other := model.AlertRule{ID: "rule_2", Name: "capacity low", Metric: model.MetricCapacity, Enabled: true, CreatedAt: time.Now().Add(time.Minute)}
	if err := s.CreateAlertRule(ctx, &other); err != nil {
		t.Fatalf("CreateAlertRule(rule_2) error = %v, want nil", err)
	}

	enabled := true
	page, err := s.ListAlertRules(ctx, store.ListQuery{}, store.AlertRuleFilter{Enabled: &enabled})
	if err != nil {
		t.Fatalf("ListAlertRules(enabled) error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "rule_2" {
		t.Fatalf("ListAlertRules(enabled) = %+v, want only rule_2 (rule_1 was disabled by the update above)", page.Items)
	}

	disabled := false
	page, err = s.ListAlertRules(ctx, store.ListQuery{}, store.AlertRuleFilter{Enabled: &disabled})
	if err != nil {
		t.Fatalf("ListAlertRules(disabled) error = %v, want nil", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "rule_1" {
		t.Fatalf("ListAlertRules(disabled) = %+v, want only rule_1", page.Items)
	}

	if err := s.DeleteAlertRule(ctx, "rule_1"); err != nil {
		t.Fatalf("DeleteAlertRule() error = %v, want nil", err)
	}
	if _, err := s.GetAlertRule(ctx, "rule_1"); err != store.ErrNotFound {
		t.Fatalf("GetAlertRule(deleted) error = %v, want store.ErrNotFound", err)
	}
	if err := s.DeleteAlertRule(ctx, "rule_1"); err != store.ErrNotFound {
		t.Fatalf("DeleteAlertRule(already deleted) error = %v, want store.ErrNotFound", err)
	}
}

func TestListEnabledAlertRulesReturnsOnlyEnabled(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	rules := []model.AlertRule{
		{ID: "rule_on", Enabled: true, CreatedAt: time.Now()},
		{ID: "rule_off", Enabled: false, CreatedAt: time.Now()},
	}
	for i := range rules {
		if err := s.CreateAlertRule(ctx, &rules[i]); err != nil {
			t.Fatalf("CreateAlertRule(%s) error = %v, want nil", rules[i].ID, err)
		}
	}

	out, err := s.ListEnabledAlertRules(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAlertRules() error = %v, want nil", err)
	}
	if len(out) != 1 || out[0].ID != "rule_on" {
		t.Fatalf("ListEnabledAlertRules() = %+v, want only rule_on", out)
	}
}

func TestGetOpenAlertInstanceInvariant(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	if _, ok, err := s.GetOpenAlertInstance(ctx, "rule_1"); err != nil || ok {
		t.Fatalf("GetOpenAlertInstance(no instances) = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	older := model.AlertInstance{ID: "inst_old", RuleID: "rule_1", Status: model.AlertStatusFiring, CreatedAt: base}
	newer := model.AlertInstance{ID: "inst_new", RuleID: "rule_1", Status: model.AlertStatusFiring, CreatedAt: base.Add(time.Minute)}
	if err := s.CreateAlertInstance(ctx, &older); err != nil {
		t.Fatalf("CreateAlertInstance(older) error = %v, want nil", err)
	}
	if err := s.CreateAlertInstance(ctx, &newer); err != nil {
		t.Fatalf("CreateAlertInstance(newer) error = %v, want nil", err)
	}

	got, ok, err := s.GetOpenAlertInstance(ctx, "rule_1")
	if err != nil || !ok {
		t.Fatalf("GetOpenAlertInstance() = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if got.ID != "inst_new" {
		t.Fatalf("GetOpenAlertInstance().ID = %q, want %q (the most recently fired open instance)", got.ID, "inst_new")
	}

	// Resolving the only instances that were open must clear the open state
	// entirely, not just for the one resolved.
	//
	// 消解掉全部处于 open 状态的实例后，open 状态必须彻底清空，而不只是
	// 针对被消解的那一个。
	if err := s.ResolveAlertInstance(ctx, "inst_old", time.Now()); err != nil {
		t.Fatalf("ResolveAlertInstance(inst_old) error = %v, want nil", err)
	}
	if err := s.ResolveAlertInstance(ctx, "inst_new", time.Now()); err != nil {
		t.Fatalf("ResolveAlertInstance(inst_new) error = %v, want nil", err)
	}
	if _, ok, err := s.GetOpenAlertInstance(ctx, "rule_1"); err != nil || ok {
		t.Fatalf("GetOpenAlertInstance(after resolving all) = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestAcknowledgeAlertInstance(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	inst := model.AlertInstance{ID: "inst_1", RuleID: "rule_1", Status: model.AlertStatusFiring, CreatedAt: time.Now()}
	if err := s.CreateAlertInstance(ctx, &inst); err != nil {
		t.Fatalf("CreateAlertInstance() error = %v, want nil", err)
	}

	ackAt := time.Now()
	acked, err := s.AcknowledgeAlertInstance(ctx, "inst_1", "user_1", ackAt)
	if err != nil {
		t.Fatalf("AcknowledgeAlertInstance() error = %v, want nil", err)
	}
	if acked.Status != model.AlertStatusAcknowledged || acked.AcknowledgedBy != "user_1" {
		t.Fatalf("AcknowledgeAlertInstance() = %+v, want Status=acknowledged, AcknowledgedBy=user_1", acked)
	}

	if _, err := s.AcknowledgeAlertInstance(ctx, "no_such_instance", "user_1", ackAt); err != store.ErrNotFound {
		t.Fatalf("AcknowledgeAlertInstance(missing) error = %v, want store.ErrNotFound", err)
	}

	if err := s.ResolveAlertInstance(ctx, "inst_1", time.Now()); err != nil {
		t.Fatalf("ResolveAlertInstance() error = %v, want nil", err)
	}
	if _, err := s.AcknowledgeAlertInstance(ctx, "inst_1", "user_1", time.Now()); err != store.ErrConflict {
		t.Fatalf("AcknowledgeAlertInstance(resolved) error = %v, want store.ErrConflict (a closed alert cannot be acknowledged)", err)
	}
}

func TestListAlertInstancesFilters(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	instances := []model.AlertInstance{
		{ID: "inst_a1", RuleID: "rule_a", Status: model.AlertStatusFiring, CreatedAt: base},
		{ID: "inst_a2", RuleID: "rule_a", Status: model.AlertStatusResolved, CreatedAt: base.Add(time.Minute)},
		{ID: "inst_b1", RuleID: "rule_b", Status: model.AlertStatusFiring, CreatedAt: base.Add(2 * time.Minute)},
	}
	for i := range instances {
		if err := s.CreateAlertInstance(ctx, &instances[i]); err != nil {
			t.Fatalf("CreateAlertInstance(%s) error = %v, want nil", instances[i].ID, err)
		}
	}

	tests := []struct {
		name    string
		filter  store.AlertInstanceFilter
		wantIDs []string
	}{
		{name: "no filter lists newest first", filter: store.AlertInstanceFilter{}, wantIDs: []string{"inst_b1", "inst_a2", "inst_a1"}},
		{name: "status filter", filter: store.AlertInstanceFilter{Status: model.AlertStatusFiring}, wantIDs: []string{"inst_b1", "inst_a1"}},
		{name: "rule filter", filter: store.AlertInstanceFilter{RuleID: "rule_a"}, wantIDs: []string{"inst_a2", "inst_a1"}},
		{name: "since excludes earlier rows", filter: store.AlertInstanceFilter{Since: base.Add(90 * time.Second)}, wantIDs: []string{"inst_b1"}},
		{name: "until excludes rows at or after it", filter: store.AlertInstanceFilter{Until: base.Add(time.Minute)}, wantIDs: []string{"inst_a1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := s.ListAlertInstances(ctx, store.ListQuery{}, tt.filter)
			if err != nil {
				t.Fatalf("ListAlertInstances(%+v) error = %v, want nil", tt.filter, err)
			}
			gotIDs := make([]string, len(page.Items))
			for i, inst := range page.Items {
				gotIDs[i] = inst.ID
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("ListAlertInstances(%+v) = %v, want %v", tt.filter, gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("ListAlertInstances(%+v)[%d] = %q, want %q (newest first)", tt.filter, i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}
