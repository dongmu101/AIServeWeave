package session_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
)

// TestLiveRedisStore exercises the Lua-backed implementation only when an
// isolated Redis endpoint is supplied. It never flushes the selected DB.
//
// TestLiveRedisStore 仅在提供隔离 Redis 端点时覆盖 Lua 实现。它绝不清空所选数据库。
func TestLiveRedisStore(t *testing.T) {
	addr := os.Getenv("AISW_REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("AISW_REDIS_TEST_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Ping error = %v, want nil", err)
	}

	store := session.NewRedis(client, runtime.NewSystemClock())
	subject := session.Subject{Kind: session.SubjectTenantUser, ID: "usr_live_" + session.NewID()}
	record := session.Record{
		ID:        session.NewID(),
		Subject:   subject,
		TenantID:  "tnt_live",
		Role:      "owner",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	t.Cleanup(func() { _, _ = store.RevokeAll(ctx, subject) })

	if err := store.Create(ctx, record); err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	if err := store.Validate(ctx, record.ID, record.Subject, record.TenantID, record.Role); err != nil {
		t.Fatalf("Validate error = %v, want nil", err)
	}
	gate, err := store.BeginMutation(ctx, subject)
	if err != nil {
		t.Fatalf("BeginMutation error = %v, want nil", err)
	}
	if gate.RevokedSessions != 1 {
		t.Errorf("RevokedSessions = %d, want 1", gate.RevokedSessions)
	}
	if err := store.Validate(ctx, record.ID, record.Subject, record.TenantID, record.Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("Validate after gate error = %v, want %v", err, session.ErrInvalid)
	}
	if err := store.Create(ctx, record); !errors.Is(err, session.ErrMutationActive) {
		t.Errorf("Create during gate error = %v, want %v", err, session.ErrMutationActive)
	}
	if err := store.EndMutation(ctx, gate); err != nil {
		t.Fatalf("EndMutation error = %v, want nil", err)
	}
	if err := store.Create(ctx, record); err != nil {
		t.Fatalf("Create after gate error = %v, want nil", err)
	}
	if changed, err := store.Revoke(ctx, subject, record.ID); err != nil || !changed {
		t.Errorf("Revoke = (%v, %v), want (true, nil)", changed, err)
	}
}

// TestLiveRedisPersistencePhase supports a three-command restart check. Set
// AISW_REDIS_PERSISTENCE_PHASE to create, revoke, then validate-revoked with a
// Redis restart between commands.
//
// TestLiveRedisPersistencePhase 支持三条命令组成的重启检查。依次把
// AISW_REDIS_PERSISTENCE_PHASE 设为 create、revoke、validate-revoked，并在命令间
// 重启 Redis。
func TestLiveRedisPersistencePhase(t *testing.T) {
	phase := os.Getenv("AISW_REDIS_PERSISTENCE_PHASE")
	if phase == "" {
		t.Skip("AISW_REDIS_PERSISTENCE_PHASE is not set")
	}
	addr := os.Getenv("AISW_REDIS_TEST_ADDR")
	if addr == "" {
		t.Fatal("AISW_REDIS_TEST_ADDR is required with AISW_REDIS_PERSISTENCE_PHASE")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	store := session.NewRedis(client, runtime.NewSystemClock())
	ctx := context.Background()
	subject := session.Subject{Kind: session.SubjectTenantUser, ID: "usr_persistence_check"}
	record := session.Record{
		ID: "ses_persistence_check", Subject: subject, TenantID: "tnt_persistence_check",
		Role: "owner", ExpiresAt: time.Now().Add(time.Hour),
	}

	switch phase {
	case "create":
		_, _ = store.RevokeAll(ctx, subject)
		if err := store.Create(ctx, record); err != nil {
			t.Fatalf("Create error = %v, want nil", err)
		}
	case "revoke":
		if err := store.Validate(ctx, record.ID, record.Subject, record.TenantID, record.Role); err != nil {
			t.Fatalf("Validate persisted session error = %v, want nil", err)
		}
		if changed, err := store.Revoke(ctx, subject, record.ID); err != nil || !changed {
			t.Fatalf("Revoke = (%v, %v), want (true, nil)", changed, err)
		}
	case "validate-revoked":
		if err := store.Validate(ctx, record.ID, record.Subject, record.TenantID, record.Role); !errors.Is(err, session.ErrInvalid) {
			t.Fatalf("Validate revoked session error = %v, want %v", err, session.ErrInvalid)
		}
	default:
		t.Fatalf("unknown AISW_REDIS_PERSISTENCE_PHASE %q", phase)
	}
}
