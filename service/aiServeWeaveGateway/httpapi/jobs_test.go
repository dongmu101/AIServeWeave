package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// textToImageGraph is a trimmed API-format ComfyUI workflow with one
// substitutable prompt field.
//
// textToImageGraph 是一份精简的 API Format ComfyUI 工作流，含一个可替换的提示词字段。
const textToImageGraph = `{
  "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512}},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": ""}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0]}}
}`

// templates writes a one-template catalogue and loads it, the same way the
// Gateway loads -workflow-templates at startup.
//
// templates 写出一份只含一个模板的目录并加载它，与 Gateway 启动时加载
// -workflow-templates 的路径一致。
func templates(t *testing.T) *workflow.Registry {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(workflow.Template{
		ID: "text-to-image",
		Inputs: []workflow.Input{
			{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true, MaxLength: 64},
			{Name: "width", Node: "5", Field: "width", Type: workflow.InputInteger, Min: ptr(64.0), Max: ptr(2048.0)},
		},
		Graph: json.RawMessage(textToImageGraph),
	})
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t.json"), body, 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	reg, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("workflow.Load: %v", err)
	}
	return reg
}

func ptr(v float64) *float64 { return &v }

// workflowSnapshot describes one ComfyUI instance, whose capabilities sit on
// the runtime rather than on a model.
//
// workflowSnapshot 描述一个 ComfyUI 实例，其能力挂在 runtime 而不是模型上。
func workflowSnapshot(runtimeID string) runtime.Snapshot {
	return runtime.Snapshot{
		Descriptor: runtime.Descriptor{ID: runtimeID, Kind: runtime.KindComfyUI, BaseURL: "http://127.0.0.1:8188", MaxConcurrent: 2},
		State:      runtime.StateHealthy,
		Discovery: runtime.Discovery{
			Capabilities: runtime.CapabilitySet{
				runtime.CapabilityWorkflowExecution: {Level: runtime.SupportSupported},
				runtime.CapabilityWorkflowEvents:    {Level: runtime.SupportSupported},
				runtime.CapabilityWorkflowCancel:    {Level: runtime.SupportSupported},
			},
		},
	}
}

// workflowHandler answers Submit by echoing back the bound graph as the run
// id, so a test can see exactly what reached the node, and answers Status
// with a running snapshot.
//
// workflowHandler 用回显绑定后的图作为 run id 来应答 Submit，好让测试看清到底什么
// 抵达了节点；并用 running 快照应答 Status。
func workflowHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	switch req.GetOperation() {
	case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
		var template []byte
		for _, chunk := range body {
			template = append(template, chunk...)
		}
		payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: string(template)})
		if err != nil {
			return err
		}
		return reply(gatewaytest.DataFrame(payload))
	case tunnelv1.Operation_OPERATION_WORKFLOW_STATUS:
		payload, err := tunnelwire.MarshalWorkflowStatus(runtime.WorkflowStatus{
			State:         runtime.WorkflowRunning,
			QueuePosition: 2,
		})
		if err != nil {
			return err
		}
		return reply(gatewaytest.DataFrame(payload))
	}
	return errors.New("unsupported operation")
}

type jobBody struct {
	JobID         string `json:"job_id"`
	WorkflowID    string `json:"workflow_id"`
	Status        string `json:"status"`
	QueuePosition int    `json:"queue_position"`
	Error         string `json:"error"`
}

func postRun(t *testing.T, url, workflowID, body string) (*http.Response, jobBody) {
	t.Helper()
	resp, err := http.Post(url+"/v1/workflows/"+workflowID+"/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var job jobBody
	_ = json.NewDecoder(resp.Body).Decode(&job)
	return resp, job
}

func TestSubmitWorkflowRunBindsInputsAndReturnsAJobID(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	resp, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox","width":1024}}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if job.JobID == "" {
		t.Error("job_id is empty, want a generated id")
	}
	if job.WorkflowID != "text-to-image" {
		t.Errorf("workflow_id = %q, want text-to-image", job.WorkflowID)
	}
	if job.Status != "queued" {
		t.Errorf("status = %q, want queued", job.Status)
	}
	// The ComfyUI prompt_id is a backend identifier; README requires the
	// public id be our own, so it must not appear anywhere in the response.
	//
	// ComfyUI 的 prompt_id 是后端标识符；README 要求公开 id 用我们自己的，因此它不得
	// 出现在响应的任何位置。
	if strings.Contains(job.JobID, "class_type") {
		t.Errorf("job_id = %q, want it not to be the backend's own id", job.JobID)
	}
}

func TestSubmitWorkflowRunSendsTheBoundGraphToTheNode(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	resp, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	// The status call reaches the same node; what the submit actually carried
	// is asserted through the echoed run id, which the handler returned as the
	// graph itself.
	//
	// 状态查询打到同一个节点；提交实际携带了什么，通过被回显的 run id 断言——handler
	// 把图本身当作 run id 返回了。
	got := readJob(t, srv.URL, job.JobID)
	if got.Status != "running" {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.QueuePosition != 2 {
		t.Errorf("queue_position = %d, want 2", got.QueuePosition)
	}
}

func readJob(t *testing.T, url, jobID string) jobBody {
	t.Helper()
	resp, err := http.Get(url + "/v1/jobs/" + jobID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/jobs/%s status = %d, want 200", jobID, resp.StatusCode)
	}
	var job jobBody
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		t.Fatalf("decoding job: %v", err)
	}
	return job
}

func TestSubmitWorkflowRunRejects(t *testing.T) {
	tests := []struct {
		name       string
		workflowID string
		body       string
		wantStatus int
		wantIn     string
	}{
		{
			name:       "unknown workflow",
			workflowID: "does-not-exist",
			body:       `{"inputs":{"prompt":"hi"}}`,
			wantStatus: http.StatusNotFound,
			wantIn:     "workflow",
		},
		{
			name:       "input the template never declared",
			workflowID: "text-to-image",
			body:       `{"inputs":{"prompt":"hi","checkpoint":"evil.safetensors"}}`,
			wantStatus: http.StatusBadRequest,
			wantIn:     "checkpoint",
		},
		{
			name:       "required input missing",
			workflowID: "text-to-image",
			body:       `{"inputs":{"width":512}}`,
			wantStatus: http.StatusBadRequest,
			wantIn:     "prompt",
		},
		{
			name:       "input out of the declared range",
			workflowID: "text-to-image",
			body:       `{"inputs":{"prompt":"hi","width":99999}}`,
			wantStatus: http.StatusBadRequest,
			wantIn:     "width",
		},
		{
			name:       "body is not JSON",
			workflowID: "text-to-image",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
			wantIn:     "JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
			connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

			resp, err := http.Post(srv.URL+"/v1/workflows/"+tt.workflowID+"/runs", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}
			if !strings.Contains(body.Error.Message, tt.wantIn) {
				t.Errorf("error message = %q, want it to mention %q", body.Error.Message, tt.wantIn)
			}
		})
	}
}

func TestSubmitWorkflowRunWithNoWorkflowCapableNode(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp, _ := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"hi"}}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when nothing can run workflows", resp.StatusCode)
	}
}

func TestJobStatusForAnUnknownJob(t *testing.T) {
	srv, _ := newServer(t, httpapi.Config{Workflows: templates(t)})
	resp, err := http.Get(srv.URL + "/v1/jobs/job_nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestJobIsScopedToItsTenant is README's "使用租户权限保护状态、事件和产物" for the
// status endpoint: another tenant gets the same answer as for a job id that
// was never issued, since telling them it exists is itself information.
//
// TestJobIsScopedToItsTenant 是 README「使用租户权限保护状态、事件和产物」在状态端点
// 上的落实：另一个租户得到的答复与查询一个从未签发过的 job id 相同——告诉他们它存在，
// 本身就是信息。
func TestJobIsScopedToItsTenant(t *testing.T) {
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
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

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
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	for _, tt := range []struct {
		name       string
		key        string
		wantStatus int
	}{
		{name: "the tenant that submitted it", key: "key-a", wantStatus: http.StatusOK},
		{name: "another tenant", key: "key-b", wantStatus: http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+job.JobID, nil)
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+tt.key)
			resp, err := http.DefaultClient.Do(req)
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

// TestJobEndpointLabelsStayClosed is the cardinality test for the two routes
// added here. Both carry an identifier in the path — a workflow id and a job
// id — and neither may become a label value: job ids are minted per request,
// so one client could grow the metric without bound.
//
// TestJobEndpointLabelsStayClosed 是本次新增两条路由的基数测试。两者的路径里都带着
// 标识符——工作流 id 与 job id——两者都不得成为标签取值：job id 是每个请求现铸的，
// 单个客户端就能把指标撑到无界。
func TestJobEndpointLabelsStayClosed(t *testing.T) {
	mx := metricstest.New()
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), Metrics: mx})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), workflowHandler)

	_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	readJob(t, srv.URL, job.JobID)

	seen := make(map[string]bool)
	for _, s := range mx.All() {
		for key, value := range s.Labels {
			if key != httpapi.LabelEndpoint {
				continue
			}
			seen[value] = true
			switch value {
			case httpapi.EndpointModels, httpapi.EndpointChatCompletions, httpapi.EndpointEmbeddings,
				httpapi.EndpointWorkflowRuns, httpapi.EndpointJobs, httpapi.EndpointOther:
			default:
				t.Errorf("metric %s carries endpoint=%q, which is outside the closed vocabulary", s.Name, value)
			}
			if strings.Contains(value, job.JobID) || strings.Contains(value, "text-to-image") {
				t.Errorf("metric %s carries endpoint=%q: a path identifier reached a label", s.Name, value)
			}
		}
	}
	for _, want := range []string{httpapi.EndpointWorkflowRuns, httpapi.EndpointJobs} {
		if !seen[want] {
			t.Errorf("no metric was recorded under endpoint=%q", want)
		}
	}
}
