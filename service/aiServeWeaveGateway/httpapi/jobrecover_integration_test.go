package httpapi_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// fakeJobRecoveryClient is a minimal httpapi.JobRecoveryClient for asserting
// that a recovered job becomes servable through the real HTTP front door —
// jobrecover_test.go covers the recoverer's own scheduling and bookkeeping
// in detail; this file only checks that httpapi.New wires
// Config.JobRecoveryClient into a live server, and that what comes back is
// something GET /v1/jobs/{job_id} can actually answer.
//
// fakeJobRecoveryClient 是一个最小的 httpapi.JobRecoveryClient，用来断言一个
// 被恢复的 job 会经由真实的 HTTP 前门变得可服务——恢复器自己的调度与记账已经
// 由 jobrecover_test.go 详细覆盖；本文件只检查 httpapi.New 是否把
// Config.JobRecoveryClient 接进了一个真实运行的 server，以及恢复回来的东西
// 是否真的能被 GET /v1/jobs/{job_id} 回答。
type fakeJobRecoveryClient struct {
	mu   sync.Mutex
	byID map[string][]httpapi.RecoveredJob
}

func newFakeJobRecoveryClient() *fakeJobRecoveryClient {
	return &fakeJobRecoveryClient{byID: map[string][]httpapi.RecoveredJob{}}
}

func (f *fakeJobRecoveryClient) ListActiveJobsForRoute(_ context.Context, nodeID, runtimeID string) ([]httpapi.RecoveredJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byID[nodeID+"/"+runtimeID], nil
}

func (f *fakeJobRecoveryClient) seed(nodeID, runtimeID string, jobs ...httpapi.RecoveredJob) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[nodeID+"/"+runtimeID] = jobs
}

var _ httpapi.JobRecoveryClient = (*fakeJobRecoveryClient)(nil)

// TestARecoveredJobIsServableThroughTheHTTPFrontDoor is the wiring assertion
// for STATUS.md's J06: a job this replica never submitted itself, but that
// the control plane says is bound to a node currently connected here, ends
// up answerable by GET /v1/jobs/{job_id} — the same endpoint a caller would
// have polled if this replica had never restarted. The recoverer runs on a
// short real interval here rather than the production default: this test
// drives httpapi.New's own system clock in real time rather than a fake one,
// since newServer builds its scheduler and Config.Clock from two different
// clocks and only the former is reachable before the server exists.
//
// TestARecoveredJobIsServableThroughTheHTTPFrontDoor 是 STATUS.md J06 的
// 接线断言：一个本副本自己从未提交过、但控制面说绑定在此刻已连接节点上的
// job，最终能被 GET /v1/jobs/{job_id} 回答——与一个调用方在本副本从未重启过
// 时会去轮询的是同一个端点。这里恢复器用一个很短的真实间隔运行，而不是生产
// 默认值：本测试是在真实时间里驱动 httpapi.New 自己的系统时钟，而不是假时钟，
// 因为 newServer 用两个不同的时钟分别构造它的 scheduler 与 Config.Clock，
// 而 server 建好之前只有前者可以被拿到。
func TestARecoveredJobIsServableThroughTheHTTPFrontDoor(t *testing.T) {
	client := newFakeJobRecoveryClient()
	srv, h := newServer(t, httpapi.Config{
		Workflows:         templates(t),
		JobRecoveryClient: client,
		RecoverInterval:   20 * time.Millisecond,
	})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	client.seed("node-comfy", "comfy-1", httpapi.RecoveredJob{
		JobID: "job_recovered_1", TenantID: "", WorkflowID: "text-to-image",
		NodeID: "node-comfy", RuntimeID: "comfy-1", BackendRunID: "prompt-1",
		State: "running", ObservedSeq: 3,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	gatewaytest.WaitFor(t, "the recovered job to become servable", func() bool {
		resp, err := http.Get(srv.URL + "/v1/jobs/job_recovered_1")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}
