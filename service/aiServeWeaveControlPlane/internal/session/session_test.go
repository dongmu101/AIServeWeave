package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newFixture() (*fakeClock, session.Store, session.Subject) {
	clock := &fakeClock{now: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	return clock, session.NewMemory(clock), session.Subject{Kind: session.SubjectTenantUser, ID: "usr_1"}
}

func record(id string, subject session.Subject, expiry time.Time) session.Record {
	return session.Record{
		ID:        id,
		Subject:   subject,
		TenantID:  "tnt_1",
		Role:      "owner",
		ExpiresAt: expiry,
	}
}

func TestMemoryStoreValidatesExactLiveRecord(t *testing.T) {
	clock, sessions, subject := newFixture()
	want := record("ses_1", subject, clock.Now().Add(time.Hour))
	if err := sessions.Create(context.Background(), want); err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	if err := sessions.Validate(context.Background(), want.ID, want.Subject, want.TenantID, want.Role); err != nil {
		t.Fatalf("Validate error = %v, want nil", err)
	}

	tests := []struct {
		name string
		edit func(*session.Record)
	}{
		{name: "subject kind", edit: func(got *session.Record) { got.Subject.Kind = session.SubjectPlatformOperator }},
		{name: "subject id", edit: func(got *session.Record) { got.Subject.ID = "usr_2" }},
		{name: "tenant", edit: func(got *session.Record) { got.TenantID = "tnt_2" }},
		{name: "role", edit: func(got *session.Record) { got.Role = "member" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mismatch := want
			tt.edit(&mismatch)
			if err := sessions.Validate(context.Background(), mismatch.ID, mismatch.Subject, mismatch.TenantID, mismatch.Role); !errors.Is(err, session.ErrInvalid) {
				t.Errorf("Validate error = %v, want %v", err, session.ErrInvalid)
			}
		})
	}
}

func TestMemoryStoreExpiresWithoutSleeping(t *testing.T) {
	clock, sessions, subject := newFixture()
	want := record("ses_1", subject, clock.Now().Add(time.Minute))
	if err := sessions.Create(context.Background(), want); err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	clock.Advance(time.Minute)
	if err := sessions.Validate(context.Background(), want.ID, want.Subject, want.TenantID, want.Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("Validate error = %v, want %v", err, session.ErrInvalid)
	}
	if got, err := sessions.RevokeAll(context.Background(), subject); err != nil || got != 0 {
		t.Errorf("RevokeAll = (%d, %v), want (0, nil)", got, err)
	}
}

func TestMemoryStoreEvictsEarliestSessionAtBound(t *testing.T) {
	clock, sessions, subject := newFixture()
	records := make([]session.Record, session.MaxSessions+1)
	for i := range records {
		records[i] = record("ses_"+string(rune('a'+i)), subject, clock.Now().Add(time.Duration(i+1)*time.Minute))
		if err := sessions.Create(context.Background(), records[i]); err != nil {
			t.Fatalf("Create(%d) error = %v, want nil", i, err)
		}
	}
	if err := sessions.Validate(context.Background(), records[0].ID, records[0].Subject, records[0].TenantID, records[0].Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("earliest Validate error = %v, want %v", err, session.ErrInvalid)
	}
	for i := 1; i < len(records); i++ {
		if err := sessions.Validate(context.Background(), records[i].ID, records[i].Subject, records[i].TenantID, records[i].Role); err != nil {
			t.Errorf("Validate(%d) error = %v, want nil", i, err)
		}
	}
}

func TestMemoryStoreRevokesOneSessionWithinItsSubject(t *testing.T) {
	clock, sessions, subject := newFixture()
	want := record("ses_1", subject, clock.Now().Add(time.Hour))
	if err := sessions.Create(context.Background(), want); err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	other := session.Subject{Kind: session.SubjectTenantUser, ID: "usr_2"}
	if changed, err := sessions.Revoke(context.Background(), other, want.ID); err != nil || changed {
		t.Errorf("wrong-subject Revoke = (%v, %v), want (false, nil)", changed, err)
	}
	if err := sessions.Validate(context.Background(), want.ID, want.Subject, want.TenantID, want.Role); err != nil {
		t.Fatalf("Validate after wrong-subject revoke = %v, want nil", err)
	}
	if changed, err := sessions.Revoke(context.Background(), subject, want.ID); err != nil || !changed {
		t.Errorf("Revoke = (%v, %v), want (true, nil)", changed, err)
	}
	if changed, err := sessions.Revoke(context.Background(), subject, want.ID); err != nil || changed {
		t.Errorf("second Revoke = (%v, %v), want (false, nil)", changed, err)
	}
}

func TestMemoryStoreBulkRevocationIsIdempotent(t *testing.T) {
	clock, sessions, subject := newFixture()
	for _, id := range []string{"ses_1", "ses_2"} {
		if err := sessions.Create(context.Background(), record(id, subject, clock.Now().Add(time.Hour))); err != nil {
			t.Fatalf("Create(%q) error = %v, want nil", id, err)
		}
	}
	if got, err := sessions.RevokeAll(context.Background(), subject); err != nil || got != 2 {
		t.Errorf("RevokeAll = (%d, %v), want (2, nil)", got, err)
	}
	if got, err := sessions.RevokeAll(context.Background(), subject); err != nil || got != 0 {
		t.Errorf("second RevokeAll = (%d, %v), want (0, nil)", got, err)
	}
}

func TestMemoryStoreMutationGateRevokesAndBlocksCreation(t *testing.T) {
	clock, sessions, subject := newFixture()
	old := record("ses_old", subject, clock.Now().Add(time.Hour))
	if err := sessions.Create(context.Background(), old); err != nil {
		t.Fatalf("Create old error = %v, want nil", err)
	}

	gate, err := sessions.BeginMutation(context.Background(), subject)
	if err != nil {
		t.Fatalf("BeginMutation error = %v, want nil", err)
	}
	if gate.RevokedSessions != 1 {
		t.Errorf("RevokedSessions = %d, want 1", gate.RevokedSessions)
	}
	if err := sessions.Validate(context.Background(), old.ID, old.Subject, old.TenantID, old.Role); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("old Validate error = %v, want %v", err, session.ErrInvalid)
	}
	newRecord := record("ses_new", subject, clock.Now().Add(time.Hour))
	if err := sessions.Create(context.Background(), newRecord); !errors.Is(err, session.ErrMutationActive) {
		t.Errorf("Create during mutation error = %v, want %v", err, session.ErrMutationActive)
	}

	wrong := gate
	wrong.Nonce = "wrong"
	if err := sessions.EndMutation(context.Background(), wrong); !errors.Is(err, session.ErrInvalid) {
		t.Errorf("EndMutation wrong nonce error = %v, want %v", err, session.ErrInvalid)
	}
	if err := sessions.Create(context.Background(), newRecord); !errors.Is(err, session.ErrMutationActive) {
		t.Errorf("Create after wrong nonce error = %v, want %v", err, session.ErrMutationActive)
	}
	if err := sessions.EndMutation(context.Background(), gate); err != nil {
		t.Fatalf("EndMutation error = %v, want nil", err)
	}
	if err := sessions.Create(context.Background(), newRecord); err != nil {
		t.Errorf("Create after gate release error = %v, want nil", err)
	}
}

func TestMemoryStoreRejectsInvalidRecords(t *testing.T) {
	clock, sessions, subject := newFixture()
	valid := record("ses_1", subject, clock.Now().Add(time.Hour))
	tests := []struct {
		name string
		edit func(*session.Record)
	}{
		{name: "missing session id", edit: func(got *session.Record) { got.ID = "" }},
		{name: "unknown subject kind", edit: func(got *session.Record) { got.Subject.Kind = "unknown" }},
		{name: "missing subject id", edit: func(got *session.Record) { got.Subject.ID = "" }},
		{name: "missing tenant", edit: func(got *session.Record) { got.TenantID = "" }},
		{name: "missing role", edit: func(got *session.Record) { got.Role = "" }},
		{name: "already expired", edit: func(got *session.Record) { got.ExpiresAt = clock.Now() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			tt.edit(&candidate)
			if err := sessions.Create(context.Background(), candidate); !errors.Is(err, session.ErrInvalid) {
				t.Errorf("Create error = %v, want %v", err, session.ErrInvalid)
			}
		})
	}
}
