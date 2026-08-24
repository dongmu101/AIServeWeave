package httpapi_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

const artifactBytes = "PNG-BYTES-PRETENDING-TO-BE-AN-IMAGE"

// artifactWorkflowHandler answers Submit, the terminal event stream, artifact
// listing and artifact download. filename is what the listed artifact is
// called, which is how the header-injection case is driven.
//
// artifactWorkflowHandler 应答 Submit、终态事件流、产物列举与产物下载。filename 是
// 被列出的那个产物的名字，响应头注入那个用例正是这样驱动的。
func artifactWorkflowHandler(filename string, listErr *gatewaytest.WireError) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		switch req.GetOperation() {
		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
			payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "prompt-1"})
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
		case tunnelv1.Operation_OPERATION_ARTIFACT_LIST:
			if listErr != nil {
				return listErr
			}
			payload, err := tunnelwire.MarshalArtifactList([]runtime.ArtifactRef{
				{RunID: "prompt-1", Filename: filename, Type: "output"},
			})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_ARTIFACT_OPEN:
			if err := reply(gatewaytest.HeaderFrame("image/png", int64(len(artifactBytes)))); err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame([]byte(artifactBytes)))
		}
		return errors.New("unsupported operation")
	}
}

// connectArtifactNode parks one inference slot and one bulk slot. Both are
// needed: listing is a bounded reply on an inference slot, while opening an
// artifact streams its body on a bulk slot — the tunnel keeps the two classes
// physically isolated so a large download cannot displace inference. A node
// with only inference slots answers a download with backpressure, which is
// the correct behavior and not what these tests are about.
//
// connectArtifactNode 各 park 一个推理槽与一个批量槽。两者都需要：列举是走推理槽的
// 有界回复，而打开产物要在批量槽上流出它的响应体——隧道让这两类槽物理隔离，好让一次
// 大的下载无法挤占推理。只有推理槽的节点会用背压应答下载，那是正确行为，但不是这些
// 测试要讲的事。
func connectArtifactNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID string, handle gatewaytest.SlotHandler) {
	t.Helper()
	connectNode(t, h, nodeID, runtimeID, workflowSnapshot(runtimeID), handle)
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_BULK, nodeID+"-bulk-1", handle)
	gatewaytest.WaitFor(t, "the bulk slot to park on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_BULK] == 1
	})
}

type artifactsBody struct {
	Data []struct {
		ArtifactID  string `json:"artifact_id"`
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
	} `json:"data"`
}

// finishedJob submits a job and drives it terminal through the event stream,
// which is the state artifacts can be listed in.
//
// finishedJob 提交一个 job 并经事件流把它推到终态，那正是可以列举产物的状态。
func finishedJob(t *testing.T, url string) string {
	t.Helper()
	_, job := postRun(t, url, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	openEvents(t, url, job.JobID)
	return job.JobID
}

func listArtifacts(t *testing.T, url, jobID string) (*http.Response, artifactsBody) {
	t.Helper()
	resp, err := http.Get(url + "/v1/jobs/" + jobID + "/artifacts")
	if err != nil {
		t.Fatalf("GET artifacts: %v", err)
	}
	defer resp.Body.Close()
	var body artifactsBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

func TestListArtifactsMintsIDsAndHidesBackendPaths(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectArtifactNode(t, h, "node-comfy", "comfy-1", artifactWorkflowHandler("ComfyUI_00001_.png", nil))

	jobID := finishedJob(t, srv.URL)
	resp, body := listArtifacts(t, srv.URL, jobID)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(body.Data) != 1 {
		t.Fatalf("artifacts = %d, want 1: %+v", len(body.Data), body.Data)
	}
	if body.Data[0].ArtifactID == "" {
		t.Error("artifact_id is empty, want a minted id")
	}
	// The backend addresses an artifact by filename, subfolder and type. That
	// triple is a path into someone else's disk layout, and the public id
	// must not be built out of it — otherwise a caller could forge one.
	//
	// 后端用 filename、subfolder 与 type 三元组定位产物。那个三元组是通往别人磁盘布局
	// 的一条路径，公开 id 不得由它构造——否则调用方可以伪造一个出来。
	if strings.Contains(body.Data[0].ArtifactID, "ComfyUI_00001_") {
		t.Errorf("artifact_id = %q, want an id that does not embed the backend's path", body.Data[0].ArtifactID)
	}
}

// TestListArtifactsIsStable keeps a poll loop from minting a fresh id set on
// every call, which would grow the store without bound and invalidate ids a
// caller is still holding.
//
// TestListArtifactsIsStable 防止轮询在每次调用时都铸出一套新 id——那会让存储无界增长，
// 也会让调用方手上还攥着的 id 失效。
func TestListArtifactsIsStable(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectArtifactNode(t, h, "node-comfy", "comfy-1", artifactWorkflowHandler("ComfyUI_00001_.png", nil))

	jobID := finishedJob(t, srv.URL)
	_, first := listArtifacts(t, srv.URL, jobID)
	_, second := listArtifacts(t, srv.URL, jobID)

	if len(first.Data) != 1 || len(second.Data) != 1 {
		t.Fatalf("listings = %d and %d, want 1 each", len(first.Data), len(second.Data))
	}
	if first.Data[0].ArtifactID != second.Data[0].ArtifactID {
		t.Errorf("artifact_id changed between listings: %q then %q", first.Data[0].ArtifactID, second.Data[0].ArtifactID)
	}
}

func TestDownloadArtifactStreamsTheBody(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectArtifactNode(t, h, "node-comfy", "comfy-1", artifactWorkflowHandler("ComfyUI_00001_.png", nil))

	jobID := finishedJob(t, srv.URL)
	_, listing := listArtifacts(t, srv.URL, jobID)

	resp, err := http.Get(srv.URL + "/v1/artifacts/" + listing.Data[0].ArtifactID)
	if err != nil {
		t.Fatalf("GET artifact: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(got) != artifactBytes {
		t.Errorf("body = %q, want %q", got, artifactBytes)
	}
}

// TestDownloadArtifactSanitizesTheFilename covers a filename that came from
// the backend and, through the workflow's own save-node prefix, ultimately
// from the caller. Header injection is what a raw echo would allow.
//
// TestDownloadArtifactSanitizesTheFilename 覆盖一个来自后端、并且经由工作流自己的
// 保存节点前缀最终来自调用方的文件名。原样回显所允许的正是响应头注入。
func TestDownloadArtifactSanitizesTheFilename(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectArtifactNode(t, h, "node-comfy", "comfy-1",
		artifactWorkflowHandler("evil\r\nX-Injected: yes\r\n\"quoted\".png", nil))

	jobID := finishedJob(t, srv.URL)
	_, listing := listArtifacts(t, srv.URL, jobID)

	resp, err := http.Get(srv.URL + "/v1/artifacts/" + listing.Data[0].ArtifactID)
	if err != nil {
		t.Fatalf("GET artifact: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Injected"); got != "" {
		t.Errorf("X-Injected = %q: a backend filename injected a response header", got)
	}
	disposition := resp.Header.Get("Content-Disposition")
	if strings.ContainsAny(disposition, "\r\n") {
		t.Errorf("Content-Disposition = %q, want no CR or LF", disposition)
	}
	if strings.Count(disposition, `"`) > 2 {
		t.Errorf("Content-Disposition = %q, want the filename's own quotes removed", disposition)
	}
}

func TestArtifactRejects(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "unknown job", path: "/v1/jobs/job_nope/artifacts", wantStatus: http.StatusNotFound},
		{name: "unknown artifact", path: "/v1/artifacts/art_nope", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
			connectArtifactNode(t, h, "node-comfy", "comfy-1", artifactWorkflowHandler("a.png", nil))

			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

// TestArtifactsAreScopedToTheirTenant is the disclosure that matters most in
// this whole surface: an artifact is the generated image itself, not metadata
// about it.
//
// TestArtifactsAreScopedToTheirTenant 是整个界面里最要紧的那一处泄露：产物就是生成
// 出来的图像本身，而不是关于它的元数据。
func TestArtifactsAreScopedToTheirTenant(t *testing.T) {
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
	connectArtifactNode(t, h, "node-comfy", "comfy-1", artifactWorkflowHandler("ComfyUI_00001_.png", nil))

	do := func(t *testing.T, method, path, key string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"inputs":{"prompt":"a red fox"}}`))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	resp := do(t, http.MethodPost, "/v1/workflows/text-to-image/runs", "key-a")
	var job jobBody
	_ = json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()

	// Drive it terminal as tenant A, then list as tenant A to mint the ids.
	//
	// 以租户 A 把它推到终态，再以租户 A 列举以铸出 id。
	events := do(t, http.MethodGet, "/v1/jobs/"+job.JobID+"/events", "key-a")
	_, _ = io.ReadAll(events.Body)
	events.Body.Close()

	listing := do(t, http.MethodGet, "/v1/jobs/"+job.JobID+"/artifacts", "key-a")
	var body artifactsBody
	_ = json.NewDecoder(listing.Body).Decode(&body)
	listing.Body.Close()
	if len(body.Data) != 1 {
		t.Fatalf("tenant A listed %d artifacts, want 1", len(body.Data))
	}

	for _, tt := range []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "listing another tenant's job", path: "/v1/jobs/" + job.JobID + "/artifacts", wantStatus: http.StatusNotFound},
		{name: "downloading another tenant's artifact", path: "/v1/artifacts/" + body.Data[0].ArtifactID, wantStatus: http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			other := do(t, http.MethodGet, tt.path, "key-b")
			defer other.Body.Close()
			if other.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", other.StatusCode, tt.wantStatus)
			}
		})
	}
}
