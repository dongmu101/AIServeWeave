package httpapi_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// pngSignature is a real PNG file's 8-byte magic header. submitRun now
// sniffs an uploaded file's actual bytes against its claimed extension
// (uploadformat.go), so any test part named ".png" needs to start with this
// or the request is rejected as a format mismatch before it reaches the
// scheduler.
//
// pngSignature 是真实 PNG 文件的 8 字节魔数。submitRun 现在会把一次上传
// 文件的真实字节与其声称的扩展名做嗅探比对（uploadformat.go），因此任何
// 命名为 ".png" 的测试分片都需要以它开头，否则请求会在到达调度器之前就被
// 判定为格式不匹配而拒绝。
const pngSignature = "\x89PNG\r\n\x1a\n"

// textToImageWithFileGraph adds a LoadImage node to textToImageGraph's shape,
// for templates whose declared inputs include a file (STATUS.md's P04).
//
// textToImageWithFileGraph 在 textToImageGraph 的形状上加了一个 LoadImage
// 节点，供声明了文件输入（STATUS.md 的 P04）的模板使用。
const textToImageWithFileGraph = `{
  "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512}},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": ""}},
  "7": {"class_type": "LoadImage", "inputs": {"image": "placeholder.png"}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0]}}
}`

// templatesWithFileInput is templates' counterpart for a template that also
// declares a file input, kept separate so the many existing tests built on
// templates(t) are not affected by adding one.
//
// templatesWithFileInput 是 templates 的对应版本，模板额外声明了一个文件
// 输入，特意分开是为了不影响建立在 templates(t) 之上的众多既有测试。
func templatesWithFileInput(t *testing.T) *workflow.Handle {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(workflow.Template{
		ID: "text-to-image-with-file",
		Inputs: []workflow.Input{
			{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true, MaxLength: 64},
			{Name: "image", Node: "7", Field: "image", Type: workflow.InputFile, Required: true},
		},
		Graph: json.RawMessage(textToImageWithFileGraph),
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
	return workflow.NewHandle(reg)
}

// capturedSubmit records the exact bytes one WORKFLOW_SUBMIT's template
// arrived with, so a test can inspect what actually reached the node — not
// just that the HTTP response was a 202 — without threading it back out
// through the public job JSON, which never exposes a graph.
//
// capturedSubmit 记录一次 WORKFLOW_SUBMIT 抵达时模板的确切字节，好让测试能
// 检查真正到达节点的是什么——而不只是 HTTP 响应是不是 202——且无需把它经由
// 从不暴露图的公开 job JSON 传回来。
type capturedSubmit struct {
	mu       sync.Mutex
	template string
}

func (c *capturedSubmit) set(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.template = s
}

func (c *capturedSubmit) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.template
}

// uploadEchoingWorkflowHandler answers WORKFLOW_SUBMIT (capturing the
// template into captured) and OPERATION_INPUT_UPLOAD (echoing back a
// deterministic InputRef derived from the uploaded filename and bytes, so a
// test can confirm both arrived intact and that SetGraphField wove the
// result into the template SUBMIT later received).
//
// uploadEchoingWorkflowHandler 应答 WORKFLOW_SUBMIT（把模板捕获进
// captured）与 OPERATION_INPUT_UPLOAD（回显一个由上传的文件名与字节确定性
// 派生出的 InputRef，好让测试确认两者都完整送达，且 SetGraphField 把结果
// 织进了之后 SUBMIT 收到的模板里）。
func uploadEchoingWorkflowHandler(captured *capturedSubmit) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		switch req.GetOperation() {
		case tunnelv1.Operation_OPERATION_INPUT_UPLOAD:
			meta, err := tunnelwire.UnmarshalInputUploadRequest(req.GetPayload())
			if err != nil {
				return err
			}
			var content []byte
			for _, chunk := range body {
				content = append(content, chunk...)
			}
			// hex-encoded: InputRef is a proto3 string field, which must be
			// valid UTF-8, and the uploaded bytes here can be arbitrary
			// binary (e.g. a real PNG signature) — a real backend never hits
			// this, since ComfyUI's own upload response always names a
			// filename, never echoes raw bytes.
			//
			// 十六进制编码：InputRef 是一个 proto3 string 字段，必须是合法
			// UTF-8，而这里上传的字节可以是任意二进制（比如一个真实的 PNG
			// 签名）——真实后端不会遇到这个问题，因为 ComfyUI 自己的上传
			// 响应给出的始终是文件名，从不回显原始字节。
			payload, err := tunnelwire.MarshalInputUploadResult(runtime.InputUploadResult{
				InputRef: "uploaded/" + meta.Filename + "/" + hex.EncodeToString(content),
			})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))

		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
			var template []byte
			for _, chunk := range body {
				template = append(template, chunk...)
			}
			captured.set(string(template))
			payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "run-1"})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		}
		return errors.New("unsupported operation")
	}
}

// postRunMultipart submits a workflow run the way a caller with a file input
// must: multipart/form-data with an "inputs" field carrying the same JSON
// body postRun's callers pass, plus one file part per entry in files.
//
// postRunMultipart 以携带文件输入的调用方必须使用的方式提交一次运行：
// multipart/form-data，一个 "inputs" 字段携带与 postRun 调用方相同的 JSON
// 请求体，外加 files 里每一项对应的一个文件分片。
func postRunMultipart(t *testing.T, url, workflowID, inputsJSON string, files map[string]string) (*http.Response, jobBody) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("inputs", inputsJSON); err != nil {
		t.Fatalf("WriteField(inputs): %v", err)
	}
	for name, content := range files {
		part, err := mw.CreateFormFile(name, name+".png")
		if err != nil {
			t.Fatalf("CreateFormFile(%q): %v", name, err)
		}
		// Every part here is named ".png" above, and submitRun now sniffs a
		// file's actual bytes against its claimed extension (uploadformat.go)
		// — so the part needs a real PNG signature, not just an arbitrary
		// payload, or the request is rejected before it ever reaches the
		// fake node this test is exercising.
		//
		// 这里的每个分片都在上面被命名为 ".png"，而 submitRun 现在会把一个
		// 文件的真实字节与其声称的扩展名做嗅探比对（uploadformat.go）——所以
		// 这个分片需要一个真实的 PNG 文件签名，而不能只是任意负载，否则请求
		// 在到达本测试要演练的假节点之前就会被拒绝。
		if _, err := part.Write(append([]byte(pngSignature), content...)); err != nil {
			t.Fatalf("write file part %q: %v", name, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	resp, err := http.Post(url+"/v1/workflows/"+workflowID+"/runs", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var job jobBody
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &job)
	return resp, job
}

// connectUploadCapableNode is connectNode plus a parked bulk slot: an input
// upload travels OPERATION_INPUT_UPLOAD, which — like ARTIFACT_OPEN — is
// routed to the bulk class (tunnelserver.classFor), so a node with only an
// inference slot answers it with backpressure instead of ever reaching
// handle.
//
// connectUploadCapableNode 是 connectNode 外加一个已停放的批量槽：一次输入
// 上传走 OPERATION_INPUT_UPLOAD，与 ARTIFACT_OPEN 一样被路由到批量类别
// （tunnelserver.classFor），因此只有推理槽的节点会用背压应答它，而根本
// 不会走到 handle。
func connectUploadCapableNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID string, handle gatewaytest.SlotHandler) {
	t.Helper()
	connectNode(t, h, nodeID, runtimeID, workflowSnapshot(runtimeID), handle)
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_BULK, nodeID+"-bulk-1", handle)
	gatewaytest.WaitFor(t, "the bulk slot to park on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_BULK] == 1
	})
}

func TestSubmitWorkflowRunWithAFileInputUploadsAndWeavesTheResultIntoTheGraph(t *testing.T) {
	captured := &capturedSubmit{}
	srv, h := newServer(t, httpapi.Config{Workflows: templatesWithFileInput(t)})
	connectUploadCapableNode(t, h, "node-comfy", "comfy-1", uploadEchoingWorkflowHandler(captured))

	resp, job := postRunMultipart(t, srv.URL, "text-to-image-with-file",
		`{"inputs":{"prompt":"a red fox"}}`, map[string]string{"image": "cat-bytes"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; job = %+v", resp.StatusCode, job)
	}
	if job.JobID == "" {
		t.Error("job_id is empty, want a generated id")
	}

	gatewaytest.WaitFor(t, "the workflow submit to reach the node", func() bool {
		return captured.get() != ""
	})

	template := captured.get()
	if !strings.Contains(template, "a red fox") {
		t.Errorf("submitted template = %s, want it to contain the bound prompt", template)
	}
	// The InputRef echoes back "uploaded/" + filename + "/" + the hex of the
	// exact bytes this Gateway forwarded, pngSignature included —
	// validateUploadContent (uploadformat.go) only sniffs the file's bytes,
	// it never strips or alters them on its way to the node.
	//
	// InputRef 回显的是 "uploaded/" + 文件名 + "/" + 本 Gateway 转发出的原样
	// 字节（含 pngSignature）的十六进制——validateUploadContent
	// （uploadformat.go）只嗅探文件字节，从不在转发给节点的路上剥离或改动
	// 它们。
	if want := "uploaded/image.png/" + hex.EncodeToString([]byte(pngSignature+"cat-bytes")); !strings.Contains(template, want) {
		t.Errorf("submitted template = %s, want it to contain %q (the uploaded InputRef)", template, want)
	}
	if strings.Contains(template, "placeholder.png") {
		t.Errorf("submitted template = %s, still has the template's own placeholder — SetGraphField did not run", template)
	}
}

func TestSubmitWorkflowRunRequiresTheDeclaredFileInput(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templatesWithFileInput(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), uploadEchoingWorkflowHandler(&capturedSubmit{}))

	// No file part at all: plain JSON, matching the pre-P04 request shape.
	//
	// 完全没有文件分片：纯 JSON，与 P04 之前的请求形态一致。
	resp, job := postRun(t, srv.URL, "text-to-image-with-file", `{"inputs":{"prompt":"a red fox"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; job = %+v", resp.StatusCode, job)
	}
}

// postRunMultipartWithFile is postRunMultipart's lower-level counterpart: it
// lets a test pick the uploaded part's filename and raw bytes directly,
// rather than always naming the part "<name>.png" and prefixing it with
// pngSignature. The format-validation tests below need that control to
// exercise the extension check and the content-sniff check independently.
//
// postRunMultipartWithFile 是 postRunMultipart 更底层的对应版本：它让测试
// 直接选择所上传分片的文件名与原始字节，而不总是把分片命名为
// "<name>.png" 并加上 pngSignature 前缀。下面的格式校验测试需要这种控制，
// 才能分别单独演练扩展名检查与内容嗅探检查。
func postRunMultipartWithFile(t *testing.T, url, workflowID, inputsJSON, partName, filename string, content []byte) (*http.Response, jobBody) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("inputs", inputsJSON); err != nil {
		t.Fatalf("WriteField(inputs): %v", err)
	}
	part, err := mw.CreateFormFile(partName, filename)
	if err != nil {
		t.Fatalf("CreateFormFile(%q, %q): %v", partName, filename, err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file part %q: %v", partName, err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	resp, err := http.Post(url+"/v1/workflows/"+workflowID+"/runs", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var job jobBody
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &job)
	return resp, job
}

func TestSubmitWorkflowRunRejectsAFileWithADisallowedExtension(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templatesWithFileInput(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), uploadEchoingWorkflowHandler(&capturedSubmit{}))

	// A caller trying to pass an .exe through an InputFile input: rejected on
	// the filename alone, before a single byte of it is read.
	//
	// 调用方试图把一个 .exe 当作 InputFile 输入传进来：单凭文件名就会被拒绝，
	// 甚至读不到它的一个字节。
	resp, job := postRunMultipartWithFile(t, srv.URL, "text-to-image-with-file",
		`{"inputs":{"prompt":"a red fox"}}`, "image", "payload.exe", []byte("MZ-not-really-a-pe"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; job = %+v", resp.StatusCode, job)
	}
}

func TestSubmitWorkflowRunRejectsAFileWhoseContentDoesNotMatchItsExtension(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templatesWithFileInput(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), uploadEchoingWorkflowHandler(&capturedSubmit{}))

	// The extension is allowed, but the bytes are plain text, not a PNG — the
	// content-sniff check (uploadformat.go's validateUploadContent) catches
	// what the filename check alone would have let through.
	//
	// 扩展名是被允许的，但字节是纯文本，不是 PNG——内容嗅探检查
	// （uploadformat.go 的 validateUploadContent）拦下了单凭文件名检查会放行
	// 的东西。
	resp, job := postRunMultipartWithFile(t, srv.URL, "text-to-image-with-file",
		`{"inputs":{"prompt":"a red fox"}}`, "image", "image.png", []byte("just some plain text, not a real png"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; job = %+v", resp.StatusCode, job)
	}
}

func TestSubmitWorkflowRunAcceptsAFileWithNoSniffCategoryOnExtensionAlone(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{
		Workflows:               templatesWithFileInput(t),
		AllowedUploadExtensions: []string{".bin"},
	})
	connectUploadCapableNode(t, h, "node-comfy", "comfy-1", uploadEchoingWorkflowHandler(&capturedSubmit{}))

	// ".bin" is allowed by this deployment's own Config.AllowedUploadExtensions
	// but has no entry in uploadSniffCategoryByExtension's fixed internal
	// table, so arbitrary bytes pass once the extension check alone clears
	// them.
	//
	// ".bin" 被这个部署自己的 Config.AllowedUploadExtensions 允许，但在
	// uploadSniffCategoryByExtension 那张固定的内部表里没有表项，因此只要
	// 扩展名检查通过，任意字节都能过关。
	resp, job := postRunMultipartWithFile(t, srv.URL, "text-to-image-with-file",
		`{"inputs":{"prompt":"a red fox"}}`, "image", "weights.bin", []byte("arbitrary opaque bytes"))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; job = %+v", resp.StatusCode, job)
	}
}
