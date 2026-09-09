package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

// fakeJobPersistClient is a minimal httpapi.JobPersistClient for asserting
// that a real HTTP submit reaches the persister end to end — jobpersist_test.go
// covers the persister's own scheduling and retry behaviour in detail; this
// file only checks that httpapi.New actually wires Config.JobPersistClient
// into the submit path, not the other way around.
//
// fakeJobPersistClient 是一个最小的 httpapi.JobPersistClient，用来断言一次
// 真实的 HTTP 提交端到端地抵达了持久化器——持久化器自己的调度与重试行为已经
// 由 jobpersist_test.go 详细覆盖；本文件只检查 httpapi.New 是否真的把
// Config.JobPersistClient 接进了提交路径，而不是反过来。
type fakeJobPersistClient struct {
	mu        sync.Mutex
	created   []string
	artifacts []artifactPersistCall
}

// artifactPersistCall is one CreateJobArtifact call this fake received,
// recorded in full so TestArtifactBytesArePersistedToConfiguredStorageAndServedFromThere
// can find the storage key jobPersister minted for a given artifact.
//
// artifactPersistCall 是这个假客户端收到的一次 CreateJobArtifact 调用，完整
// 记录下来，好让 TestArtifactBytesArePersistedToConfiguredStorageAndServedFromThere
// 能找到 jobPersister 为某个产物铸造的存储 key。
type artifactPersistCall struct {
	jobID, artifactID, sha256, contentType, storageKey string
	sizeBytes                                          int64
}

func (f *fakeJobPersistClient) CreateJob(_ context.Context, jobID, _, _, _, _, _, _, _ string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, jobID)
	return nil
}

func (f *fakeJobPersistClient) UpdateJobState(context.Context, string, string, string, string, int64) (bool, error) {
	return true, nil
}

func (f *fakeJobPersistClient) CreateJobArtifact(_ context.Context, jobID, artifactID, _, _, _, _, sha256, contentType, storageKey string, sizeBytes int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.artifacts = append(f.artifacts, artifactPersistCall{jobID, artifactID, sha256, contentType, storageKey, sizeBytes})
	return nil
}

func (f *fakeJobPersistClient) createdJobIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...)
}

func (f *fakeJobPersistClient) artifactCalls() []artifactPersistCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]artifactPersistCall(nil), f.artifacts...)
}

var _ httpapi.JobPersistClient = (*fakeJobPersistClient)(nil)

// TestSubmitWorkflowRunReachesTheConfiguredJobPersistClient is the wiring
// assertion for STATUS.md's J05: a real submit through the HTTP front door
// must, without the caller polling or waiting for it, end up as a CreateJob
// call to whatever JobPersistClient Config names. It does not assert on
// timing beyond "eventually" — nudge only asks the persister's loop to run
// sooner, and the front door has already answered 202 before that happens.
//
// TestSubmitWorkflowRunReachesTheConfiguredJobPersistClient 是 STATUS.md
// J05 的接线断言：一次经由 HTTP 前门的真实提交，无需调用方轮询或等待，最终
// 必须变成对 Config 所指定的 JobPersistClient 的一次 CreateJob 调用。它除了
// 「最终会发生」之外不断言具体时机——nudge 只是请求持久化器的循环提早运行，
// 而前门在那发生之前就已经答复了 202。
func TestSubmitWorkflowRunReachesTheConfiguredJobPersistClient(t *testing.T) {
	client := &fakeJobPersistClient{}
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), JobPersistClient: client})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	resp, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	gatewaytest.WaitFor(t, "the persister to report the new job to the control plane client", func() bool {
		return slices.Contains(client.createdJobIDs(), job.JobID)
	})
}

// TestArtifactBytesArePersistedToConfiguredStorageAndServedFromThere is the
// end-to-end wiring assertion for STATUS.md's P04: a real HTTP submit,
// listing and download, with a real (local-disk) objectstore.Backend
// configured, must result in the artifact's actual bytes landing in that
// backend under the storage key reported to the control plane client — and
// GET /v1/artifacts/{id} must serve exactly those bytes. jobpersist_test.go
// already covers persistArtifactBytes's hashing, counting and retry
// behaviour against fakes; this file only checks that httpapi.New wires a
// real objectstore.Backend all the way from Config.ArtifactStorage through
// jobPersister to downloadArtifact.
//
// TestArtifactBytesArePersistedToConfiguredStorageAndServedFromThere 是
// STATUS.md P04 的端到端接线断言：配置了一个真实（本地磁盘）objectstore.Backend
// 时，一次真实的 HTTP 提交、列举与下载，必须让产物的真实字节落进该后端、键为
// 上报给控制面客户端的那个存储 key——而 GET /v1/artifacts/{id} 必须原样
// 送出这些字节。jobpersist_test.go 已经针对假对象覆盖了
// persistArtifactBytes 的哈希、计数与重试行为；本文件只检查 httpapi.New
// 是否把一个真实的 objectstore.Backend 从 Config.ArtifactStorage 一路接到
// jobPersister 再到 downloadArtifact。
func TestArtifactBytesArePersistedToConfiguredStorageAndServedFromThere(t *testing.T) {
	storage, err := objectstore.NewLocal(objectstore.LocalConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	client := &fakeJobPersistClient{}
	srv, h := newServer(t, httpapi.Config{
		Workflows:        templates(t),
		JobPersistClient: client,
		ArtifactStorage:  storage,
		// The persister's default interval is 5s and nothing nudges it for
		// a newly listed artifact, so this shortens the wait to keep the
		// test fast without racing gatewaytest.Timeout.
		//
		// 持久化器的默认间隔是 5 秒，而新列出的产物不会触发任何 nudge，
		// 缩短这个间隔是为了让测试保持快速，同时不与 gatewaytest.Timeout
		// 竞速。
		PersistInterval: 50 * time.Millisecond,
	})
	connectArtifactNode(t, h, "node-comfy", "comfy-1", artifactWorkflowHandler("ComfyUI_00001_.png", nil))

	jobID := finishedJob(t, srv.URL)
	_, listing := listArtifacts(t, srv.URL, jobID)
	if len(listing.Data) != 1 {
		t.Fatalf("listed %d artifacts, want 1", len(listing.Data))
	}
	artifactID := listing.Data[0].ArtifactID

	var storageKey string
	gatewaytest.WaitFor(t, "the persister to copy the artifact into object storage", func() bool {
		for _, c := range client.artifactCalls() {
			if c.artifactID == artifactID && c.storageKey != "" {
				storageKey = c.storageKey
				return true
			}
		}
		return false
	})

	// The bytes are really in the configured backend — proof independent of
	// the download path checked below.
	//
	// 字节确实在配置的后端里——这是独立于下面所检查的下载路径之外的证据。
	rc, _, err := storage.Open(context.Background(), storageKey)
	if err != nil {
		t.Fatalf("storage.Open(%q): %v", storageKey, err)
	}
	stored, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("reading stored artifact: %v", err)
	}
	if string(stored) != artifactBytes {
		t.Errorf("stored bytes = %q, want %q", stored, artifactBytes)
	}

	resp, err := http.Get(srv.URL + "/v1/artifacts/" + artifactID)
	if err != nil {
		t.Fatalf("GET artifact: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	downloaded, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading download body: %v", err)
	}
	if string(downloaded) != artifactBytes {
		t.Errorf("downloaded body = %q, want %q", downloaded, artifactBytes)
	}
}
