package gormstore_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

func lifecycleStores(t *testing.T, test func(*testing.T, *gormstore.Store)) {
	t.Helper()
	engines := []struct {
		name string
		env  string
		open func(string) gorm.Dialector
	}{
		{name: "postgres", env: "AISW_POSTGRES_TEST_DSN", open: func(dsn string) gorm.Dialector { return postgres.Open(dsn) }},
		{name: "mysql", env: "AISW_MYSQL_TEST_DSN", open: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
	}
	for _, engine := range engines {
		t.Run(engine.name, func(t *testing.T) {
			dsn := os.Getenv(engine.env)
			if dsn == "" {
				t.Skipf("set %s to run this test", engine.env)
			}
			db, err := gorm.Open(engine.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
			if err != nil {
				t.Fatalf("gorm.Open: %v", err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatalf("db.DB: %v", err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			st := gormstore.New(db)
			if err := st.Migrate(context.Background()); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			for _, table := range []string{"api_keys", "users", "platform_operators", "audit_logs", "tenants"} {
				if err := db.Exec("DELETE FROM " + table).Error; err != nil {
					t.Fatalf("clearing %s: %v", table, err)
				}
			}
			test(t, st)
		})
	}
}

func TestLiveUserLifecyclePreservesOwnerAndRevokesKeys(t *testing.T) {
	lifecycleStores(t, func(t *testing.T, st *gormstore.Store) {
		ctx := context.Background()
		tenantID := model.NewID(model.PrefixTenant)
		if err := st.CreateTenant(ctx, &model.Tenant{ID: tenantID, Name: "lifecycle", Status: model.StatusActive}); err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}
		owners := []*model.User{
			{ID: model.NewID(model.PrefixUser), TenantID: tenantID, Email: model.NewID(model.PrefixUser) + "@example.com", PasswordHash: "digest", Role: model.RoleOwner, Status: model.StatusActive},
			{ID: model.NewID(model.PrefixUser), TenantID: tenantID, Email: model.NewID(model.PrefixUser) + "@example.com", PasswordHash: "digest", Role: model.RoleOwner, Status: model.StatusActive},
		}
		for _, owner := range owners {
			if err := st.CreateUser(ctx, owner); err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
		}
		keys := make([]*model.APIKey, len(owners))
		for i, owner := range owners {
			keys[i] = &model.APIKey{ID: model.NewID(model.PrefixAPIKey), TenantID: tenantID, CreatedBy: owner.ID, Name: "key", Hash: owner.ID + owner.ID, Display: "aisw-live", Status: model.StatusActive}
			if err := st.CreateAPIKey(ctx, keys[i]); err != nil {
				t.Fatalf("CreateAPIKey: %v", err)
			}
		}

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for i, owner := range owners {
			wg.Add(1)
			go func(i int, owner *model.User) {
				defer wg.Done()
				<-start
				_, err := st.SetUserStatus(ctx, tenantID, owner.ID, model.StatusSuspended, time.Now(), model.AuditLog{
					ID: model.NewID(model.PrefixAuditLog), TenantID: tenantID, Action: model.ActionUserDisable, Target: owner.ID, CreatedAt: time.Now(),
				})
				errs <- err
			}(i, owner)
		}
		close(start)
		wg.Wait()
		close(errs)
		var successes, conflicts int
		for err := range errs {
			switch {
			case err == nil:
				successes++
			case errors.Is(err, store.ErrConflict):
				conflicts++
			default:
				t.Errorf("SetUserStatus error = %v, want nil or %v", err, store.ErrConflict)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Errorf("results = %d successes, %d conflicts; want 1 and 1", successes, conflicts)
		}
		for i, owner := range owners {
			storedUser, err := st.GetUser(ctx, tenantID, owner.ID)
			if err != nil {
				t.Fatalf("GetUser: %v", err)
			}
			storedKey, err := st.GetAPIKey(ctx, tenantID, keys[i].ID)
			if err != nil {
				t.Fatalf("GetAPIKey: %v", err)
			}
			wantStatus := model.StatusActive
			if storedUser.Status == model.StatusSuspended {
				wantStatus = model.StatusRevoked
			}
			if storedKey.Status != wantStatus {
				t.Errorf("key[%d] status = %q, want %q for user status %q", i, storedKey.Status, wantStatus, storedUser.Status)
			}
		}
	})
}

func TestLivePlatformLifecyclePreservesOneActiveOperator(t *testing.T) {
	lifecycleStores(t, func(t *testing.T, st *gormstore.Store) {
		ctx := context.Background()
		operators := []*model.PlatformOperator{
			{ID: model.NewID(model.PrefixPlatformOperator), Email: model.NewID(model.PrefixPlatformOperator) + "@example.com", PasswordHash: "digest", Status: model.StatusActive},
			{ID: model.NewID(model.PrefixPlatformOperator), Email: model.NewID(model.PrefixPlatformOperator) + "@example.com", PasswordHash: "digest", Status: model.StatusActive},
		}
		for _, operator := range operators {
			if err := st.CreatePlatformOperator(ctx, operator); err != nil {
				t.Fatalf("CreatePlatformOperator: %v", err)
			}
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, operator := range operators {
			wg.Add(1)
			go func(operator *model.PlatformOperator) {
				defer wg.Done()
				<-start
				_, err := st.SetPlatformOperatorStatus(ctx, operator.ID, model.StatusSuspended, time.Now(), model.AuditLog{
					ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, Action: model.ActionPlatformOperatorDisable, Target: operator.ID, CreatedAt: time.Now(),
				})
				errs <- err
			}(operator)
		}
		close(start)
		wg.Wait()
		close(errs)
		var successes, conflicts int
		for err := range errs {
			switch {
			case err == nil:
				successes++
			case errors.Is(err, store.ErrConflict):
				conflicts++
			default:
				t.Errorf("SetPlatformOperatorStatus error = %v, want nil or %v", err, store.ErrConflict)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Errorf("results = %d successes, %d conflicts; want 1 and 1", successes, conflicts)
		}
	})
}
