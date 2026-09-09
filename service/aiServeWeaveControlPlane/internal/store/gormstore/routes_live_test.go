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

	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// TestLiveRoutes checks actual engine CAS and atomic audit on disposable databases.
// TestLiveRoutes 在可丢弃数据库上校验真实引擎的 CAS 与原子审计。
func TestLiveRoutes(t *testing.T) {
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
			if _, err := st.MigrateRoutes(ctx); err != nil {
				t.Fatal(err)
			}
			if applied, err := st.MigrateRoutes(ctx); err != nil || len(applied) != 0 {
				t.Fatalf("repeat applied=%v err=%v want empty", applied, err)
			}
			if err := db.Exec("DELETE FROM route_revisions").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.RouteActive{}).Where("id = 1").Update("revision", 0).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := st.CurrentRouteRevision(ctx); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("initial err=%v want not found", err)
			}
			routes := []modelroute.Route{{Model: "alias", Targets: []modelroute.Target{{RuntimeModel: "backend", Weight: 3, Priority: 2, NodeSelector: map[string]string{"large": strings.Repeat("x", 70*1024)}}}}}
			body, err := modelroute.Canonical(routes)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := modelroute.Digest(routes)
			if err != nil {
				t.Fatal(err)
			}
			var wins atomic.Int32
			var wg sync.WaitGroup
			for range 20 {
				wg.Go(func() {
					row := model.RouteRevision{Digest: digest, RoutesJSON: string(body), ActorID: "operator", CreatedAt: time.Now().UTC()}
					audit := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, ActorID: "operator", Action: "routes.publish"}
					err := gormstore.New(db).PublishRouteRevision(ctx, 0, &row, &audit)
					if err == nil {
						wins.Add(1)
					} else if !errors.Is(err, store.ErrConflict) {
						t.Errorf("CAS err=%v want conflict", err)
					}
				})
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("CAS winners=%d want1", wins.Load())
			}
			current, err := st.CurrentRouteRevision(ctx)
			if err != nil || current.Revision != 1 {
				t.Fatalf("current=%+v err=%v want revision1", current, err)
			}
			duplicate := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, Action: "routes.test"}
			if err := st.AppendAudit(ctx, &duplicate); err != nil {
				t.Fatal(err)
			}
			candidate := model.RouteRevision{Digest: "changed", RoutesJSON: "[]", ActorID: "operator", CreatedAt: time.Now().UTC()}
			if err := st.PublishRouteRevision(ctx, 1, &candidate, &duplicate); err == nil {
				t.Fatal("duplicate audit succeeded; want transaction failure")
			}
			current, err = st.CurrentRouteRevision(ctx)
			if err != nil || current.Revision != 1 {
				t.Fatalf("after audit failure revision=%d err=%v want1", current.Revision, err)
			}
			if _, err := st.GetRouteRevision(ctx, 2); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed revision err=%v want not found", err)
			}
			service := logic.New(st, nil)
			actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
			rolled, err := service.RollbackRoutes(ctx, actor, 1, 1)
			if err != nil || rolled.Revision != 2 || rolled.RollbackOf != 1 || rolled.Digest != digest || len(rolled.Routes[0].Targets[0].NodeSelector["large"]) != 70*1024 {
				t.Fatalf("rollback revision=%d source=%d err=%v want2/1 and intact content", rolled.Revision, rolled.RollbackOf, err)
			}
			history, err := service.RoutesHistory(ctx, 0, 1)
			if err != nil || len(history.Items) != 1 || history.Items[0].Revision != 2 || history.NextBefore != 2 {
				t.Fatalf("history=%+v err=%v want revision2/cursor2", history, err)
			}
			if err := db.Model(&model.RouteActive{}).Where("id = 1").Update("revision", modelroute.MaxRevisions).Error; err != nil {
				t.Fatal(err)
			}
			duplicate.ID = model.NewID(model.PrefixAuditLog)
			if err := st.PublishRouteRevision(ctx, modelroute.MaxRevisions, &candidate, &duplicate); !errors.Is(err, store.ErrRouteCapacity) {
				t.Fatalf("capacity err=%v want capacity", err)
			}
			if err := db.Model(&model.RouteActive{}).Where("id = 1").Update("revision", 2).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}
