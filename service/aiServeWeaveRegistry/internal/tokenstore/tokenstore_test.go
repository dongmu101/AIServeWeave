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

	if err := store.Consume(token, now.Add(time.Minute)); err != nil {
		t.Fatalf("first Consume() error = %v, want nil", err)
	}
	if err := store.Consume(token, now.Add(2*time.Minute)); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("second Consume() (replay) error = %v, want ErrInvalidToken", err)
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
	if err := store.Consume(token, now.Add(2*time.Minute)); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() after expiry error = %v, want ErrInvalidToken", err)
	}
}

func TestConsumeRejectsAnUnknownToken(t *testing.T) {
	dir := t.TempDir()
	store, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Consume("never-minted", time.Now()); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() on an unknown token error = %v, want ErrInvalidToken", err)
	}
	if err := store.Consume("", time.Now()); !errors.Is(err, tokenstore.ErrInvalidToken) {
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
	if err := second.Consume(token, now.Add(time.Minute)); err != nil {
		t.Fatalf("Consume() on a reopened store error = %v, want nil", err)
	}

	// The reopened store's own record of the consumption must persist too,
	// so a third process cannot replay the same token.
	third, err := tokenstore.Open(path)
	if err != nil {
		t.Fatalf("second reopen Open() error = %v", err)
	}
	if err := third.Consume(token, now.Add(2*time.Minute)); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() on a third store after a persisted use = %v, want ErrInvalidToken", err)
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
