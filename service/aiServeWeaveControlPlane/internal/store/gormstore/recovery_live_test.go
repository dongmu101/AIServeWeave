package gormstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/cache"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

func TestLiveNativeDatabaseBackupRestore(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dsn string) {
		container := os.Getenv("AISW_" + strings.ToUpper(db.Name()) + "_TEST_CONTAINER")
		if container == "" {
			t.Skip("set the engine's AISW_*_TEST_CONTAINER for native dump/restore")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		seedRecoveryData(t, db)
		before := databaseSnapshot(t, db)
		var sourceName, user, password string
		var restoredDialect gorm.Dialector
		restoreName := "aisw_p07_restore_" + strings.TrimPrefix(model.NewID(model.PrefixTenant), model.PrefixTenant)
		if db.Name() == "mysql" {
			cfg, err := mysqldriver.ParseDSN(dsn)
			if err != nil {
				t.Fatal("invalid test DSN")
			}
			sourceName, user, password = cfg.DBName, cfg.User, cfg.Passwd
			cfg.DBName = restoreName
			restoredDialect = mysql.Open(cfg.FormatDSN())
		} else {
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal("invalid test DSN")
			}
			sourceName, user = strings.TrimPrefix(u.Path, "/"), u.User.Username()
			password, _ = u.User.Password()
			u.Path = "/" + restoreName
			restoredDialect = postgres.Open(u.String())
		}
		if err := db.Exec("CREATE DATABASE " + restoreName).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Exec("DROP DATABASE " + restoreName).Error; err != nil {
				t.Error(err)
			}
		})
		native := func(input io.Reader, output io.Writer, args ...string) error {
			variable := "PGPASSWORD"
			if db.Name() == "mysql" {
				variable = "MYSQL_PWD"
			}
			command := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", "-e", variable, container}, args...)...)
			command.Env = append(os.Environ(), variable+"="+password)
			command.Stdin, command.Stdout = input, output
			// Native diagnostics may contain restored data; expose only the exit status.
			// 原生工具诊断可能包含恢复的数据，因此仅暴露退出状态。
			command.Stderr = io.Discard
			return command.Run()
		}
		backupPath := filepath.Join(t.TempDir(), "database.dump")
		file, err := os.OpenFile(backupPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		var restoreArgs []string
		if db.Name() == "postgres" {
			err = native(nil, file, "pg_dump", "-U", user, "--format=custom", "--no-owner", "--no-acl", sourceName)
			restoreArgs = []string{"pg_restore", "-U", user, "--exit-on-error", "--single-transaction", "--no-owner", "--no-acl", "-d", restoreName}
		} else {
			err = native(nil, file, "mysqldump", "--user="+user, "--single-transaction", "--quick", "--no-tablespaces", "--set-gtid-purged=OFF", "--skip-add-drop-table", sourceName)
			restoreArgs = []string{"mysql", "--user=" + user, "--database=" + restoreName}
		}
		if err != nil {
			t.Fatalf("native backup = %v, want success", err)
		}
		if err := native(strings.NewReader("INVALID BACKUP;\n"), io.Discard, restoreArgs...); err == nil {
			t.Fatal("invalid backup restored successfully, want nonzero exit")
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		if err := native(file, io.Discard, restoreArgs...); err != nil {
			t.Fatalf("native restore = %v, want success", err)
		}
		restored, err := gorm.Open(restoredDialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
		if err != nil {
			t.Fatal("opening restored database failed")
		}
		pool, err := restored.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		recovered := gormstore.New(restored)
		if err := recovered.CheckSchema(ctx); err != nil {
			t.Fatalf("restored CheckSchema = %v, want nil", err)
		}
		after := databaseSnapshot(t, restored)
		if !bytes.Equal(before, after) {
			t.Fatal("restored database differs from original business rows or migration history")
		}
		published := 0
		if flushed, err := recovered.FlushRevocations(ctx, func(context.Context) error { published++; return nil }); err != nil || !flushed || published != 1 {
			t.Fatalf("restored outbox flushed = %v, publishes = %d, err = %v; want true/1/nil", flushed, published, err)
		}
		if err := recovered.MigrateAll(ctx, false); err != nil {
			t.Fatalf("migration after restore = %v, want no-op success", err)
		}
	})
}

func seedRecoveryData(t *testing.T, db *gorm.DB) {
	t.Helper()
	at := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
	values := []any{
		&model.Tenant{ID: "tnt_backup", Name: "backup", Status: model.StatusActive, RequestsPerMinute: 31, TokensPerMinute: 701, MaxConcurrent: 3, CreatedAt: at, UpdatedAt: at},
		&model.User{ID: "usr_backup", TenantID: "tnt_backup", Email: "backup@example.com", PasswordHash: "existing-digest", Name: "backup", Role: model.RoleOwner, Status: model.StatusActive, CreatedAt: at, UpdatedAt: at},
		&model.PlatformOperator{ID: "opr_backup", Email: "operator@example.com", PasswordHash: "existing-operator-digest", Status: model.StatusActive, CreatedAt: at, UpdatedAt: at},
		&model.APIKey{ID: "key_backup", TenantID: "tnt_backup", CreatedBy: "usr_backup", Name: "backup", Hash: strings.Repeat("b", 64), Display: "aisw-backup", Status: model.StatusActive, CreatedAt: at, UpdatedAt: at},
		&model.AuditLog{ID: "aud_backup", TenantID: "tnt_backup", ActorID: "usr_backup", Action: model.ActionAPIKeyCreate, Target: "key_backup", CreatedAt: at},
		&model.RouteRevision{Revision: 1, Digest: strings.Repeat("c", 64), RoutesJSON: "[]", ActorID: "opr_backup", CreatedAt: at},
		&model.WorkflowTemplateRevision{TemplateID: "workflow_backup", Revision: 1, Digest: strings.Repeat("d", 64), Description: "backup", InputsJSON: "[]", OutputsJSON: "[]", DependenciesJSON: "[]", VisibleTenantsJSON: "[]", GraphJSON: "{}", ActorID: "opr_backup", CreatedAt: at},
		&model.WorkflowTemplateActive{TemplateID: "workflow_backup", Revision: 1},
	}
	if db.Name() == "mysql" {
		values = append(values, &model.Job{ID: "job_backup", TenantID: "tnt_backup", WorkflowID: "workflow_backup", WorkflowVersion: "1", NodeID: "node_backup", RuntimeID: "runtime_backup", BackendRunID: "run_backup", State: model.JobSucceeded, ObservedSeq: 42, CreatedAt: at, UpdatedAt: at, TerminalAt: &at},
			&model.JobArtifact{ID: "artifact_backup", JobID: "job_backup", TenantID: "tnt_backup", Filename: "output.png", Type: "output", SHA256: strings.Repeat("e", 64), SizeBytes: 1234, ContentType: "image/png", StorageKey: "outputs/backup", CreatedAt: at})
	}
	for _, value := range values {
		if err := db.Create(value).Error; err != nil {
			t.Fatal("seeding recovery fixture failed")
		}
	}
	if err := db.Model(&model.RouteActive{}).Where("id = 1").Update("revision", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := gormstore.New(db).RevokeAPIKey(context.Background(), "tnt_backup", "key_backup", at); err != nil {
		t.Fatal(err)
	}
}

func databaseSnapshot(t *testing.T, db *gorm.DB) []byte {
	t.Helper()
	orders := map[string]string{"tenants": "id", "users": "id", "platform_operators": "id", "api_keys": "id", "audit_logs": "id", "route_revisions": "revision", "route_actives": "id", "workflow_template_revisions": "template_id, revision", "workflow_template_actives": "template_id", "key_revocation_outbox": "id", "schema_migrations_base": "id", "schema_migrations_routes": "id", "schema_migrations_workflow_templates": "id"}
	if db.Name() == "mysql" {
		orders["jobs"] = "id"
		orders["job_artifacts"] = "id"
		orders["schema_migrations_jobs"] = "id"
	}
	snapshot := make(map[string][]map[string]any)
	for table, order := range orders {
		var rows []map[string]any
		if err := db.Table(table).Order(order).Find(&rows).Error; err != nil {
			t.Fatalf("snapshot of %s failed", table)
		}
		snapshot[table] = rows
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestLiveServiceStartupRelaysRecoveredRevocations(t *testing.T) {
	addr := os.Getenv("AISW_REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("set AISW_REDIS_TEST_ADDR to a disposable Redis")
	}
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dsn string) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		st := gormstore.New(db)
		if err := st.MigrateAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		seedRecoveryData(t, db)
		client := redis.NewClient(&redis.Options{Addr: addr})
		defer client.Close()
		verification := cache.NewWithClient(client, time.Minute)
		before, err := verification.Generation(ctx)
		if err != nil {
			t.Fatal("reading Redis generation failed")
		}
		cfg := config.Config{Database: config.DatabaseConf{Driver: db.Name(), DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2}, Redis: config.RedisConf{Addr: addr, TTL: time.Minute}, Auth: config.AuthConf{AccessSecret: strings.Repeat("a", 32), AccessExpire: time.Hour}, InternalToken: strings.Repeat("b", 32), BootstrapToken: strings.Repeat("c", 32)}
		service, err := svc.NewServiceContext(ctx, cfg)
		if err != nil {
			t.Fatalf("NewServiceContext = %v, want nil", err)
		}
		defer service.Close()
		generation, err := service.Revocations.WatchGeneration(ctx, before, 2*time.Second)
		if err != nil || generation <= before {
			t.Fatalf("startup generation = %d, previous = %d, err = %v; want increase", generation, before, err)
		}
		_, err = st.FlushRevocations(ctx, func(context.Context) error {
			t.Error("recovered generation published again after service confirmation")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Do(ctx, "CLIENT", "PAUSE", 2000, "WRITE").Err(); err != nil {
			t.Fatal("pausing disposable Redis writes failed")
		}
		defer client.Do(context.Background(), "CLIENT", "UNPAUSE")
		deadline, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		defer stop()
		started := time.Now()
		err = service.Cache.PublishInvalidation(deadline)
		if err == nil || time.Since(started) >= time.Second {
			t.Fatalf("publisher deadline elapsed = %v, err = %v; want error within 1s", time.Since(started), err)
		}
	})
}
