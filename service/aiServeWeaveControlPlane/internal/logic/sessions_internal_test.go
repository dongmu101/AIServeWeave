package logic

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
)

type mutationClock struct{ now time.Time }

func (c *mutationClock) Now() time.Time { return c.now }
func (c *mutationClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

func TestFinishMutationRevokesSessionCreatedAfterGateExpiry(t *testing.T) {
	clock := &mutationClock{now: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)}
	sessions := session.NewMemory(clock)
	subject := session.Subject{Kind: session.SubjectTenantUser, ID: "usr_1"}
	gate, err := sessions.BeginMutation(context.Background(), subject)
	if err != nil {
		t.Fatalf("BeginMutation error = %v, want nil", err)
	}
	clock.now = clock.now.Add(31 * time.Second)
	raced := session.Record{ID: "ses_raced", Subject: subject, TenantID: "tnt_1", Role: "owner", ExpiresAt: clock.now.Add(time.Hour)}
	if err := sessions.Create(context.Background(), raced); err != nil {
		t.Fatalf("Create after expired gate error = %v, want nil", err)
	}

	svc := &Service{sessions: sessions}
	if err := svc.finishMutation(context.Background(), gate, nil); err != nil {
		t.Fatalf("finishMutation error = %v, want nil", err)
	}
	if err := sessions.Validate(context.Background(), raced.ID, raced.Subject, raced.TenantID, raced.Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("Validate raced session error = %v, want %v", err, session.ErrInvalid)
	}
}
