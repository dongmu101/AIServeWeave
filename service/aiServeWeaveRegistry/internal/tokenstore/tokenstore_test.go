package tokenstore_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

func TestMintThenConsumeSucceedsOnce(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()

	token, err := store.Mint(15*time.Minute, now)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if token == "" {
		t.Fatal("Mint() returned an empty token")
	}

	nodeID, err := store.Consume(token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("first Consume() error = %v, want nil", err)
	}
	if nodeID != "" {
		t.Errorf("first Consume() node_id = %q, want \"\" for an unbound token", nodeID)
	}
	if _, err := store.Consume(token, now.Add(2*time.Minute)); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("second Consume() (replay) error = %v, want ErrInvalidToken", err)
	}
}

func TestMintForNodeBindsTheTokenAndConsumeReportsIt(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()

	token, err := store.MintForNode(15*time.Minute, now, "node-fixed")
	if err != nil {
		t.Fatalf("MintForNode() error = %v", err)
	}
	nodeID, err := store.Consume(token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Consume() error = %v, want nil", err)
	}
	if nodeID != "node-fixed" {
		t.Errorf("Consume() node_id = %q, want %q", nodeID, "node-fixed")
	}
}

func TestConsumeRejectsAnExpiredToken(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()

	token, err := store.Mint(time.Minute, now)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := store.Consume(token, now.Add(2*time.Minute)); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() after expiry error = %v, want ErrInvalidToken", err)
	}
}

func TestConsumeRejectsAnUnknownToken(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Consume("never-minted", time.Now()); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() on an unknown token error = %v, want ErrInvalidToken", err)
	}
	if _, err := store.Consume("", time.Now()); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() on an empty token error = %v, want ErrInvalidToken", err)
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	now := time.Now()

	first, err := tokenstore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	token, err := first.Mint(15*time.Minute, now)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	second, err := tokenstore.Open(path)
	if err != nil {
		t.Fatalf("reopen Open() error = %v", err)
	}
	if _, err := second.Consume(token, now.Add(time.Minute)); err != nil {
		t.Fatalf("Consume() on a reopened store error = %v, want nil", err)
	}

	// The reopened store's own record of the consumption must persist too,
	// so a third process cannot replay the same token.
	third, err := tokenstore.Open(path)
	if err != nil {
		t.Fatalf("second reopen Open() error = %v", err)
	}
	if _, err := third.Consume(token, now.Add(2*time.Minute)); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() on a third store after a persisted use = %v, want ErrInvalidToken", err)
	}
}

func TestRevokePreventsConsume(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()

	token, err := store.Mint(15*time.Minute, now)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if err := store.Revoke(token, now.Add(time.Minute)); err != nil {
		t.Fatalf("Revoke() error = %v, want nil", err)
	}
	if _, err := store.Consume(token, now.Add(2*time.Minute)); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() on a revoked token error = %v, want ErrInvalidToken", err)
	}
}

func TestRevokeIsIdempotentAndSurvivesAlreadyUsedOrExpiredTokens(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()

	used, err := store.Mint(15*time.Minute, now)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := store.Consume(used, now); err != nil {
		t.Fatalf("Consume() error = %v, want nil", err)
	}
	if err := store.Revoke(used, now.Add(time.Minute)); err != nil {
		t.Fatalf("Revoke() on an already-used token error = %v, want nil", err)
	}
	if err := store.Revoke(used, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("second Revoke() (idempotent) error = %v, want nil", err)
	}

	expired, err := store.Mint(time.Minute, now)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if err := store.Revoke(expired, now.Add(time.Hour)); err != nil {
		t.Fatalf("Revoke() on an expired token error = %v, want nil", err)
	}
}

func TestRevokeRejectsAnUnknownToken(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Revoke("never-minted", time.Now()); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Revoke() on an unknown token error = %v, want ErrInvalidToken", err)
	}
}

func TestMintRejectsANonPositiveTTL(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Mint(0, time.Now()); err == nil {
		t.Fatal("Mint(0, ...) = nil error, want an error")
	}
	if _, err := store.Mint(-time.Second, time.Now()); err == nil {
		t.Fatal("Mint(negative, ...) = nil error, want an error")
	}
}
