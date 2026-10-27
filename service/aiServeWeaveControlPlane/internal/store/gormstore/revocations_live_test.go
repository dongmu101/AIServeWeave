package gormstore_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// TestLiveRevocationMutationsRollBackWithoutOutbox proves both security
// mutations fail atomically when their durable notification cannot be queued.
//
// TestLiveRevocationMutationsRollBackWithoutOutbox 证明两种安全变更在无法持久化通知时
// 都会原子回滚。
func TestLiveRevocationMutationsRollBackWithoutOutbox(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := migrateRevocationStore(t, ctx, db)
		t.Run("key revoke", func(t *testing.T) {
			fixture := seedRevocationFixture(t, ctx, st, model.StatusActive)
			removeOutboxSingleton(t, db)

			if err := st.RevokeAPIKey(ctx, fixture.tenantID, fixture.keyID, fixture.at); err == nil {
				t.Fatal("RevokeAPIKey error = nil, want outbox error")
			}
			key, err := st.GetAPIKey(ctx, fixture.tenantID, fixture.keyID)
			if err != nil {
				t.Fatalf("GetAPIKey error = %v, want nil", err)
			}
			if key.Status != model.StatusActive || key.RevokedAt != nil {
				t.Errorf("key after rollback = status %q revoked_at %v, want active and nil", key.Status, key.RevokedAt)
			}
		})

		ensureOutboxSingleton(t, db)
		t.Run("user disable", func(t *testing.T) {
			fixture := seedRevocationFixture(t, ctx, st, model.StatusActive)
			removeOutboxSingleton(t, db)
			audit := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: fixture.tenantID, ActorID: fixture.ownerID, Action: model.ActionUserDisable, Target: fixture.userID, CreatedAt: fixture.at}

			if _, err := st.SetUserStatus(ctx, fixture.tenantID, fixture.userID, model.StatusSuspended, fixture.at, audit); err == nil {
				t.Fatal("SetUserStatus error = nil, want outbox error")
			}
			user, err := st.GetUser(ctx, fixture.tenantID, fixture.userID)
			if err != nil {
				t.Fatalf("GetUser error = %v, want nil", err)
			}
			key, err := st.GetAPIKey(ctx, fixture.tenantID, fixture.keyID)
			if err != nil {
				t.Fatalf("GetAPIKey error = %v, want nil", err)
			}
			if user.Status != model.StatusActive || key.Status != model.StatusActive {
				t.Errorf("statuses after rollback = user %q key %q, want both active", user.Status, key.Status)
			}
			var audits int64
			if err := db.Model(&model.AuditLog{}).Where("id = ?", audit.ID).Count(&audits).Error; err != nil {
				t.Fatalf("count audit error = %v, want nil", err)
			}
			if audits != 0 {
				t.Errorf("audit rows after rollback = %d, want 0", audits)
			}
		})
	})
}

// TestLiveRevocationOutboxLagReflectsUndeliveredGenerations proves
// RevocationOutboxLag reads back the same generation/delivered_generation a
// pending revocation and a successful flush actually left behind.
//
// TestLiveRevocationOutboxLagReflectsUndeliveredGenerations 证明
// RevocationOutboxLag 读回的 generation/delivered_generation，与一次待发送的
// 吊销及一次成功发送实际留下的值一致。
func TestLiveRevocationOutboxLagReflectsUndeliveredGenerations(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := migrateRevocationStore(t, ctx, db)
		fixture := seedRevocationFixture(t, ctx, st, model.StatusActive)

		if err := st.RevokeAPIKey(ctx, fixture.tenantID, fixture.keyID, fixture.at); err != nil {
			t.Fatalf("RevokeAPIKey error = %v, want nil", err)
		}
		generation, delivered, err := st.RevocationOutboxLag(ctx)
		if err != nil {
			t.Fatalf("RevocationOutboxLag error = %v, want nil", err)
		}
		if generation-delivered != 1 {
			t.Fatalf("lag = %d, want 1 right after one undelivered revocation", generation-delivered)
		}

		if _, err := st.FlushRevocations(ctx, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("FlushRevocations error = %v, want nil", err)
		}
		generation, delivered, err = st.RevocationOutboxLag(ctx)
		if err != nil {
			t.Fatalf("RevocationOutboxLag error = %v, want nil", err)
		}
		if generation-delivered != 0 {
			t.Fatalf("lag = %d, want 0 after a successful flush", generation-delivered)
		}
	})
}

// TestLiveRevocationsPersistAndRetry proves a failed publisher leaves durable
// work that a reconstructed Store can deliver and confirm exactly once.
//
// TestLiveRevocationsPersistAndRetry 证明发布失败会留下持久化工作，新建的 Store 能重新
// 发送并确认，之后不再重复发送。
func TestLiveRevocationsPersistAndRetry(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := migrateRevocationStore(t, ctx, db)
		fixture := seedRevocationFixture(t, ctx, st, model.StatusActive)
		if err := st.RevokeAPIKey(ctx, fixture.tenantID, fixture.keyID, fixture.at); err != nil {
			t.Fatalf("RevokeAPIKey error = %v, want nil", err)
		}

		var attempts atomic.Int32
		published, err := st.FlushRevocations(ctx, func(context.Context) error {
			attempts.Add(1)
			return errors.New("publisher unavailable")
		})
		if err == nil || published {
			t.Fatalf("failed FlushRevocations = (%v, %v), want (false, error)", published, err)
		}
		generation, delivered := readOutbox(t, db)
		if generation != 1 || delivered != 0 {
			t.Fatalf("outbox after publisher failure = (%d, %d), want (1, 0)", generation, delivered)
		}

		recovered := gormstore.New(db)
		published, err = recovered.FlushRevocations(ctx, func(context.Context) error {
			attempts.Add(1)
			return nil
		})
		if err != nil || !published {
			t.Fatalf("recovered FlushRevocations = (%v, %v), want (true, nil)", published, err)
		}
		generation, delivered = readOutbox(t, db)
		if generation != 1 || delivered != 1 {
			t.Errorf("outbox after recovery = (%d, %d), want (1, 1)", generation, delivered)
		}
		published, err = recovered.FlushRevocations(ctx, func(context.Context) error {
			attempts.Add(1)
			return nil
		})
		if err != nil || published {
			t.Fatalf("empty FlushRevocations = (%v, %v), want (false, nil)", published, err)
		}
		if got := attempts.Load(); got != 2 {
			t.Errorf("publisher attempts = %d, want 2", got)
		}
	})
}

// TestLiveUnrelatedChangesDoNotQueueRevocations proves failed key operations
// and user enablement leave the durable generation untouched.
//
// TestLiveUnrelatedChangesDoNotQueueRevocations 证明失败的 Key 操作与启用用户都不会推进
// 持久化吊销代际。
func TestLiveUnrelatedChangesDoNotQueueRevocations(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx := context.Background()
		st := migrateRevocationStore(t, ctx, db)
		fixture := seedRevocationFixture(t, ctx, st, model.StatusActive)
		if err := db.Model(&model.User{}).Where("id = ?", fixture.userID).UpdateColumn("status", model.StatusSuspended).Error; err != nil {
			t.Fatalf("prepare suspended user error = %v, want nil", err)
		}

		if err := st.RevokeAPIKey(ctx, fixture.tenantID, "missing-key", fixture.at); err == nil {
			t.Fatal("RevokeAPIKey missing error = nil, want error")
		}
		audit := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: fixture.tenantID, ActorID: fixture.ownerID, Action: model.ActionUserEnable, Target: fixture.userID, CreatedAt: fixture.at}
		if result, err := st.SetUserStatus(ctx, fixture.tenantID, fixture.userID, model.StatusActive, fixture.at, audit); err != nil || !result.Changed {
			t.Fatalf("SetUserStatus enable = (%+v, %v), want changed and nil", result, err)
		}
		generation, delivered := readOutbox(t, db)
		if generation != 0 || delivered != 0 {
			t.Errorf("outbox after unrelated changes = (%d, %d), want (0, 0)", generation, delivered)
		}
		var calls atomic.Int32
		published, err := st.FlushRevocations(ctx, func(context.Context) error {
			calls.Add(1)
			return nil
		})
		if err != nil || published {
			t.Fatalf("FlushRevocations without pending work = (%v, %v), want (false, nil)", published, err)
		}
		if got := calls.Load(); got != 0 {
			t.Errorf("publisher calls without revocation = %d, want 0", got)
		}
	})
}

// TestLiveConcurrentRevocationsReachFinalGeneration proves concurrent
// producers and flushers cannot lose the final coalesced generation.
//
// TestLiveConcurrentRevocationsReachFinalGeneration 证明并发生产者与发送者不会丢失最终
// 合并后的吊销代际。
func TestLiveConcurrentRevocationsReachFinalGeneration(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, _ string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st := migrateRevocationStore(t, ctx, db)
		const producers = 12
		fixture := seedRevocationFixture(t, ctx, st, model.StatusActive)
		keyIDs := []string{fixture.keyID}
		for i := 1; i < producers; i++ {
			key := &model.APIKey{ID: model.NewID(model.PrefixAPIKey), TenantID: fixture.tenantID, CreatedBy: fixture.userID, Name: "concurrent", Hash: model.NewID(model.PrefixAPIKey) + model.NewID(model.PrefixAPIKey), Display: "aisw-live", Status: model.StatusActive}
			if err := st.CreateAPIKey(ctx, key); err != nil {
				t.Fatalf("CreateAPIKey[%d] error = %v, want nil", i, err)
			}
			keyIDs = append(keyIDs, key.ID)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var failures atomic.Int32
		var publications atomic.Int32
		for _, keyID := range keyIDs {
			keyID := keyID
			wg.Go(func() {
				<-start
				if err := gormstore.New(db).RevokeAPIKey(ctx, fixture.tenantID, keyID, fixture.at); err != nil {
					failures.Add(1)
				}
				_, _ = gormstore.New(db).FlushRevocations(ctx, func(context.Context) error {
					publications.Add(1)
					return nil
				})
			})
		}
		close(start)
		wg.Wait()
		if got := failures.Load(); got != 0 {
			t.Fatalf("producer failures = %d, want 0", got)
		}
		if _, err := st.FlushRevocations(ctx, func(context.Context) error {
			publications.Add(1)
			return nil
		}); err != nil {
			t.Fatalf("final FlushRevocations error = %v, want nil", err)
		}
		generation, delivered := readOutbox(t, db)
		if generation != producers || delivered != producers {
			t.Errorf("final outbox = (%d, %d), want (%d, %d)", generation, delivered, producers, producers)
		}
		if publications.Load() == 0 {
			t.Error("publisher calls = 0, want at least 1")
		}
	})
}

type revocationFixture struct {
	tenantID string
	ownerID  string
	userID   string
	keyID    string
	at       time.Time
}

func migrateRevocationStore(t *testing.T, ctx context.Context, db *gorm.DB) *gormstore.Store {
	t.Helper()
	pool, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB error = %v, want nil", err)
	}
	pool.SetMaxOpenConns(32)
	st := gormstore.New(db)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate error = %v, want nil", err)
	}
	return st
}

func ensureOutboxSingleton(t *testing.T, db *gorm.DB) {
	t.Helper()
	statement := "INSERT INTO key_revocation_outbox (id, generation, delivered_generation) VALUES (1, 0, 0) ON CONFLICT (id) DO NOTHING"
	if db.Dialector.Name() == "mysql" {
		statement = "INSERT IGNORE INTO key_revocation_outbox (id, generation, delivered_generation) VALUES (1, 0, 0)"
	}
	if err := db.Exec(statement).Error; err != nil {
		t.Fatalf("ensure outbox singleton error = %v, want nil", err)
	}
}

func removeOutboxSingleton(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("DELETE FROM key_revocation_outbox WHERE id = ?", 1).Error; err != nil {
		t.Fatalf("remove outbox singleton error = %v, want nil", err)
	}
}

func readOutbox(t *testing.T, db *gorm.DB) (int64, int64) {
	t.Helper()
	var row struct {
		Generation          int64
		DeliveredGeneration int64
	}
	if err := db.Table("key_revocation_outbox").Where("id = ?", 1).Take(&row).Error; err != nil {
		t.Fatalf("read outbox error = %v, want nil", err)
	}
	return row.Generation, row.DeliveredGeneration
}

func seedRevocationFixture(t *testing.T, ctx context.Context, st *gormstore.Store, userStatus string) revocationFixture {
	t.Helper()
	fixture := revocationFixture{
		tenantID: model.NewID(model.PrefixTenant),
		ownerID:  model.NewID(model.PrefixUser),
		userID:   model.NewID(model.PrefixUser),
		keyID:    model.NewID(model.PrefixAPIKey),
		at:       time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
	if err := st.CreateTenant(ctx, &model.Tenant{ID: fixture.tenantID, Name: "revocations", Status: model.StatusActive}); err != nil {
		t.Fatalf("CreateTenant error = %v, want nil", err)
	}
	if err := st.CreateUser(ctx, &model.User{ID: fixture.ownerID, TenantID: fixture.tenantID, Email: fixture.ownerID + "@example.com", PasswordHash: "digest", Role: model.RoleOwner, Status: model.StatusActive}); err != nil {
		t.Fatalf("CreateUser owner error = %v, want nil", err)
	}
	if err := st.CreateUser(ctx, &model.User{ID: fixture.userID, TenantID: fixture.tenantID, Email: fixture.userID + "@example.com", PasswordHash: "digest", Role: model.RoleMember, Status: userStatus}); err != nil {
		t.Fatalf("CreateUser member error = %v, want nil", err)
	}
	if err := st.CreateAPIKey(ctx, &model.APIKey{ID: fixture.keyID, TenantID: fixture.tenantID, CreatedBy: fixture.userID, Name: "key", Hash: fixture.keyID + fixture.keyID, Display: "aisw-live", Status: model.StatusActive}); err != nil {
		t.Fatalf("CreateAPIKey error = %v, want nil", err)
	}
	return fixture
}
