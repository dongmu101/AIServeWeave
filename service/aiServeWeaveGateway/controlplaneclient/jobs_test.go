package controlplaneclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
)

// fakeJobsControlPlane stands in for the control plane's internal Job
// endpoints. Like fakeControlPlane in verifier_test.go, it records what it
// received rather than reusing the real handler stack: this package sits on
// the Gateway side of the boundary and must not import the control plane
// service to test against it.
//
// fakeJobsControlPlane 是控制面内部 Job 端点的替身。与 verifier_test.go 里的
// fakeControlPlane 一样，它记录自己收到了什么，而不是复用真实的 handler 栈：
// 本包位于这条边界的 Gateway 一侧，测试它不应该反过来 import 控制面服务。
type fakeJobsControlPlane struct {
	server *httptest.Server

	mu           sync.Mutex
	lastMethod   string
	lastPath     string
	lastAuth     string
	lastBody     map[string]any
	requestCount int

	// respond answers one call. Replace it to script a status code or body.
	//
	// respond 应答一次调用。替换它即可脚本化一个状态码或响应体。
	respond func(w http.ResponseWriter, r *http.Request)
}

func newFakeJobsControlPlane(t *testing.T) *fakeJobsControlPlane {
	t.Helper()
	cp := &fakeJobsControlPlane{}
	cp.respond = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"job_id": "job_1", "tenant_id": "tenant-a", "workflow_id": "wf",
			"state": "pending", "observed_seq": 0,
			"created_at": time.Now().UTC(), "updated_at": time.Now().UTC(),
		})
	}
	cp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&decoded)
		}
		cp.mu.Lock()
		cp.requestCount++
		cp.lastMethod = r.Method
		cp.lastPath = r.URL.RequestURI()
		cp.lastAuth = r.Header.Get("Authorization")
		cp.lastBody = decoded
		cp.mu.Unlock()
		cp.respond(w, r)
	}))
	t.Cleanup(cp.server.Close)
	return cp
}

func newJobsClient(t *testing.T, cp *fakeJobsControlPlane) *controlplaneclient.JobsClient {
	t.Helper()
	client, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{
		Endpoint: cp.server.URL,
		Token:    testToken,
	})
	if err != nil {
		t.Fatalf("NewJobsClient: %v", err)
	}
	return client
}

func TestCreateJobSendsFieldsAndAuthAndDecodesTheResponse(t *testing.T) {
	cp := newFakeJobsControlPlane(t)
	client := newJobsClient(t, cp)

	job, err := client.CreateJob(context.Background(), controlplaneclient.CreateJobRequest{
		JobID: "job_1", TenantID: "tenant-a", WorkflowID: "wf",
		NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-1",
		State: "pending", ObservedSeq: 0,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if job.JobID != "job_1" || job.TenantID != "tenant-a" {
		t.Errorf("CreateJob returned %+v, want job_id=job_1 tenant_id=tenant-a", job)
	}

	if cp.lastMethod != http.MethodPost || cp.lastPath != "/internal/v1/jobs" {
		t.Errorf("request = %s %s, want POST /internal/v1/jobs", cp.lastMethod, cp.lastPath)
	}
	if cp.lastAuth != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want Bearer %s", cp.lastAuth, testToken)
	}
	if cp.lastBody["node_id"] != "node-1" || cp.lastBody["backend_run_id"] != "prompt-1" {
		t.Errorf("request body = %+v, want the route binding fields present — the internal channel is exactly where they must travel", cp.lastBody)
	}
}

func TestGetJobSendsTenantAsAQueryParameter(t *testing.T) {
	cp := newFakeJobsControlPlane(t)
	client := newJobsClient(t, cp)

	if _, err := client.GetJob(context.Background(), "tenant-a", "job_1"); err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if cp.lastMethod != http.MethodGet || cp.lastPath != "/internal/v1/jobs/job_1?tenant_id=tenant-a" {
		t.Errorf("request = %s %s, want GET /internal/v1/jobs/job_1?tenant_id=tenant-a", cp.lastMethod, cp.lastPath)
	}
}

func TestUpdateJobStateReturnsAppliedFalseWithoutError(t *testing.T) {
	cp := newFakeJobsControlPlane(t)
	cp.respond = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"applied": false,
			"job": map[string]any{
				"job_id": "job_1", "tenant_id": "tenant-a", "workflow_id": "wf",
				"state": "succeeded", "observed_seq": 5,
				"created_at": time.Now().UTC(), "updated_at": time.Now().UTC(),
			},
		})
	}
	client := newJobsClient(t, cp)

	applied, job, err := client.UpdateJobState(context.Background(), "tenant-a", "job_1", controlplaneclient.JobStateUpdate{
		State: "failed", ObservedSeq: 2,
	})
	if err != nil {
		t.Fatalf("UpdateJobState: %v, want nil — a stale update is a silent no-op, not an error", err)
	}
	if applied {
		t.Error("applied = true, want false")
	}
	if job.State != "succeeded" {
		t.Errorf("job.State = %q, want %q — the caller must see what actually landed even after losing the race", job.State, "succeeded")
	}
	if cp.lastMethod != http.MethodPatch || cp.lastPath != "/internal/v1/jobs/job_1/state" {
		t.Errorf("request = %s %s, want PATCH /internal/v1/jobs/job_1/state", cp.lastMethod, cp.lastPath)
	}
}

func TestListJobArtifactsSendsTenantAsAQueryParameter(t *testing.T) {
	cp := newFakeJobsControlPlane(t)
	cp.respond = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"artifact_id": "art_1", "job_id": "job_1", "tenant_id": "tenant-a", "filename": "out.png", "created_at": time.Now().UTC()},
		}})
	}
	client := newJobsClient(t, cp)

	artifacts, err := client.ListJobArtifacts(context.Background(), "tenant-a", "job_1")
	if err != nil {
		t.Fatalf("ListJobArtifacts: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].ArtifactID != "art_1" {
		t.Fatalf("ListJobArtifacts = %+v, want one artifact art_1", artifacts)
	}
	if cp.lastMethod != http.MethodGet || cp.lastPath != "/internal/v1/jobs/job_1/artifacts?tenant_id=tenant-a" {
		t.Errorf("request = %s %s, want GET /internal/v1/jobs/job_1/artifacts?tenant_id=tenant-a", cp.lastMethod, cp.lastPath)
	}
}

func TestCreateJobArtifactUsesTheJobIDInThePath(t *testing.T) {
	cp := newFakeJobsControlPlane(t)
	cp.respond = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"artifact_id": "art_1", "job_id": "job_1", "tenant_id": "tenant-a",
			"filename": "out.png", "created_at": time.Now().UTC(),
		})
	}
	client := newJobsClient(t, cp)

	artifact, err := client.CreateJobArtifact(context.Background(), "job_1", controlplaneclient.CreateJobArtifactRequest{
		ArtifactID: "art_1", TenantID: "tenant-a", Filename: "out.png",
	})
	if err != nil {
		t.Fatalf("CreateJobArtifact: %v", err)
	}
	if artifact.ArtifactID != "art_1" {
		t.Errorf("artifact.ArtifactID = %q, want art_1", artifact.ArtifactID)
	}
	if cp.lastPath != "/internal/v1/jobs/job_1/artifacts" {
		t.Errorf("request path = %q, want /internal/v1/jobs/job_1/artifacts", cp.lastPath)
	}
}

func TestStatusCodesMapToTheDocumentedErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"not found", http.StatusNotFound, controlplaneclient.ErrNotFound},
		{"conflict", http.StatusConflict, controlplaneclient.ErrConflict},
		{"bad request", http.StatusBadRequest, controlplaneclient.ErrInvalidRequest},
		{"this Gateway's own token rejected", http.StatusUnauthorized, controlplaneclient.ErrOutcomeUnknown},
		{"forbidden", http.StatusForbidden, controlplaneclient.ErrOutcomeUnknown},
		{"unexpected server error", http.StatusInternalServerError, controlplaneclient.ErrOutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp := newFakeJobsControlPlane(t)
			cp.respond = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status) }
			client := newJobsClient(t, cp)

			_, err := client.GetJob(context.Background(), "tenant-a", "job_1")
			if !errors.Is(err, tt.want) {
				t.Errorf("GetJob() with status %d = %v, want an error wrapping %v", tt.status, err, tt.want)
			}
		})
	}
}

// TestTransportFailureIsOutcomeUnknown asserts the persistence contract's
// "提交结果未知" state is reachable from a real transport failure, not just
// from a scripted status code: pointing the client at a server that has
// already stopped means every call fails the same way a real network
// partition would.
//
// TestTransportFailureIsOutcomeUnknown 断言持久化契约的「提交结果未知」状态
// 可以由一次真实的传输失败触达，而不只是来自一个脚本化的状态码：把客户端指向
// 一个已经停止的服务器，会让每次调用都以真实网络分区会造成的同一种方式失败。
func TestTransportFailureIsOutcomeUnknown(t *testing.T) {
	cp := newFakeJobsControlPlane(t)
	client := newJobsClient(t, cp)
	cp.server.Close() // stop it before the call, not after — the connection must actually fail

	_, err := client.GetJob(context.Background(), "tenant-a", "job_1")
	if !errors.Is(err, controlplaneclient.ErrOutcomeUnknown) {
		t.Errorf("GetJob() against a closed server = %v, want an error wrapping ErrOutcomeUnknown", err)
	}
}

func TestNewJobsClientRejectsAMisconfiguredEndpointOrToken(t *testing.T) {
	tests := []struct {
		name string
		cfg  controlplaneclient.JobsClientConfig
	}{
		{"no endpoint", controlplaneclient.JobsClientConfig{Token: testToken}},
		{"endpoint without a scheme", controlplaneclient.JobsClientConfig{Endpoint: "127.0.0.1:8090", Token: testToken}},
		{"no token", controlplaneclient.JobsClientConfig{Endpoint: "http://127.0.0.1:8090"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := controlplaneclient.NewJobsClient(tt.cfg); err == nil {
				t.Error("NewJobsClient() = nil error, want a validation failure caught at startup rather than on the first job submission")
			}
		})
	}
}
