package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

// fakeArtifactCleanupClient answers ListExpiredJobArtifacts from a
// per-type script and records every DeleteJobArtifact call, so a test can
// assert both what the sweeper asked for and what it went on to delete.
//
// fakeArtifactCleanupClient 按类型脚本化地应答 ListExpiredJobArtifacts，并
// 记录每一次 DeleteJobArtifact 调用，好让测试既能断言扫描器问了什么，也能
// 断言它接下来删除了什么。
type fakeArtifactCleanupClient struct {
	mu sync.Mutex

	expired    map[string][]ExpiredJobArtifact
	listCutoff map[string]time.Time

	deleteCalls []deleteCall
	deleteErr   func(jobID, artifactID string) error
}

type deleteCall struct {
	jobID, artifactID string
}

func newFakeArtifactCleanupClient() *fakeArtifactCleanupClient {
	return &fakeArtifactCleanupClient{
		expired:    map[string][]ExpiredJobArtifact{},
		listCutoff: map[string]time.Time{},
	}
}

func (f *fakeArtifactCleanupClient) ListExpiredJobArtifacts(_ context.Context, artifactType string, cutoff time.Time) ([]ExpiredJobArtifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCutoff[artifactType] = cutoff
	return f.expired[artifactType], nil
}

func (f *fakeArtifactCleanupClient) DeleteJobArtifact(_ context.Context, jobID, artifactID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, deleteCall{jobID, artifactID})
	if f.deleteErr != nil {
		return f.deleteErr(jobID, artifactID)
	}
	return nil
}

func (f *fakeArtifactCleanupClient) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.deleteCalls))
	for i, c := range f.deleteCalls {
		out[i] = c.artifactID
	}
	return out
}

func (f *fakeArtifactCleanupClient) cutoffFor(artifactType string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.listCutoff[artifactType]
	return c, ok
}

var _ ArtifactCleanupClient = (*fakeArtifactCleanupClient)(nil)

// failingBackend wraps a real objectstore.Backend and fails every Delete,
// standing in for an object storage outage without depending on filesystem
// permission quirks that vary across environments (root ignores them,
// sandboxes may too).
//
// failingBackend 包装一个真实的 objectstore.Backend 并让每一次 Delete 都
// 失败，用它代替一次对象存储故障，而不依赖在不同环境下表现不一的文件系统
// 权限位（root 会无视它们，沙箱环境也可能一样）。
type failingBackend struct {
	objectstore.Backend
}

func (b *failingBackend) Delete(context.Context, string) error {
	return errors.New("fake: object storage is unreachable")
}

func TestArtifactCleanerReapsExpiredArtifactsAcrossConfiguredTypes(t *testing.T) {
	clock := gatewaytest.NewClock()
	client := newFakeArtifactCleanupClient()
	client.expired["output"] = []ExpiredJobArtifact{{ArtifactID: "art_out_1", JobID: "job_1"}}
	client.expired["temp"] = []ExpiredJobArtifact{{ArtifactID: "art_temp_1", JobID: "job_2"}}

	c := newArtifactCleaner(client, nil, clock, discardLogger(), artifactCleanupConfig{
		RetentionByType: map[string]time.Duration{"output": time.Hour, "temp": time.Minute},
	})
	c.tick()

	deleted := client.deletedIDs()
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want 2 artifacts", deleted)
	}
	found := map[string]bool{}
	for _, id := range deleted {
		found[id] = true
	}
	if !found["art_out_1"] || !found["art_temp_1"] {
		t.Errorf("deleted = %v, want both art_out_1 and art_temp_1", deleted)
	}
}

func TestArtifactCleanerUsesEachTypesOwnRetentionAsTheCutoff(t *testing.T) {
	clock := gatewaytest.NewClock()
	client := newFakeArtifactCleanupClient()

	c := newArtifactCleaner(client, nil, clock, discardLogger(), artifactCleanupConfig{
		RetentionByType: map[string]time.Duration{"output": 30 * 24 * time.Hour, "temp": time.Hour},
	})
	c.tick()

	outputCutoff, ok := client.cutoffFor("output")
	if !ok {
		t.Fatal("ListExpiredJobArtifacts was never called for type=output")
	}
	tempCutoff, ok := client.cutoffFor("temp")
	if !ok {
		t.Fatal("ListExpiredJobArtifacts was never called for type=temp")
	}
	if want := clock.Now().Add(-30 * 24 * time.Hour); !outputCutoff.Equal(want) {
		t.Errorf("output cutoff = %v, want %v (now - 30d)", outputCutoff, want)
	}
	if want := clock.Now().Add(-time.Hour); !tempCutoff.Equal(want) {
		t.Errorf("temp cutoff = %v, want %v (now - 1h)", tempCutoff, want)
	}
	// The preview type's cutoff must be strictly more recent than output's —
	// the whole point of giving it a shorter retention.
	//
	// 预览类型的截止时刻必须严格晚于 output 的——这正是给它更短保留期的意义
	// 所在。
	if !tempCutoff.After(outputCutoff) {
		t.Errorf("temp cutoff %v is not after output cutoff %v, want the shorter retention to produce a more recent cutoff", tempCutoff, outputCutoff)
	}
}

func TestArtifactCleanerDeletesStorageBytesBeforeTheMetadataRow(t *testing.T) {
	clock := gatewaytest.NewClock()
	client := newFakeArtifactCleanupClient()
	storage, err := objectstore.NewLocal(objectstore.LocalConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if err := storage.Put(context.Background(), "tenant-a/job_1/art_1", strings.NewReader("bytes"), 5); err != nil {
		t.Fatalf("Put: %v", err)
	}
	client.expired["output"] = []ExpiredJobArtifact{{ArtifactID: "art_1", JobID: "job_1", StorageKey: "tenant-a/job_1/art_1"}}

	c := newArtifactCleaner(client, storage, clock, discardLogger(), artifactCleanupConfig{
		RetentionByType: map[string]time.Duration{"output": time.Hour},
	})
	c.tick()

	if len(client.deletedIDs()) != 1 {
		t.Fatalf("deleted = %v, want 1", client.deletedIDs())
	}
	if _, _, err := storage.Open(context.Background(), "tenant-a/job_1/art_1"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("storage.Open after sweep: err = %v, want objectstore.ErrNotFound (bytes should be gone)", err)
	}
}

func TestArtifactCleanerLeavesTheRowWhenStorageDeleteFails(t *testing.T) {
	clock := gatewaytest.NewClock()
	client := newFakeArtifactCleanupClient()
	client.expired["output"] = []ExpiredJobArtifact{{ArtifactID: "art_1", JobID: "job_1", StorageKey: "tenant-a/job_1/art_1"}}

	real, err := objectstore.NewLocal(objectstore.LocalConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	storage := &failingBackend{Backend: real}

	c := newArtifactCleaner(client, storage, clock, discardLogger(), artifactCleanupConfig{
		RetentionByType: map[string]time.Duration{"output": time.Hour},
	})
	c.tick()

	if deleted := client.deletedIDs(); len(deleted) != 0 {
		t.Errorf("DeleteJobArtifact was called = %v, want none: a failed storage delete must leave the row for a future sweep to retry", deleted)
	}
}

func TestArtifactCleanerSkipsStorageDeleteWhenThereIsNoStorageKey(t *testing.T) {
	clock := gatewaytest.NewClock()
	client := newFakeArtifactCleanupClient()
	// storage is nil: an artifact recorded before ArtifactStorage was ever
	// configured, or while it was disabled, still gets its metadata row
	// reaped.
	//
	// storage 为 nil：一个在 ArtifactStorage 从未配置过、或被关闭期间记录的
	// 产物，其元数据行依然会被回收。
	client.expired["output"] = []ExpiredJobArtifact{{ArtifactID: "art_1", JobID: "job_1", StorageKey: ""}}

	c := newArtifactCleaner(client, nil, clock, discardLogger(), artifactCleanupConfig{
		RetentionByType: map[string]time.Duration{"output": time.Hour},
	})
	c.tick()

	if deleted := client.deletedIDs(); len(deleted) != 1 || deleted[0] != "art_1" {
		t.Errorf("deleted = %v, want [art_1]", deleted)
	}
}

func TestArtifactCleanerContinuesAfterOneArtifactFailsToDelete(t *testing.T) {
	clock := gatewaytest.NewClock()
	client := newFakeArtifactCleanupClient()
	client.expired["output"] = []ExpiredJobArtifact{
		{ArtifactID: "art_bad", JobID: "job_1"},
		{ArtifactID: "art_good", JobID: "job_2"},
	}
	client.deleteErr = func(_, artifactID string) error {
		if artifactID == "art_bad" {
			return errUnreachable
		}
		return nil
	}

	c := newArtifactCleaner(client, nil, clock, discardLogger(), artifactCleanupConfig{
		RetentionByType: map[string]time.Duration{"output": time.Hour},
	})
	c.tick()

	deleted := client.deletedIDs()
	if len(deleted) != 2 {
		t.Fatalf("DeleteJobArtifact calls = %v, want 2 (both attempted even though one failed)", deleted)
	}
}

func TestArtifactCleanerRunTicksOnItsIntervalAndStopsCleanly(t *testing.T) {
	clock := gatewaytest.NewClock()
	client := newFakeArtifactCleanupClient()
	client.expired["output"] = []ExpiredJobArtifact{{ArtifactID: "art_1", JobID: "job_1"}}

	c := newArtifactCleaner(client, nil, clock, discardLogger(), artifactCleanupConfig{
		Interval:        time.Hour,
		RetentionByType: map[string]time.Duration{"output": time.Minute},
	})
	go c.run()

	gatewaytest.WaitFor(t, "the cleaner to arm its first timer", func() bool { return clock.PendingTimers() >= 1 })
	clock.Advance(time.Hour)
	gatewaytest.WaitFor(t, "the tick to reap the expired artifact", func() bool {
		return len(client.deletedIDs()) > 0
	})

	c.Stop()
}
