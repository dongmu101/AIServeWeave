package gormstore_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

func TestLiveAlertRuleCRUD(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}

		rule := model.AlertRule{
			ID: "alr_live1", Name: "success rate drop", Metric: model.MetricSuccessRate,
			Operator: model.OperatorLessThan, Threshold: 0.95, ConsecutiveBuckets: 3, Enabled: true,
			CreatedAt: time.Now().UTC().Truncate(time.Millisecond), UpdatedAt: time.Now().UTC().Truncate(time.Millisecond),
		}
		if err := st.CreateAlertRule(ctx, &rule); err != nil {
			t.Fatalf("CreateAlertRule() error = %v", err)
		}

		updated, err := st.UpdateAlertRule(ctx, rule.ID, store.AlertRuleUpdate{
			Name: "success rate drop v2", Metric: rule.Metric, Operator: rule.Operator,
			Threshold: 0.9, ConsecutiveBuckets: 2, WebhookURL: "https://example.test/hook", Enabled: false,
		})
		if err != nil {
			t.Fatalf("UpdateAlertRule() error = %v", err)
		}
		if updated.Threshold != 0.9 || updated.Enabled {
			t.Fatalf("UpdateAlertRule() = %+v, want Threshold=0.9 Enabled=false", updated)
		}

		enabled := true
		page, err := st.ListAlertRules(ctx, store.ListQuery{}, store.AlertRuleFilter{Enabled: &enabled})
		if err != nil {
			t.Fatalf("ListAlertRules(enabled) error = %v", err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("ListAlertRules(enabled=true) = %+v, want none (rule was disabled by the update)", page.Items)
		}

		if err := st.DeleteAlertRule(ctx, rule.ID); err != nil {
			t.Fatalf("DeleteAlertRule() error = %v", err)
		}
		if err := st.DeleteAlertRule(ctx, rule.ID); err != store.ErrNotFound {
			t.Fatalf("DeleteAlertRule() on an already-deleted rule error = %v, want store.ErrNotFound", err)
		}
	})
}

func TestLiveAlertInstanceLifecycleAndAcknowledgeConflict(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}
		rule := model.AlertRule{ID: "alr_live2", Name: "r", Metric: model.MetricRequestRate, Operator: model.OperatorGreaterThan, Threshold: 100, ConsecutiveBuckets: 1, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := st.CreateAlertRule(ctx, &rule); err != nil {
			t.Fatalf("CreateAlertRule() error = %v", err)
		}

		now := time.Now().UTC().Truncate(time.Millisecond)
		instance := model.AlertInstance{ID: "ali_live1", RuleID: rule.ID, Status: model.AlertStatusFiring, ValueAtFire: 150, CreatedAt: now, LastEvaluatedAt: now, NotifyStatus: model.NotifyStatusPending}
		if err := st.CreateAlertInstance(ctx, &instance); err != nil {
			t.Fatalf("CreateAlertInstance() error = %v", err)
		}

		got, ok, err := st.GetOpenAlertInstance(ctx, rule.ID)
		if err != nil || !ok || got.ID != instance.ID {
			t.Fatalf("GetOpenAlertInstance() = (%+v, %v, %v), want the firing instance", got, ok, err)
		}

		acked, err := st.AcknowledgeAlertInstance(ctx, instance.ID, "usr_1", now.Add(time.Minute))
		if err != nil || acked.Status != model.AlertStatusAcknowledged {
			t.Fatalf("AcknowledgeAlertInstance() = (%+v, %v), want Status=acknowledged", acked, err)
		}

		if err := st.ResolveAlertInstance(ctx, instance.ID, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("ResolveAlertInstance() error = %v", err)
		}

		if _, err := st.AcknowledgeAlertInstance(ctx, instance.ID, "usr_1", now.Add(3*time.Minute)); err != store.ErrConflict {
			t.Fatalf("AcknowledgeAlertInstance() on a resolved instance error = %v, want store.ErrConflict", err)
		}

		_, ok, err = st.GetOpenAlertInstance(ctx, rule.ID)
		if err != nil {
			t.Fatalf("GetOpenAlertInstance() after resolve error = %v", err)
		}
		if ok {
			t.Fatal("GetOpenAlertInstance() after resolve reports an open instance, want none")
		}
	})
}
