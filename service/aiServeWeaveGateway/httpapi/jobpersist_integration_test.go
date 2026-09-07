package httpapi_test

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
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
	mu      sync.Mutex
	created []string
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

func (f *fakeJobPersistClient) CreateJobArtifact(context.Context, string, string, string, string, string, string) error {
	return nil
}

func (f *fakeJobPersistClient) createdJobIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...)
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
