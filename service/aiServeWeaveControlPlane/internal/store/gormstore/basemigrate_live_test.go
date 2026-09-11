package gormstore_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

func migrationDatabases(t *testing.T, test func(*testing.T, *gorm.DB, string)) {
	t.Helper()
	for _, engine := range []struct{ name, env string }{
		{name: "postgres", env: "AISW_POSTGRES_TEST_DSN"},
		{name: "mysql", env: "AISW_MYSQL_TEST_DSN"},
	} {
		t.Run(engine.name, func(t *testing.T) {
			dsn := os.Getenv(engine.env)
			if dsn == "" {
				t.Skipf("set %s to run isolated migration tests", engine.env)
			}
			open := func(dsn string) *gorm.DB {
				t.Helper()
				var dialect gorm.Dialector = postgres.Open(dsn)
				if engine.name == "mysql" {
					dialect = mysql.Open(dsn)
				}
				db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
				if err != nil {
					t.Fatal("opening disposable database failed")
				}
				pool, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pool.Close() })
				return db
			}
			admin := open(dsn)
			name := "aisw_p07_" + strings.TrimPrefix(model.NewID(model.PrefixTenant), model.PrefixTenant)
			if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
				t.Fatalf("create disposable database: %v", err)
			}
			t.Cleanup(func() {
				if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
					t.Errorf("drop disposable database: %v", err)
				}
			})
			if engine.name == "mysql" {
				cfg, err := mysqldriver.ParseDSN(dsn)
				if err != nil {
					t.Fatal("invalid test DSN")
				}
				cfg.DBName = name
				dsn = cfg.FormatDSN()
			} else {
				u, err := url.Parse(dsn)
				if err != nil || u.Scheme == "" {
					t.Fatal("migration tests require a PostgreSQL URL DSN")
				}
				u.Path = "/" + name
				dsn = u.String()
			}
			test(t, open(dsn), dsn)
		})
	}
}

func TestLiveBaseMigrationRecordsVersions(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		st := gormstore.New(db)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate() = %v, want nil", err)
		}
		var count int64
		if err := db.Table("schema_migrations_base").Count(&count).Error; err != nil || count == 0 {
			t.Fatalf("migration history count = %d, error = %v; want recorded versions", count, err)
		}
	})
}

func TestLiveDatabaseUpgradePreservesExistingData(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		// Reproduce the released AutoMigrate schema only as an upgrade fixture.
		// 仅作为升级夹具，重现已发布的 AutoMigrate 表结构。
		if err := db.AutoMigrate(&model.Tenant{}, &model.User{}, &model.PlatformOperator{}, &model.APIKey{}, &model.AuditLog{}); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
		tenant := model.Tenant{ID: "tnt_legacy", Name: "existing tenant", Status: model.StatusActive, RequestsPerMinute: 123, TokensPerMinute: 456, MaxConcurrent: 7, CreatedAt: at, UpdatedAt: at}
		user := model.User{ID: "usr_legacy", TenantID: tenant.ID, Email: "legacy@example.com", PasswordHash: "existing-password-digest", Role: model.RoleOwner, Status: model.StatusActive, LastLoginAt: &at, CreatedAt: at, UpdatedAt: at}
		key := model.APIKey{ID: "key_legacy", TenantID: tenant.ID, CreatedBy: user.ID, Name: "existing key", Hash: strings.Repeat("a", 64), Display: "aisw-existing", Status: model.StatusRevoked, RevokedAt: &at, CreatedAt: at, UpdatedAt: at}
		audit := model.AuditLog{ID: "audit_legacy", TenantID: tenant.ID, ActorID: user.ID, Action: model.ActionAPIKeyRevoke, Target: key.ID, CreatedAt: at}
		for _, value := range []any{&tenant, &user, &key, &audit} {
			if err := db.Create(value).Error; err != nil {
				t.Fatal(err)
			}
		}
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.CheckSchema(ctx); err == nil {
			t.Fatal("CheckSchema() = nil for unversioned database, want error")
		}
		for i := 0; i < 2; i++ {
			if err := st.MigrateAll(ctx, false); err != nil {
				t.Fatalf("MigrateAll attempt %d = %v, want nil", i, err)
			}
			if err := st.CheckSchema(ctx); err != nil {
				t.Fatalf("CheckSchema() = %v, want nil", err)
			}
		}
		gotTenant, err := st.GetTenant(ctx, tenant.ID)
		if err != nil || gotTenant.Name != tenant.Name || gotTenant.Limits() != tenant.Limits() {
			t.Fatalf("tenant limits = %v, err = %v; want %v", gotTenant.Limits(), err, tenant.Limits())
		}
		gotUser, err := st.GetUserByEmail(ctx, user.Email)
		if err != nil || gotUser.PasswordHash != user.PasswordHash || gotUser.LastLoginAt == nil || !gotUser.LastLoginAt.Equal(at) {
			t.Fatalf("existing user preserved = false, error = %v", err)
		}
		gotKey, err := st.GetAPIKey(ctx, tenant.ID, key.ID)
		if err != nil || gotKey.Status != model.StatusRevoked || gotKey.Hash != key.Hash || gotKey.RevokedAt == nil || !gotKey.RevokedAt.Equal(at) {
			t.Fatalf("existing revoked key preserved = false, error = %v", err)
		}
		var count int64
		if err := db.Model(&model.AuditLog{}).Where("id = ?", audit.ID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("audit count = %d, err = %v; want 1", count, err)
		}
		duplicate := user
		duplicate.ID = "usr_duplicate"
		if err := db.Create(&duplicate).Error; err == nil {
			t.Fatal("duplicate email accepted after upgrade, want unique violation")
		}
	})
}

func TestLiveMigrationFailureRequiresSafeRecovery(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		if err := db.AutoMigrate(&model.Tenant{}, &model.User{}, &model.PlatformOperator{}, &model.APIKey{}, &model.AuditLog{}); err != nil {
			t.Fatal(err)
		}
		if err := dropMigrationTestIndex(db, "users", "idx_users_email"); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"usr_keep", "usr_conflict"} {
			if err := db.Create(&model.User{ID: id, TenantID: "tnt_old", Email: "duplicate@example.com", PasswordHash: "digest", Role: model.RoleMember, Status: model.StatusActive}).Error; err != nil {
				t.Fatal(err)
			}
		}
		// Missing historical columns are legitimate additive upgrades.
		// 缺失的历史增量列应能够通过增量迁移补齐。
		for _, pair := range [][2]string{{"tenants", "requests_per_minute"}, {"tenants", "tokens_per_minute"}, {"tenants", "max_concurrent"}, {"users", "last_login_at"}, {"platform_operators", "last_login_at"}} {
			if err := db.Migrator().DropColumn(pair[0], pair[1]); err != nil {
				t.Fatal(err)
			}
		}
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err == nil {
			t.Fatal("MigrateAll() accepted duplicate legacy emails, want error")
		}
		var dirty int64
		if err := db.Table("schema_migrations_base").Where("dirty = ?", true).Count(&dirty).Error; err != nil {
			t.Fatal(err)
		}
		wantDirty := int64(0)
		if db.Name() == "mysql" {
			wantDirty = 1
		}
		if dirty != wantDirty {
			t.Fatalf("dirty migrations = %d, want %d", dirty, wantDirty)
		}
		if err := db.Where("id = ?", "usr_conflict").Delete(&model.User{}).Error; err != nil {
			t.Fatal(err)
		}
		if db.Name() == "mysql" {
			if err := st.MigrateAll(ctx, false); err == nil {
				t.Fatal("ordinary up resumed dirty migration, want refusal")
			}
		}
		if err := st.MigrateAll(ctx, true); err != nil {
			t.Fatalf("explicit resume = %v, want nil", err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatalf("CheckSchema after repair = %v, want nil", err)
		}
	})
}

func TestLiveMigrationRejectsChangedHistoryAndSchema(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			name, column string
			value        any
		}{
			{name: "checksum", column: "checksum", value: "modified"},
			{name: "cleared checksum", column: "checksum", value: ""},
			{name: "dirty", column: "dirty", value: true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				err := db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Table("schema_migrations_base").Where("id = ?", "0009_key_revocation_outbox.sql").Update(tt.column, tt.value).Error; err != nil {
						t.Fatal(err)
					}
					if err := gormstore.New(tx).CheckSchema(ctx); err == nil {
						t.Errorf("CheckSchema accepted %s history, want error", tt.name)
					}
					return gorm.ErrInvalidTransaction
				})
				if err != gorm.ErrInvalidTransaction {
					t.Fatal(err)
				}
			})
		}
		if err := db.Exec("INSERT INTO schema_migrations_base (id, applied_at, checksum, dirty) VALUES (?, ?, ?, ?)", "9999_future.sql", time.Now(), strings.Repeat("0", 64), false).Error; err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err == nil {
			t.Fatal("future schema accepted, want error")
		}
		if err := st.MigrateAll(ctx, true); err == nil {
			t.Fatal("resume accepted future schema, want error")
		}
		if err := db.Exec("DELETE FROM schema_migrations_base WHERE id = ?", "9999_future.sql").Error; err != nil {
			t.Fatal(err)
		}
		if err := dropMigrationTestIndex(db, "api_keys", "idx_api_keys_hash"); err != nil {
			t.Fatal(err)
		}
		if err := st.CheckSchema(ctx); err == nil {
			t.Fatal("missing unique key index accepted, want error")
		}
	})
}

func TestLiveMigrationAdoptsLegacyLedgersAndPartialJobDDL(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		seedRecoveryData(t, db)
		groups := []string{"routes", "workflow_templates"}
		if db.Name() == "mysql" {
			groups = append(groups, "jobs")
		}
		for _, group := range groups {
			for _, column := range []string{"checksum", "dirty"} {
				if err := db.Migrator().DropColumn("schema_migrations_"+group, column); err != nil {
					t.Fatal(err)
				}
			}
		}
		if db.Name() == "mysql" {
			if err := db.Exec("DELETE FROM schema_migrations_jobs WHERE id >= ?", "0003").Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := st.CheckSchema(ctx); err == nil {
			t.Fatal("legacy ledger passed startup check, want migration required")
		}
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("legacy/partial DDL upgrade = %v, want nil", err)
		}
		if err := st.CheckSchema(ctx); err != nil {
			t.Fatalf("adopted schema = %v, want nil", err)
		}
		if db.Name() == "mysql" {
			var artifact model.JobArtifact
			if err := db.Where("id = ?", "artifact_backup").Take(&artifact).Error; err != nil {
				t.Fatal(err)
			}
			if artifact.StorageKey != "outputs/backup" || artifact.SizeBytes != 1234 {
				t.Fatal("replayed Job DDL changed existing artifact data")
			}
		}
	})
}

func TestLiveConcurrentMigrationsAreSerialized(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		start := make(chan struct{})
		results := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() { <-start; results <- gormstore.New(db).MigrateAll(context.Background(), false) })
		}
		close(start)
		wg.Wait()
		close(results)
		passed := 0
		for err := range results {
			if err == nil {
				passed++
				continue
			}
			if !strings.Contains(err.Error(), "migration lock unavailable") {
				t.Errorf("concurrent migration error = %v, want lock unavailable", err)
			}
		}
		if passed == 0 {
			t.Fatal("successful migrations = 0, want at least one")
		}
		if err := gormstore.New(db).CheckSchema(context.Background()); err != nil {
			t.Fatalf("CheckSchema = %v, want nil", err)
		}
	})
}

func dropMigrationTestIndex(db *gorm.DB, table, index string) error {
	if db.Name() == "postgres" {
		return db.Exec("DROP INDEX " + index).Error
	}
	return db.Migrator().DropIndex(table, index)
}

func TestLiveSchemaRejectsContractDrift(t *testing.T) {
	for _, scenario := range []string{"widened primary key", "narrowed publication body", "nullable outbox generation"} {
		t.Run(scenario, func(t *testing.T) {
			migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
				st := gormstore.New(db)
				ctx := context.Background()
				if err := st.MigrateAll(ctx, false); err != nil {
					t.Fatal(err)
				}
				var statements []string
				switch scenario {
				case "widened primary key":
					if db.Name() == "postgres" {
						statements = []string{"ALTER TABLE api_keys DROP CONSTRAINT api_keys_pkey", "ALTER TABLE api_keys ADD PRIMARY KEY (id, tenant_id)"}
					} else {
						statements = []string{"ALTER TABLE jobs DROP PRIMARY KEY, ADD PRIMARY KEY (id, tenant_id)"}
					}
				case "narrowed publication body":
					if db.Name() == "postgres" {
						statements = []string{"ALTER TABLE route_revisions ALTER COLUMN routes_json TYPE VARCHAR(255)"}
					} else {
						statements = []string{"ALTER TABLE workflow_template_revisions MODIFY graph_json TEXT NOT NULL"}
					}
				case "nullable outbox generation":
					if db.Name() == "postgres" {
						statements = []string{"ALTER TABLE key_revocation_outbox ALTER COLUMN generation DROP NOT NULL"}
					} else {
						statements = []string{"ALTER TABLE key_revocation_outbox MODIFY generation BIGINT NULL"}
					}
					statements = append(statements, "UPDATE key_revocation_outbox SET generation = NULL WHERE id = 1")
				}
				for _, statement := range statements {
					if err := db.Exec(statement).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := st.CheckSchema(ctx); err == nil {
					t.Fatalf("CheckSchema accepted %s, want refusal", scenario)
				}
			})
		})
	}
}
