package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// validAlertRuleParams returns one CreateAlertRuleParams that passes every
// validation check, for tests that mutate a single field to make it invalid.
//
// validAlertRuleParams 返回一份能通过全部校验的 CreateAlertRuleParams，供
// 测试改动单个字段使其非法时使用。
func validAlertRuleParams() logic.CreateAlertRuleParams {
	return logic.CreateAlertRuleParams{
		Name: "high error rate", Metric: model.MetricSuccessRate, Operator: model.OperatorLessThan,
		Threshold: 0.95, ConsecutiveBuckets: 3, WebhookURL: "https://example.com/hook", Enabled: true,
	}
}

func TestCreateAlertRuleRejectsANonPlatformActor(t *testing.T) {
	f := newPlatformFixture(t)
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if _, err := f.svc.CreateAlertRule(context.Background(), tenantActor, validAlertRuleParams()); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("CreateAlertRule with a tenant actor = %v, want %v", err, logic.ErrForbidden)
	}
}

func TestCreateAlertRuleRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *logic.CreateAlertRuleParams)
	}{
		{name: "empty name", mutate: func(p *logic.CreateAlertRuleParams) { p.Name = "" }},
		{name: "unknown metric", mutate: func(p *logic.CreateAlertRuleParams) { p.Metric = "not_a_metric" }},
		{name: "unknown operator", mutate: func(p *logic.CreateAlertRuleParams) { p.Operator = "!=" }},
		{name: "zero consecutive buckets", mutate: func(p *logic.CreateAlertRuleParams) { p.ConsecutiveBuckets = 0 }},
		{name: "negative consecutive buckets", mutate: func(p *logic.CreateAlertRuleParams) { p.ConsecutiveBuckets = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPlatformFixture(t)
			p := validAlertRuleParams()
			tt.mutate(&p)
			if _, err := f.svc.CreateAlertRule(context.Background(), f.actor, p); !errors.Is(err, logic.ErrInvalidInput) {
				t.Errorf("CreateAlertRule(%+v) error = %v, want %v", p, err, logic.ErrInvalidInput)
			}
		})
	}
}

func TestCreateAlertRuleSucceedsAndAudits(t *testing.T) {
	f := newPlatformFixture(t)
	p := validAlertRuleParams()

	rule, err := f.svc.CreateAlertRule(context.Background(), f.actor, p)
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	if rule.ID == "" || rule.Name != p.Name || rule.Metric != p.Metric || rule.Operator != p.Operator {
		t.Errorf("CreateAlertRule() = %+v, want fields matching params %+v", rule, p)
	}

	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, entry := range page.Items {
		if entry.Action == model.ActionAlertRuleCreate && entry.Target == rule.ID && entry.TenantID == model.PlatformScope {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s audit entry recorded under PlatformScope for rule %s", model.ActionAlertRuleCreate, rule.ID)
	}
}

func TestUpdateAlertRuleAuditsAndTranslatesNotFound(t *testing.T) {
	f := newPlatformFixture(t)
	rule, err := f.svc.CreateAlertRule(context.Background(), f.actor, validAlertRuleParams())
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	updated := validAlertRuleParams()
	updated.Name = "high error rate v2"
	updated.Enabled = false
	got, err := f.svc.UpdateAlertRule(context.Background(), f.actor, rule.ID, updated)
	if err != nil {
		t.Fatalf("UpdateAlertRule: %v", err)
	}
	if got.Name != updated.Name || got.Enabled != updated.Enabled {
		t.Errorf("UpdateAlertRule() = %+v, want Name=%q Enabled=%v", got, updated.Name, updated.Enabled)
	}

	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, entry := range page.Items {
		if entry.Action == model.ActionAlertRuleUpdate && entry.Target == rule.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s audit entry recorded for rule %s", model.ActionAlertRuleUpdate, rule.ID)
	}

	if _, err := f.svc.UpdateAlertRule(context.Background(), f.actor, "no_such_rule", validAlertRuleParams()); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("UpdateAlertRule(missing) error = %v, want %v", err, logic.ErrNotFound)
	}
}

func TestDeleteAlertRuleAuditsAndTranslatesNotFound(t *testing.T) {
	f := newPlatformFixture(t)
	rule, err := f.svc.CreateAlertRule(context.Background(), f.actor, validAlertRuleParams())
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	if err := f.svc.DeleteAlertRule(context.Background(), f.actor, rule.ID); err != nil {
		t.Fatalf("DeleteAlertRule: %v", err)
	}
	if _, err := f.svc.GetAlertRule(context.Background(), f.actor, rule.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("GetAlertRule(deleted) error = %v, want %v", err, logic.ErrNotFound)
	}

	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, entry := range page.Items {
		if entry.Action == model.ActionAlertRuleDelete && entry.Target == rule.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s audit entry recorded for rule %s", model.ActionAlertRuleDelete, rule.ID)
	}

	if err := f.svc.DeleteAlertRule(context.Background(), f.actor, rule.ID); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("DeleteAlertRule(already deleted) error = %v, want %v", err, logic.ErrNotFound)
	}
}

func TestListAlertRulesPassesFilterThrough(t *testing.T) {
	f := newPlatformFixture(t)
	enabled := validAlertRuleParams()
	enabled.Name = "enabled rule"
	if _, err := f.svc.CreateAlertRule(context.Background(), f.actor, enabled); err != nil {
		t.Fatalf("CreateAlertRule(enabled): %v", err)
	}
	disabled := validAlertRuleParams()
	disabled.Name = "disabled rule"
	disabled.Enabled = false
	if _, err := f.svc.CreateAlertRule(context.Background(), f.actor, disabled); err != nil {
		t.Fatalf("CreateAlertRule(disabled): %v", err)
	}

	wantEnabled := true
	page, err := f.svc.ListAlertRules(context.Background(), f.actor, store.ListQuery{}, store.AlertRuleFilter{Enabled: &wantEnabled})
	if err != nil {
		t.Fatalf("ListAlertRules(enabled): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Name != enabled.Name {
		t.Errorf("ListAlertRules(Enabled=true) = %+v, want only %q", page.Items, enabled.Name)
	}

	wantDisabled := false
	page, err = f.svc.ListAlertRules(context.Background(), f.actor, store.ListQuery{}, store.AlertRuleFilter{Enabled: &wantDisabled})
	if err != nil {
		t.Fatalf("ListAlertRules(disabled): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Name != disabled.Name {
		t.Errorf("ListAlertRules(Enabled=false) = %+v, want only %q", page.Items, disabled.Name)
	}
}

func TestListAlertRulesRejectsANonPlatformActor(t *testing.T) {
	f := newPlatformFixture(t)
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if _, err := f.svc.ListAlertRules(context.Background(), tenantActor, store.ListQuery{}, store.AlertRuleFilter{}); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("ListAlertRules with a tenant actor = %v, want %v", err, logic.ErrForbidden)
	}
}
