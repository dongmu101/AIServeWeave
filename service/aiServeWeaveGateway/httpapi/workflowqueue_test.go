package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/jobqueue"
	"AIServeWeave/service/aiServeWeaveGateway/jobqueue/jobqueuetest"
)

// sharedQueue is one cluster-wide queue that several replicas join, the
// in-memory stand-in for one Redis.
type sharedQueue struct {
	store     *jobqueuetest.Store
	transport *jobqueuetest.Transport
}

func newSharedQueue() *sharedQueue {
	return &sharedQueue{store: jobqueuetest.NewStore(), transport: jobqueuetest.NewTransport()}
}

// replica builds one replica's Queue and its transport handle.
func (s *sharedQueue) replica(t *testing.T, id string, maxLength int) (*jobqueue.Queue, *jobqueuetest.Handle) {
	t.Helper()
	handle := s.transport.Replica()
	q, err := jobqueue.New(jobqueue.Config{
		Store: s.store, Transport: handle, Classify: httpapi.ClassifyQueuedSubmit, ReplicaID: id,
		MaxWait: time.Minute, MaxLength: maxLength, RetryInterval: 10 * time.Millisecond,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return q, handle
}

// countingWorkflowHandler is workflowHandler that counts submits.
func countingWorkflowHandler(submits *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if req.GetOperation() == tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT {
			submits.Add(1)
		}
		return workflowHandler(req, body, reply)
	}
}

type queuedJobBody struct {
	JobID         string `json:"job_id"`
	Status        string `json:"status"`
	QueuePosition int    `json:"queue_position"`
	Durability    string `json:"durability"`
	ErrorCode     string `json:"error_code"`
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(out)
	return resp.StatusCode
}

func postJSON(t *testing.T, url, body string, out any) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(out)
	return resp
}

const runBody = `{"inputs":{"prompt":"a red fox"}}`

// TestQueuedRunIsDispatchedByAnotherReplica is the multi-replica path: the
// job is submitted on replica A, dispatched by replica B's worker, and A then
// answers for it from the shared record.
func TestQueuedRunIsDispatchedByAnotherReplica(t *testing.T) {
	shared := newSharedQueue()
	queueA, _ := shared.replica(t, "replica-a", 8)
	queueB, handleB := shared.replica(t, "replica-b", 8)
	srvA, hA := newServer(t, httpapi.Config{Workflows: templates(t), WorkflowQueue: queueA})
	srvB, hB := newServer(t, httpapi.Config{Workflows: templates(t), WorkflowQueue: queueB})
	var submitsA, submitsB atomic.Int32
	connectNode(t, hA, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), countingWorkflowHandler(&submitsA))
	connectNode(t, hB, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), countingWorkflowHandler(&submitsB))

	var queued queuedJobBody
	resp := postJSON(t, srvA.URL+"/v1/workflows/text-to-image/runs", runBody, &queued)
	if resp.StatusCode != http.StatusAccepted || queued.Status != "queued" || queued.Durability != httpapi.DurabilitySharedQueue || queued.QueuePosition != 1 {
		t.Fatalf("submit = %d %+v, want 202 queued in the shared queue at position 1", resp.StatusCode, queued)
	}
	if submitsA.Load()+submitsB.Load() != 0 {
		t.Fatal("a queued submission reached a node before any worker took it")
	}

	// Replica B, which never saw the request, answers from the shared record.
	var seen queuedJobBody
	if code := getJSON(t, srvB.URL+"/v1/jobs/"+queued.JobID, &seen); code != http.StatusOK || seen.Status != "queued" || seen.QueuePosition != 1 {
		t.Fatalf("GET on replica B while queued = %d %+v", code, seen)
	}

	if err := shared.transport.Deliver(context.Background(), handleB, queued.JobID); err != nil {
		t.Fatalf("deliver on replica B: %v", err)
	}
	if submitsB.Load() != 1 || submitsA.Load() != 0 {
		t.Fatalf("submits A=%d B=%d, want the one worker that took it to submit once", submitsA.Load(), submitsB.Load())
	}

	// Replica A restores the binding and asks the node through its own tunnel.
	got := readJob(t, srvA.URL, queued.JobID)
	if got.Status != "running" {
		t.Fatalf("GET on replica A after dispatch = %+v, want running", got)
	}
}

func TestQueuedRunCanBeCancelledBeforeDispatch(t *testing.T) {
	shared := newSharedQueue()
	queue, _ := shared.replica(t, "replica-a", 8)
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), WorkflowQueue: queue})
	var submits atomic.Int32
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), countingWorkflowHandler(&submits))

	var queued queuedJobBody
	postJSON(t, srv.URL+"/v1/workflows/text-to-image/runs", runBody, &queued)

	var cancelled queuedJobBody
	resp := postJSON(t, srv.URL+"/v1/jobs/"+queued.JobID+"/cancel", "", &cancelled)
	if resp.StatusCode != http.StatusAccepted || cancelled.Status != "cancelled" || cancelled.ErrorCode != jobqueue.CodeCancelled {
		t.Fatalf("cancel = %d %+v, want 202 cancelled", resp.StatusCode, cancelled)
	}
	if len(shared.transport.Pending()) != 0 {
		t.Fatal("the cancelled task is still pending")
	}

	var after queuedJobBody
	if code := getJSON(t, srv.URL+"/v1/jobs/"+queued.JobID, &after); code != http.StatusOK || after.Status != "cancelled" {
		t.Fatalf("GET after cancel = %d %+v", code, after)
	}
	var artifacts struct {
		Data []any `json:"data"`
	}
	if code := getJSON(t, srv.URL+"/v1/jobs/"+queued.JobID+"/artifacts", &artifacts); code != http.StatusOK || len(artifacts.Data) != 0 {
		t.Fatalf("artifacts of a never-dispatched job = %d %+v, want an empty list", code, artifacts)
	}
	if resp := postJSON(t, srv.URL+"/v1/jobs/"+queued.JobID+"/cancel", "", &struct{}{}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second cancel = %d, want 409 like any finished job", resp.StatusCode)
	}
	if submits.Load() != 0 {
		t.Fatal("a cancelled job reached the node")
	}
}

func TestFullQueueAnswers429(t *testing.T) {
	shared := newSharedQueue()
	queue, _ := shared.replica(t, "replica-a", 1)
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), WorkflowQueue: queue})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	postJSON(t, srv.URL+"/v1/workflows/text-to-image/runs", runBody, &queuedJobBody{})
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	resp := postJSON(t, srv.URL+"/v1/workflows/text-to-image/runs", runBody, &body)
	if resp.StatusCode != http.StatusTooManyRequests || body.Error.Code != "queue_full" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("submit beyond the length = %d %+v retry-after=%q", resp.StatusCode, body, resp.Header.Get("Retry-After"))
	}
}

func TestQueueUnavailableFallsBackToDirectDispatch(t *testing.T) {
	shared := newSharedQueue()
	queue, _ := shared.replica(t, "replica-a", 8)
	shared.store.SetErr(errors.New("redis down"))
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), WorkflowQueue: queue})
	var submits atomic.Int32
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), countingWorkflowHandler(&submits))

	var job queuedJobBody
	resp := postJSON(t, srv.URL+"/v1/workflows/text-to-image/runs", runBody, &job)
	if resp.StatusCode != http.StatusAccepted || job.Durability != httpapi.DurabilityMemoryOnly || submits.Load() != 1 {
		t.Fatalf("submit with Redis down = %d %+v submits=%d, want a direct dispatch", resp.StatusCode, job, submits.Load())
	}
}

func TestQueuedJobIsScopedToItsTenant(t *testing.T) {
	verifier := &fakeVerifier{answer: func(key string) (httpapi.Identity, error) {
		switch key {
		case "key-a":
			return httpapi.Identity{TenantID: "tnt_a", KeyID: "k_a"}, nil
		case "key-b":
			return httpapi.Identity{TenantID: "tnt_b", KeyID: "k_b"}, nil
		}
		return httpapi.Identity{}, httpapi.ErrKeyRejected
	}}
	shared := newSharedQueue()
	queue, _ := shared.replica(t, "replica-a", 8)
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), Verifier: verifier, WorkflowQueue: queue})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	do := func(method, path, key, body string) (*http.Response, queuedJobBody) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out queuedJobBody
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	_, queued := do(http.MethodPost, "/v1/workflows/text-to-image/runs", "key-a", runBody)
	if resp, _ := do(http.MethodGet, "/v1/jobs/"+queued.JobID, "key-b", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another tenant reading a queued job = %d, want 404", resp.StatusCode)
	}
	if resp, _ := do(http.MethodPost, "/v1/jobs/"+queued.JobID+"/cancel", "key-b", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another tenant cancelling a queued job = %d, want 404", resp.StatusCode)
	}
	if resp, got := do(http.MethodGet, "/v1/jobs/"+queued.JobID, "key-a", ""); resp.StatusCode != http.StatusOK || got.Status != "queued" {
		t.Fatalf("owner reading the queued job = %d %+v", resp.StatusCode, got)
	}
}

// TestQueuedJobEventsReportThePositionThenTheOutcome subscribes while the job
// waits: the stream starts with its queue position and ends with the
// terminal frame once it is cancelled.
func TestQueuedJobEventsReportThePositionThenTheOutcome(t *testing.T) {
	shared := newSharedQueue()
	queue, _ := shared.replica(t, "replica-a", 8)
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), WorkflowQueue: queue})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	var queued queuedJobBody
	postJSON(t, srv.URL+"/v1/workflows/text-to-image/runs", runBody, &queued)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/jobs/"+queued.JobID+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	next := func() string {
		for lines.Scan() {
			if name, ok := strings.CutPrefix(lines.Text(), "event: "); ok {
				return name
			}
		}
		return ""
	}
	if name := next(); name != "queue_changed" {
		t.Fatalf("first event = %q, want queue_changed", name)
	}
	postJSON(t, srv.URL+"/v1/jobs/"+queued.JobID+"/cancel", "", &struct{}{})
	if name := next(); name != "cancelled" {
		t.Fatalf("event after cancel = %q, want cancelled", name)
	}
}

// TestQueuedImageGenerationTimesOutWhileStillQueued: no worker ever takes the
// task, so the request's own budget runs out while it waits; the generation
// is withdrawn and the caller gets a retryable 429.
func TestQueuedImageGenerationTimesOutWhileStillQueued(t *testing.T) {
	shared := newSharedQueue()
	queue, _ := shared.replica(t, "replica-a", 8)
	srv, h := newServer(t, httpapi.Config{Workflows: imagesTemplate(t), ImagesWorkflowID: "text-to-image",
		ImagesGenerationTimeout: 300 * time.Millisecond, WorkflowQueue: queue})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	resp := postJSON(t, srv.URL+"/v1/images/generations", `{"prompt":"a red fox"}`, &body)
	if resp.StatusCode != http.StatusTooManyRequests || body.Error.Code != jobqueue.CodeQueueTimeout {
		t.Fatalf("images while nothing dispatches = %d %+v, want 429 queue_timeout", resp.StatusCode, body)
	}
	pending := shared.transport.Pending()
	if len(pending) != 0 {
		t.Fatalf("the timed-out generation is still pending: %v", pending)
	}
}
