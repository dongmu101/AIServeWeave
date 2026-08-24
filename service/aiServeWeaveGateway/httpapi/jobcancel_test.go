package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// cancellableWorkflowHandler answers Submit, Status and Cancel. cancelErr, if
// set, is what Cancel fails with, which is how the unsupported case is driven.
//
// cancellableWorkflowHandler 应答 Submit、Status 与 Cancel。cancelErr 若已设置，
// 就是 Cancel 失败时返回的错误，未支持取消那个用例正是这样驱动的。
func cancellableWorkflowHandler(cancelErr *gatewaytest.WireError, cancels *int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		switch req.GetOperation() {
		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
			payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "prompt-1"})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_WORKFLOW_STATUS:
			payload, err := tunnelwire.MarshalWorkflowStatus(runtime.WorkflowStatus{State: runtime.WorkflowRunning})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE:
			payload, err := tunnelwire.MarshalWorkflowEvent(runtime.WorkflowEvent{
				Type: runtime.WorkflowEventSucceeded, RunID: "prompt-1",
			})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL:
			if cancels != nil {
				*cancels++
			}
			if cancelErr != nil {
				return cancelErr
			}
			return nil
		}
		return errors.New("unsupported operation")
	}
}

func postCancel(t *testing.T, url, jobID string) (*http.Response, jobBody) {
	t.Helper()
	resp, err := http.Post(url+"/v1/jobs/"+jobID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	defer resp.Body.Close()
	var job jobBody
	_ = json.NewDecoder(resp.Body).Decode(&job)
	return resp, job
}

func TestCancelJobReachesTheNode(t *testing.T) {
	var cancels int32
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), cancellableWorkflowHandler(nil, &cancels))

	_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	resp, got := postCancel(t, srv.URL, job.JobID)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if cancels != 1 {
		t.Errorf("the node received %d cancels, want 1", cancels)
	}
	if got.JobID != job.JobID {
		t.Errorf("job_id = %q, want %q", got.JobID, job.JobID)
	}
	// Cancellation is a request, not a verdict: ComfyUI's interrupt is
	// asynchronous, so the job stays in whatever state it was in until the
	// backend actually says otherwise. Reporting "cancelled" here would be
	// this Gateway inventing an outcome it has not been told.
	//
	// 取消是一个请求而不是一个结论：ComfyUI 的中断是异步的，因此在后端真正另有说法
	// 之前，job 保持它原本的状态。在这里报告「已取消」，就是本 Gateway 在编造一个
	// 没人告诉过它的结果。
	if got.Status == "cancelled" {
		t.Errorf("status = %q immediately after the cancel request, want the state the backend last reported", got.Status)
	}
}

// TestCancelJobRejects covers the answers that are not "accepted".
//
// TestCancelJobRejects 覆盖那些答案不是「已接受」的情况。
func TestCancelJobRejects(t *testing.T) {
	tests := []struct {
		name string
		// finish drives the job to a terminal state before cancelling.
		//
		// finish 在取消之前把 job 推到终态。
		finish     bool
		useJobID   string
		cancelErr  *gatewaytest.WireError
		wantStatus int
		wantCancel int32
	}{
		{
			name:       "unknown job",
			useJobID:   "job_nope",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "job that already finished",
			finish:     true,
			wantStatus: http.StatusConflict,
			wantCancel: 0,
		},
		{
			name:       "node does not support cancellation",
			cancelErr:  &gatewaytest.WireError{Code: "cancel_unsupported", Message: "this backend cannot interrupt", Retryable: false},
			wantStatus: http.StatusNotImplemented,
			wantCancel: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cancels int32
			srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
			connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), cancellableWorkflowHandler(tt.cancelErr, &cancels))

			_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
			jobID := job.JobID
			if tt.useJobID != "" {
				jobID = tt.useJobID
			}
			if tt.finish {
				// The event stream's terminal frame is what records the
				// finished state, the same way a real run would.
				//
				// 事件流的终态帧才是记录「已结束」的那一步，与真实运行的路径一致。
				openEvents(t, srv.URL, job.JobID)
			}

			resp, _ := postCancel(t, srv.URL, jobID)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if cancels != tt.wantCancel {
				t.Errorf("the node received %d cancels, want %d", cancels, tt.wantCancel)
			}
		})
	}
}

// TestCancelJobIsScopedToItsTenant keeps one tenant from interrupting
// another's generation — a write, and so a worse disclosure than reading its
// status.
//
// TestCancelJobIsScopedToItsTenant 防止一个租户中断另一个租户的生成——那是一次写入，
// 因而比读取其状态是更糟的越权。
func TestCancelJobIsScopedToItsTenant(t *testing.T) {
	var cancels int32
	verifier := &fakeVerifier{answer: func(key string) (httpapi.Identity, error) {
		switch key {
		case "key-a":
			return httpapi.Identity{TenantID: "tnt_a", KeyID: "k_a"}, nil
		case "key-b":
			return httpapi.Identity{TenantID: "tnt_b", KeyID: "k_b"}, nil
		}
		return httpapi.Identity{}, httpapi.ErrKeyRejected
	}}
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), Verifier: verifier})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), cancellableWorkflowHandler(nil, &cancels))

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/workflows/text-to-image/runs",
		strings.NewReader(`{"inputs":{"prompt":"a red fox"}}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer key-a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var job jobBody
	_ = json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()

	req, err = http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs/"+job.JobID+"/cancel", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer key-b")
	other, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	defer other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("status for another tenant = %d, want 404", other.StatusCode)
	}
	if cancels != 0 {
		t.Errorf("the node received %d cancels from a tenant that does not own the job, want 0", cancels)
	}
}

func TestCancelJobEndpointLabelIsItsOwn(t *testing.T) {
	mx := metricstest.New()
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), Metrics: mx})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), cancellableWorkflowHandler(nil, nil))

	_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	postCancel(t, srv.URL, job.JobID)

	seen := make(map[string]bool)
	for _, s := range mx.All() {
		if value, ok := s.Labels[httpapi.LabelEndpoint]; ok {
			seen[value] = true
			if strings.Contains(value, job.JobID) {
				t.Errorf("metric %s carries endpoint=%q: a job id reached a label", s.Name, value)
			}
		}
	}
	if !seen[httpapi.EndpointJobCancel] {
		t.Errorf("no metric was recorded under endpoint=%q; seen: %v", httpapi.EndpointJobCancel, seen)
	}
}
