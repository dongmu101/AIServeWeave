package modelpull

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLedger_ReserveAccumulatesAndEnforcesQuota(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	clock := newFakeClock()
	l := &Ledger{Path: path, Clock: clock}

	if _, err := l.Reserve(60, 100); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	if _, err := l.Reserve(30, 100); err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	if _, err := l.Reserve(20, 100); err == nil {
		t.Fatalf("third Reserve: want errLedgerQuotaExceeded, got nil (60+30+20 > 100)")
	} else if !errors.Is(err, errLedgerQuotaExceeded) {
		t.Fatalf("third Reserve error = %v, want errLedgerQuotaExceeded", err)
	}
}

func TestLedger_SurvivesAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	clock := newFakeClock()

	first := &Ledger{Path: path, Clock: clock}
	if _, err := first.Reserve(80, 100); err != nil {
		t.Fatalf("Reserve on first instance: %v", err)
	}

	second := &Ledger{Path: path, Clock: clock}
	if _, err := second.Reserve(30, 100); err == nil {
		t.Fatalf("Reserve on second instance: want errLedgerQuotaExceeded (80 already used), got nil")
	}
}

func TestLedger_RollbackReturnsUnusedBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	clock := newFakeClock()
	l := &Ledger{Path: path, Clock: clock}

	rollback, err := l.Reserve(100, 100)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	rollback(60) // only 40 bytes were actually written

	if _, err := l.Reserve(50, 100); err != nil {
		t.Fatalf("Reserve after rollback: %v, want success (40 used + 50 <= 100)", err)
	}
}

func TestLedger_PeriodResetsAfterElapsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	clock := newFakeClock()
	l := &Ledger{Path: path, Period: 24 * time.Hour, Clock: clock}

	if _, err := l.Reserve(90, 100); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := l.Reserve(20, 100); err == nil {
		t.Fatalf("Reserve before period elapses: want errLedgerQuotaExceeded, got nil")
	}

	clock.advance(25 * time.Hour)

	if _, err := l.Reserve(20, 100); err != nil {
		t.Fatalf("Reserve after period elapses: %v, want success", err)
	}
}

func TestLedger_ZeroQuotaMeansUnlimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	l := &Ledger{Path: path, Clock: newFakeClock()}

	if _, err := l.Reserve(1_000_000, 0); err != nil {
		t.Fatalf("Reserve with quotaBytes=0: %v, want success (unlimited)", err)
	}
}

func TestLedger_MissingFileIsFreshLedger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist", "ledger.json")
	l := &Ledger{Path: path, Clock: newFakeClock()}

	if _, err := l.Reserve(10, 100); err != nil {
		t.Fatalf("Reserve against a missing ledger file: %v, want success", err)
	}
}
