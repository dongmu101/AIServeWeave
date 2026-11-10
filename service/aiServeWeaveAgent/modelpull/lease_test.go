package modelpull

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ledgerBytesUsed(t *testing.T, l *Ledger) int64 {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := l.load()
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	return st.BytesUsed
}

// The ledger file must be rewritten once per lease, not once per 32KiB
// chunk: 128MiB consumed in 32KiB steps is 4096 chunks but only two grants
// of the 64MiB default lease plus the final settle.
func TestLedgerLease_WritesOncePerLeaseNotPerChunk(t *testing.T) {
	const chunk = 32 << 10
	const total = 128 << 20
	l := &Ledger{Path: filepath.Join(t.TempDir(), "ledger.json"), Clock: newFakeClock()}
	lease := l.newLease(filepath.Join(t.TempDir(), "m.part"), 0, 0, 0)

	for consumed := 0; consumed < total; consumed += chunk {
		if err := lease.Consume(chunk); err != nil {
			t.Fatalf("Consume at %d: %v", consumed, err)
		}
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got, want := l.saves.Load(), int64(3); got != want {
		t.Fatalf("ledger saves = %d, want %d (2 leases of 64MiB + 1 settle) for %d chunks", got, want, total/chunk)
	}
	if got := ledgerBytesUsed(t, l); got != total {
		t.Fatalf("BytesUsed = %d, want exactly the %d consumed", got, total)
	}
}

func TestLedgerLease_CloseReturnsUnusedRoom(t *testing.T) {
	l := &Ledger{Path: filepath.Join(t.TempDir(), "ledger.json"), Clock: newFakeClock()}
	lease := l.newLease(filepath.Join(t.TempDir(), "m.part"), 0, 100<<20, 0)
	if err := lease.Consume(1000); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := ledgerBytesUsed(t, l); got <= 1000 {
		t.Fatalf("BytesUsed while leased = %d, want the whole lease charged", got)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := ledgerBytesUsed(t, l); got != 1000 {
		t.Fatalf("BytesUsed after Close = %d, want 1000", got)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := ledgerBytesUsed(t, l); got != 1000 {
		t.Fatalf("BytesUsed after second Close = %d, want 1000 (idempotent)", got)
	}
}

func TestLedgerLease_RefundGivesBackShortWrite(t *testing.T) {
	l := &Ledger{Path: filepath.Join(t.TempDir(), "ledger.json"), Clock: newFakeClock()}
	lease := l.newLease(filepath.Join(t.TempDir(), "m.part"), 0, 0, 0)
	if err := lease.Consume(500); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	lease.Refund(200)
	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := ledgerBytesUsed(t, l); got != 300 {
		t.Fatalf("BytesUsed = %d, want 300", got)
	}
}

// Many downloads leasing from one quota at once must never jointly consume
// more than the quota, and after they all finish the ledger must hold
// exactly what was really consumed.
func TestLedgerLease_ConcurrentLeasesNeverExceedQuota(t *testing.T) {
	const quota = 40 << 20
	const step = 32 << 10
	const workers = 8
	l := &Ledger{Path: filepath.Join(t.TempDir(), "ledger.json"), Clock: newFakeClock()}
	dir := t.TempDir()

	var consumed atomic.Int64
	var quotaErrs atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease := l.newLease(filepath.Join(dir, "m"+strconv.Itoa(i)+".part"), 0, quota, 0)
			defer lease.Close()
			// 8 workers want 10MiB each, 80MiB in all, against a 40MiB quota.
			for got := 0; got < 10<<20; got += step {
				if err := lease.Consume(step); err != nil {
					if !errors.Is(err, errLedgerQuotaExceeded) {
						t.Errorf("Consume: %v, want errLedgerQuotaExceeded", err)
					}
					quotaErrs.Add(1)
					return
				}
				consumed.Add(step)
			}
		}()
	}
	wg.Wait()

	if consumed.Load() > quota {
		t.Fatalf("consumed %d bytes against a quota of %d", consumed.Load(), quota)
	}
	if quotaErrs.Load() == 0 {
		t.Fatalf("no worker hit the quota; the test does not exercise the limit")
	}
	if got := ledgerBytesUsed(t, l); got != consumed.Load() {
		t.Fatalf("BytesUsed = %d, want exactly the %d consumed once every lease is closed", got, consumed.Load())
	}
}

// A crashed process leaves its lease charged in the ledger file. The next
// process must trim that charge to what actually reached the partial file.
func TestLedger_RecoversCrashedLeaseAgainstPartialFileSize(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "ledger.json")
	part := filepath.Join(dir, "m.bin.part")
	const quota = 100 << 20

	crashed := &Ledger{Path: ledgerPath, Clock: newFakeClock()}
	lease := crashed.newLease(part, 0, quota, 0)
	if err := lease.Consume(1 << 20); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := ledgerBytesUsed(t, crashed); got < 1<<20 {
		t.Fatalf("lease not charged: %d", got)
	}
	// The process dies here: no Close. Only 300 bytes made it to disk.
	if err := os.WriteFile(part, make([]byte, 300), 0o644); err != nil {
		t.Fatal(err)
	}

	restarted := &Ledger{Path: ledgerPath, Clock: newFakeClock()}
	if _, err := restarted.Reserve(0, quota); err != nil {
		t.Fatalf("Reserve after restart: %v", err)
	}
	if got := ledgerBytesUsed(t, restarted); got != 300 {
		t.Fatalf("BytesUsed after recovery = %d, want 300 (the partial file's real size)", got)
	}
	restarted.mu.Lock()
	st, _ := restarted.load()
	restarted.mu.Unlock()
	if len(st.Leases) != 0 {
		t.Fatalf("orphan leases still listed after recovery: %v", st.Leases)
	}
}

func TestLedger_RecoveryHonoursResumeBaseAndKeepsLeaseWhenFileIsGone(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "ledger.json")
	part := filepath.Join(dir, "resumed.bin.part")

	crashed := &Ledger{Path: ledgerPath, Clock: newFakeClock()}
	// Resumed download: 1000 bytes were already on disk before the lease.
	resumed := crashed.newLease(part, 1000, 0, 5000)
	if err := resumed.Consume(10); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, make([]byte, 1000+700), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := crashed.newLease(filepath.Join(dir, "missing.part"), 0, 0, 5000)
	if err := gone.Consume(10); err != nil {
		t.Fatal(err)
	}

	restarted := &Ledger{Path: ledgerPath, Clock: newFakeClock()}
	if _, err := restarted.Reserve(0, 0); err != nil {
		t.Fatal(err)
	}
	// The hint caps each lease at max(hint, 1MiB) = 1MiB. resumed keeps the 700
	// bytes beyond its base; gone has no file, so its whole lease stays charged.
	if got, want := ledgerBytesUsed(t, restarted), int64(700)+minLeaseBytes; got != want {
		t.Fatalf("BytesUsed after recovery = %d, want %d", got, want)
	}
}

// A ledger file written before leases existed (no "leases" key) still loads.
func TestLedger_LoadsFileWithoutLeasesField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(path, []byte(`{"period_start":"2026-01-01T00:00:00Z","bytes_used":50}`), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &Ledger{Path: path, Clock: newFakeClock()}
	lease := l.newLease(filepath.Join(t.TempDir(), "m.part"), 0, 100, 0)
	if err := lease.Consume(50); err != nil {
		t.Fatalf("Consume within the remaining room: %v", err)
	}
	if err := lease.Consume(1); !errors.Is(err, errLedgerQuotaExceeded) {
		t.Fatalf("Consume past the quota: %v, want errLedgerQuotaExceeded", err)
	}
}

// If a Period reset wipes the ledger while a lease is open, the lease must
// keep working against the fresh period instead of corrupting it.
func TestLedgerLease_SurvivesPeriodReset(t *testing.T) {
	clock := newFakeClock()
	l := &Ledger{Path: filepath.Join(t.TempDir(), "ledger.json"), Period: time.Hour, Clock: clock}
	lease := l.newLease(filepath.Join(t.TempDir(), "m.part"), 0, 0, 2<<20)
	if err := lease.Consume(2 << 20); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Hour)
	if err := lease.Consume(3 << 20); err != nil {
		t.Fatalf("Consume after reset: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if got := ledgerBytesUsed(t, l); got != 3<<20 {
		t.Fatalf("BytesUsed = %d, want only the new period's 3MiB", got)
	}
}

// End to end: a 16MiB download through pullOne touches the ledger file a
// handful of times, where the per-chunk design wrote it ~512 times.
func TestPullOne_LedgerWritesAreLeaseSized(t *testing.T) {
	const size = 16 << 20
	srv, digest := benchSource(t, size)
	dir := t.TempDir()
	l := &Ledger{Path: filepath.Join(dir, "ledger.json"), Clock: newFakeClock()}

	spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: digest, SizeBytes: size, TargetPath: filepath.Join(dir, "m.bin")}
	if err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{Ledger: l, LedgerQuotaBytes: 1 << 40}); err != nil {
		t.Fatalf("pullOne: %v", err)
	}
	if got := l.saves.Load(); got > 3 {
		t.Fatalf("ledger saves = %d for a %d byte download, want <= 3", got, size)
	}
	if got := ledgerBytesUsed(t, l); got != size {
		t.Fatalf("BytesUsed = %d, want %d", got, size)
	}
}

// A download that dies mid-stream must leave the ledger charged only for the
// bytes that really reached disk, not for the whole lease it had been given.
func TestPullOne_FailedDownloadReturnsUnusedLease(t *testing.T) {
	const size = 8 << 20
	const sent = 3 << 20
	block := make([]byte, 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		for i := 0; i < sent>>20; i++ {
			w.Write(block)
		}
		// Hang up before the declared length is delivered.
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()

	dir := t.TempDir()
	l := &Ledger{Path: filepath.Join(dir, "ledger.json"), Clock: newFakeClock()}
	target := filepath.Join(dir, "m.bin")
	spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex([]byte("x")), SizeBytes: size, TargetPath: target}

	err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{Ledger: l, LedgerQuotaBytes: 1 << 40})
	if !errors.Is(err, errFetchFailed) {
		t.Fatalf("pullOne error = %v, want errFetchFailed", err)
	}
	fi, statErr := os.Stat(target + ".part")
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := ledgerBytesUsed(t, l); got != fi.Size() {
		t.Fatalf("BytesUsed = %d, want the %d bytes actually on disk", got, fi.Size())
	}
}

// Concurrent downloads sharing a ledger quota may not jointly pass it, and
// once they all stop the ledger is charged for exactly the bytes on disk (no
// unreturned lease). Which downloads win under contention is not asserted:
// as with the old per-chunk reservation, interleaved downloads can each get
// partway and fail.
func TestRunManifest_ConcurrentLedgerQuotaNotExceeded(t *testing.T) {
	const n = 6
	const perFile = 4 << 20
	srv, digest := benchSource(t, perFile)
	dir := t.TempDir()
	specs := make([]Spec, n)
	for i := range specs {
		name := "m" + strconv.Itoa(i)
		specs[i] = Spec{Name: name, SourceURL: srv.URL, SHA256: digest, TargetPath: filepath.Join(dir, name+".bin")}
	}
	const quota = perFile * 3
	l := &Ledger{Path: filepath.Join(dir, "ledger.json"), Clock: newFakeClock()}
	cfg := Config{Allowlist: []string{srv.URL}, MaxConcurrency: n, Ledger: l, LedgerQuotaBytes: quota}

	result := RunManifest(context.Background(), cfg, specs)

	for name, err := range result.Failed {
		if !errors.Is(err, errLedgerQuotaExceeded) {
			t.Fatalf("%s failed with %v, want errLedgerQuotaExceeded", name, err)
		}
	}
	if len(result.Failed) == 0 {
		t.Fatalf("no download hit the quota; the test does not exercise the limit")
	}
	var onDisk int64
	for _, pattern := range []string{"*.bin", "*.part"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil {
				t.Fatal(err)
			}
			onDisk += fi.Size()
		}
	}
	if onDisk > quota {
		t.Fatalf("%d bytes written against a ledger quota of %d", onDisk, quota)
	}
	if got := ledgerBytesUsed(t, l); got != onDisk {
		t.Fatalf("BytesUsed = %d, want the %d bytes on disk", got, onDisk)
	}
}
