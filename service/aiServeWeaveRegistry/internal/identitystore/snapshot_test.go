package identitystore_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
)

// blockingWriter is an injectable write function that parks the first write
// until released, so a test can hold a writer inside its fsync stand-in.
//
// blockingWriter 是一个可注入的写函数，把第一次写入停住直到被放行，使测试能把一个写者
// 留在它的 fsync 替身里。
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	inner   func(path string, data []byte, mode os.FileMode) error
}

func newBlockingWriter() *blockingWriter {
	// Reuse the real on-disk format by letting the write through once released.
	// 放行后让写入真实落盘，沿用真实的磁盘格式。
	return &blockingWriter{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
		inner: func(path string, data []byte, mode os.FileMode) error {
			return os.WriteFile(path, data, mode)
		},
	}
}

func (b *blockingWriter) write(path string, data []byte, mode os.FileMode) error {
	b.started <- struct{}{}
	<-b.release
	return b.inner(path, data, mode)
}

// within runs fn and fails the test if it does not return in time.
// within 运行 fn，若未及时返回则让测试失败。
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked behind an in-flight write", what)
	}
}

func TestReadsDoNotWaitForAnInFlightWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()
	if _, err := store.Reserve("node-1", "fp-a", now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.RecordPending("node-2", now); err != nil {
		t.Fatalf("RecordPending() error = %v", err)
	}

	writer := newBlockingWriter()
	identitystore.SetWriteFileForTest(store, writer.write)

	disabled := make(chan error, 1)
	go func() { disabled <- store.Disable("node-1", now.Add(time.Minute)) }()
	<-writer.started // the writer is now parked inside its write

	within(t, "IsDisabled", func() {
		if store.IsDisabled("node-1") {
			t.Error("IsDisabled(node-1) = true before the write was published; readers must see the previous snapshot")
		}
	})
	within(t, "IsApproved", func() {
		if !store.IsApproved("node-1") {
			t.Error("IsApproved(node-1) = false, want true")
		}
	})
	within(t, "HasRecord", func() {
		if !store.HasRecord("node-1") || store.HasRecord("ghost") {
			t.Error("HasRecord gave the wrong answer during a write")
		}
	})
	within(t, "IsPending", func() {
		if !store.IsPending("node-2") {
			t.Error("IsPending(node-2) = false, want true")
		}
	})
	within(t, "States/DisabledNodeIDs/MaintenanceNodeIDs", func() {
		if got := len(store.States()); got != 2 {
			t.Errorf("len(States()) = %d, want 2", got)
		}
		if got := store.DisabledNodeIDs(); len(got) != 0 {
			t.Errorf("DisabledNodeIDs() = %v, want none before publish", got)
		}
		if got := store.MaintenanceNodeIDs(); len(got) != 0 {
			t.Errorf("MaintenanceNodeIDs() = %v, want none", got)
		}
	})

	close(writer.release)
	if err := <-disabled; err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if !store.IsDisabled("node-1") {
		t.Error("IsDisabled(node-1) = false after Disable returned")
	}
}

// A Reserve that starts while a Disable is mid-write must queue behind it and
// then see the disable: the ErrDisabled check is made inside the writers'
// critical section, against the latest published state, not against a
// snapshot taken before the Disable finished.
//
// 在 Disable 写到一半时开始的 Reserve 必须排在它后面，然后看到该禁用：ErrDisabled 的检查
// 在写者的临界区内、基于最新发布的状态进行，而不是基于 Disable 完成之前取的快照。
func TestReserveAndSetQueueBehindAnInFlightDisable(t *testing.T) {
	for _, name := range []string{"Reserve", "Set"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "identities.json")
			store, err := identitystore.Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			now := time.Now()
			writer := newBlockingWriter()
			identitystore.SetWriteFileForTest(store, writer.write)

			disabled := make(chan error, 1)
			go func() { disabled <- store.Disable("node-1", now) }()
			<-writer.started

			result := make(chan error, 1)
			go func() {
				if name == "Reserve" {
					_, err := store.Reserve("node-1", "fp-a", now)
					result <- err
					return
				}
				result <- store.Set("node-1", "fp-a", now)
			}()
			select {
			case err := <-result:
				t.Fatalf("%s returned %v while a Disable was still writing; it must wait for the writer lock", name, err)
			case <-time.After(50 * time.Millisecond):
			}

			close(writer.release)
			if err := <-disabled; err != nil {
				t.Fatalf("Disable() error = %v", err)
			}
			if err := <-result; !errors.Is(err, identitystore.ErrDisabled) {
				t.Fatalf("%s error = %v, want ErrDisabled", name, err)
			}
			if store.IsApproved("node-1") {
				t.Error("the losing registration left the node approved")
			}
		})
	}
}

func TestFailedPersistNeverAdvancesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()
	if _, err := store.Reserve("bound", "fp-a", now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Disable("off", now); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if err := store.SetMaintenance("maint", now); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}
	if err := store.RecordPending("pending", now); err != nil {
		t.Fatalf("RecordPending() error = %v", err)
	}
	onDisk := func() []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading ledger: %v", err)
		}
		return data
	}
	before := snapshotOf(store)
	fileBefore := string(onDisk())

	boom := errors.New("disk full")
	identitystore.SetWriteFileForTest(store, func(string, []byte, os.FileMode) error { return boom })

	later := now.Add(time.Hour)
	failures := map[string]func() error{
		"Reserve new":            func() error { _, err := store.Reserve("fresh", "fp-x", later); return err },
		"Reserve existing":       func() error { _, err := store.Reserve("bound", "fp-a", later); return err },
		"Reserve unbound record": func() error { _, err := store.Reserve("maint", "fp-m", later); return err },
		"Set new":                func() error { return store.Set("fresh2", "fp-y", later) },
		"Set existing":           func() error { return store.Set("bound", "fp-z", later) },
		"Disable new":            func() error { return store.Disable("fresh3", later) },
		"Disable existing":       func() error { return store.Disable("bound", later) },
		"Enable":                 func() error { return store.Enable("off", later) },
		"Delete new":             func() error { return store.Delete("fresh4", later) },
		"Delete existing":        func() error { return store.Delete("bound", later) },
		"RecordPending new":      func() error { return store.RecordPending("fresh5", later) },
		"RecordPending existing": func() error { return store.RecordPending("pending", later) },
		"Approve new":            func() error { return store.Approve("fresh6", later) },
		"Approve existing":       func() error { return store.Approve("pending", later) },
		"SetMaintenance new":     func() error { return store.SetMaintenance("fresh7", later) },
		"SetMaintenance existing": func() error {
			return store.SetMaintenance("bound", later)
		},
		"ClearMaintenance": func() error { return store.ClearMaintenance("maint", later) },
	}
	names := make([]string, 0, len(failures))
	for name := range failures {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := failures[name](); !errors.Is(err, boom) {
			t.Errorf("%s error = %v, want the persist error", name, err)
		}
		if got := snapshotOf(store); !equalStates(got, before) {
			t.Fatalf("%s advanced in-memory state despite the failed write:\n got %+v\nwant %+v", name, got, before)
		}
		if store.IsDisabled("bound") || store.HasRecord("fresh") || !store.IsDisabled("off") {
			t.Fatalf("%s changed what readers observe", name)
		}
	}
	if string(onDisk()) != fileBefore {
		t.Error("the ledger file changed although every write failed")
	}

	// Recovery: once the disk works again the same operation succeeds, so a
	// failed write left no half-applied state behind.
	// 恢复：磁盘恢复后同一操作能成功，说明失败的写入没有留下半应用的状态。
	identitystore.SetWriteFileForTest(store, func(path string, data []byte, mode os.FileMode) error {
		return os.WriteFile(path, data, mode)
	})
	if err := store.Disable("bound", later); err != nil {
		t.Fatalf("Disable() after recovery error = %v", err)
	}
	if !store.IsDisabled("bound") {
		t.Error("Disable() after recovery did not take effect")
	}
}

func snapshotOf(s *identitystore.Store) []identitystore.State {
	states := s.States()
	sort.Slice(states, func(i, j int) bool { return states[i].NodeID < states[j].NodeID })
	return states
}

func equalStates(a, b []identitystore.State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.NodeID != y.NodeID || x.PendingApproval != y.PendingApproval || x.Disabled != y.Disabled ||
			x.Maintenance != y.Maintenance || x.KeyFingerprint != y.KeyFingerprint ||
			!x.FirstSeenAt.Equal(y.FirstSeenAt) || !x.LastSeenAt.Equal(y.LastSeenAt) {
			return false
		}
	}
	return true
}

// The on-disk format is unchanged: a JSON array of objects keyed exactly as
// before, so an older Registry binary can still read a file this one wrote.
//
// 磁盘格式不变：仍是键名与之前完全一致的 JSON 对象数组，旧版 Registry 二进制依然能读本
// 版本写出的文件。
func TestLedgerFileKeepsItsFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()
	if _, err := store.Reserve("node-1", "fp-a", now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.SetMaintenance("node-1", now); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(data, &recs); err != nil {
		t.Fatalf("ledger is not a JSON array of objects: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(recs))
	}
	for _, key := range []string{"node_id", "fingerprint", "first_seen_at", "last_seen_at", "approved", "maintenance"} {
		if _, ok := recs[0][key]; !ok {
			t.Errorf("record is missing key %q: %v", key, recs[0])
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want just the ledger (no stray temp files)", len(entries))
	}
}

func TestSyncDirReportsMissingDirectoryExceptOnWindows(t *testing.T) {
	if err := identitystore.SyncDirForTest(t.TempDir()); err != nil {
		t.Fatalf("syncDir(existing dir) error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "nope")
	err := identitystore.SyncDirForTest(missing)
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("syncDir on windows should be a no-op, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("syncDir(missing dir) = nil, want an error")
	}
}
