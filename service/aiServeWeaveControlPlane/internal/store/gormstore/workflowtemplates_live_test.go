package gormstore_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// TestLiveWorkflowTemplates checks actual engine CAS-on-create, per-template
// independent versioning and atomic audit on disposable databases (P03),
// mirroring TestLiveRoutes's coverage for the single global routing table.
//
// TestLiveWorkflowTemplates 在可丢弃数据库上校验真实引擎的「创建时 CAS」、按模板
// 独立版本化与原子审计（P03），覆盖面对照 TestLiveRoutes 之于单一全局路由表的
// 覆盖面。
func TestLiveWorkflowTemplates(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{{"postgres", "AISW_POSTGRES_TEST_DSN", postgres.Open}, {"mysql", "AISW_MYSQL_TEST_DSN", mysql.Open}} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skip("set " + tc.env + " to a disposable database")
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			sqlDB.SetMaxOpenConns(20)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			st := gormstore.New(db)
			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := st.MigrateWorkflowTemplates(ctx); err != nil {
				t.Fatal(err)
			}
			if applied, err := st.MigrateWorkflowTemplates(ctx); err != nil || len(applied) != 0 {
				t.Fatalf("repeat applied=%v err=%v want empty", applied, err)
			}
			if err := db.Exec("DELETE FROM workflow_template_revisions").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("DELETE FROM workflow_template_actives").Error; err != nil {
				t.Fatal(err)
			}
			if _, err := st.CurrentWorkflowTemplateRevision(ctx, "alpha"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("initial err=%v want not found", err)
			}

			graph := `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":"` + strings.Repeat("x", 70*1024) + `"}}}`
			content := workflowtemplate.Content{Description: "d", Inputs: []workflowtemplate.Input{}, Graph: []byte(graph)}
			realDigest, err := workflowtemplate.Digest("alpha", content, nil)
			if err != nil {
				t.Fatal(err)
			}

			// A concurrent first publish of the same never-before-seen template
			// id must produce exactly one winner: the CAS-on-create path (no
			// seeded pointer row exists yet, unlike routes' singleton) is the
			// one piece of behavior this table has that routes does not.
			//
			// 对同一个从未出现过的模板 id 并发首次发布，必须恰好产生一个赢家：
			// 「创建时 CAS」路径（此刻尚无预先播种的指针行，与路由的单例不同）是这张
			// 表独有、路由没有的那一段行为。
			var wins atomic.Int32
			var wg sync.WaitGroup
			for range 20 {
				wg.Go(func() {
					row := model.WorkflowTemplateRevision{Digest: realDigest, Description: "d", InputsJSON: "[]", OutputsJSON: "[]", DependenciesJSON: "{}", VisibleTenantsJSON: "[]", GraphJSON: graph, ActorID: "operator", CreatedAt: time.Now().UTC()}
					audit := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, ActorID: "operator", Action: "workflow_templates.publish"}
					err := gormstore.New(db).PublishWorkflowTemplateRevision(ctx, "alpha", 0, &row, &audit)
					if err == nil {
						wins.Add(1)
					} else if !errors.Is(err, store.ErrConflict) {
						t.Errorf("CAS-on-create err=%v want conflict", err)
					}
				})
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("CAS-on-create winners=%d want 1", wins.Load())
			}
			current, err := st.CurrentWorkflowTemplateRevision(ctx, "alpha")
			if err != nil || current.Revision != 1 {
				t.Fatalf("current=%+v err=%v want revision 1", current, err)
			}

			// A second, independent template id publishes its own revision 1,
			// unaffected by "alpha" already being at revision 1.
			//
			// 第二个、独立的模板 id 发布自己的版本 1，不受"alpha"已处于版本 1
			// 的影响。
			betaRow := model.WorkflowTemplateRevision{Digest: realDigest, Description: "d", InputsJSON: "[]", OutputsJSON: "[]", DependenciesJSON: "{}", VisibleTenantsJSON: "[]", GraphJSON: graph, ActorID: "operator", CreatedAt: time.Now().UTC()}
			betaAudit := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, ActorID: "operator", Action: "workflow_templates.publish"}
			if err := st.PublishWorkflowTemplateRevision(ctx, "beta", 0, &betaRow, &betaAudit); err != nil {
				t.Fatalf("beta first publish err=%v want nil", err)
			}
			if betaRow.Revision != 1 {
				t.Fatalf("beta revision=%d want 1, independent from alpha's own counter", betaRow.Revision)
			}

			duplicate := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, Action: "workflow_templates.test"}
			if err := st.AppendAudit(ctx, &duplicate); err != nil {
				t.Fatal(err)
			}
			candidate := model.WorkflowTemplateRevision{Digest: "changed", InputsJSON: "[]", OutputsJSON: "[]", DependenciesJSON: "{}", VisibleTenantsJSON: "[]", GraphJSON: "{}", ActorID: "operator", CreatedAt: time.Now().UTC()}
			if err := st.PublishWorkflowTemplateRevision(ctx, "alpha", 1, &candidate, &duplicate); err == nil {
				t.Fatal("duplicate audit succeeded; want transaction failure")
			}
			current, err = st.CurrentWorkflowTemplateRevision(ctx, "alpha")
			if err != nil || current.Revision != 1 {
				t.Fatalf("after audit failure revision=%d err=%v want 1", current.Revision, err)
			}
			if _, err := st.GetWorkflowTemplateRevision(ctx, "alpha", 2); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed revision err=%v want not found", err)
			}

			service := logic.New(st, nil)
			actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
			rolled, err := service.RollbackWorkflowTemplate(ctx, actor, "alpha", 1, 1)
			if err != nil || rolled.Revision != 2 || rolled.RollbackOf != 1 || rolled.Digest != realDigest || len(string(rolled.Graph)) != len(graph) {
				t.Fatalf("rollback revision=%d source=%d err=%v want 2/1 and intact content", rolled.Revision, rolled.RollbackOf, err)
			}
			history, err := service.WorkflowTemplateHistoryPage(ctx, "alpha", 0, 1)
			if err != nil || len(history.Items) != 1 || history.Items[0].Revision != 2 || history.NextBefore != 2 {
				t.Fatalf("history=%+v err=%v want revision 2/cursor 2", history, err)
			}

			if err := db.Model(&model.WorkflowTemplateActive{}).Where("template_id = ?", "alpha").Update("revision", workflowtemplate.MaxRevisionsPerTemplate).Error; err != nil {
				t.Fatal(err)
			}
			duplicate.ID = model.NewID(model.PrefixAuditLog)
			if err := st.PublishWorkflowTemplateRevision(ctx, "alpha", workflowtemplate.MaxRevisionsPerTemplate, &candidate, &duplicate); !errors.Is(err, store.ErrWorkflowTemplateCapacity) {
				t.Fatalf("capacity err=%v want capacity", err)
			}
			if err := db.Model(&model.WorkflowTemplateActive{}).Where("template_id = ?", "alpha").Update("revision", 2).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}
