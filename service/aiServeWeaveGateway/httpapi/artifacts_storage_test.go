package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

// TestDownloadArtifactPrefersStorageWhenPersisted seeds a jobStore artifact
// record with a StorageKey — exactly what jobPersister.persistArtifacts sets
// on a successful copy — and drives downloadArtifact directly, with h.sched
// left nil. Reaching the live-node fallback would nil-pointer-panic on
// h.sched.OpenArtifact, so a passing test is itself the proof that the
// storage path was taken and the node was never asked.
//
// TestDownloadArtifactPrefersStorageWhenPersisted 为 jobStore 的一条产物记录
// 预先写入 StorageKey——正是 jobPersister.persistArtifacts 在一次成功复制后
// 会设置的东西——并直接驱动 downloadArtifact，同时让 h.sched 保持为 nil。
// 一旦落入实时节点的回退路径，h.sched.OpenArtifact 会发生空指针 panic，因此
// 测试通过本身就证明了走的是存储路径，节点从未被询问过。
func TestDownloadArtifactPrefersStorageWhenPersisted(t *testing.T) {
	storage, err := objectstore.NewLocal(objectstore.LocalConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	const body = "stored bytes, not the node's"
	if err := storage.Put(context.Background(), "k", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	jobs := newJobStore(0)
	addTestJob(jobs, "job_1", "tenant-a", "run-1", time.Now())
	ids := jobs.recordArtifacts("job_1", []runtime.ArtifactRef{{RunID: "run-1", Filename: "out.png", Type: "output"}})
	jobs.artifactStored(ids[0], "k", "image/png", int64(len(body)))

	h := &handlers{jobs: jobs, storage: storage, logger: discardLogger()}

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+ids[0], nil)
	req.SetPathValue("artifact_id", ids[0])
	req = req.WithContext(context.WithValue(req.Context(), identityKey{}, Identity{TenantID: "tenant-a"}))
	rec := httptest.NewRecorder()

	h.downloadArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != body {
		t.Errorf("body = %q, want %q", rec.Body.String(), body)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
}
