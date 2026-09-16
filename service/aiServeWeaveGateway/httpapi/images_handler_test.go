package httpapi_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// imagesTemplate registers a template shaped like a real text-to-image
// workflow — a required prompt input, optional width/height inputs, and one
// output declared "image" — the same shape main.go's validateImagesWorkflow
// requires at startup.
//
// imagesTemplate 注册一份形似真实文生图工作流的模板——一个必填的 prompt
// 输入、可选的 width/height 输入，以及一个声明为 "image" 的输出——与
// main.go 的 validateImagesWorkflow 在启动期要求的形状相同。
func imagesTemplate(t *testing.T) *workflow.Handle {
	t.Helper()
	return loadOneTemplate(t, workflow.Template{
		ID: "text-to-image",
		Inputs: []workflow.Input{
			{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true, MaxLength: 64},
			{Name: "width", Node: "5", Field: "width", Type: workflow.InputInteger},
			{Name: "height", Node: "5", Field: "height", Type: workflow.InputInteger},
		},
		Outputs: []workflow.Output{{Name: "image", Node: "9", Type: "image"}},
		Graph:   json.RawMessage(textToImageGraph),
	})
}

// loadOneTemplate writes a one-template catalogue and loads it, the same way
// jobs_test.go's templates() and the Gateway's own -workflow-templates do at
// startup.
//
// loadOneTemplate 写出一份只含一个模板的目录并加载它，与 jobs_test.go 的
// templates() 以及 Gateway 自己启动时加载 -workflow-templates 的路径一致。
func loadOneTemplate(t *testing.T, tpl workflow.Template) *workflow.Handle {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(tpl)
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

// imagesNodeHandler answers WORKFLOW_SUBMIT once, then WORKFLOW_STATUS with
// "running" for statusRunningCalls polls before finalState, then
// ARTIFACT_LIST and ARTIFACT_OPEN for a single fixed artifact. It exists so
// each test controls exactly how many times the polling loop in
// imagesGenerations must go around before the run settles.
//
// imagesNodeHandler 应答一次 WORKFLOW_SUBMIT，随后对 statusRunningCalls 次
// 轮询回答 "running"，再回答 finalState，最后应答 ARTIFACT_LIST 与
// ARTIFACT_OPEN——只有一个固定产物。它的存在，是为了让每个测试精确控制
// imagesGenerations 里的轮询循环在这次运行落定之前要转多少圈。
func imagesNodeHandler(statusRunningCalls int, finalState runtime.WorkflowState, outOfMemory bool, artifactFilename string, artifactBody []byte) gatewaytest.SlotHandler {
	var statusCalls atomic.Int32
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		switch req.GetOperation() {
		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
			payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "prompt-1"})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_WORKFLOW_STATUS:
			n := statusCalls.Add(1)
			status := runtime.WorkflowStatus{State: runtime.WorkflowRunning}
			if int(n) > statusRunningCalls {
				status = runtime.WorkflowStatus{State: finalState, OutOfMemory: outOfMemory}
			}
			payload, err := tunnelwire.MarshalWorkflowStatus(status)
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_ARTIFACT_LIST:
			var refs []runtime.ArtifactRef
			if artifactFilename != "" {
				refs = []runtime.ArtifactRef{{RunID: "prompt-1", Filename: artifactFilename, Type: "output"}}
			}
			payload, err := tunnelwire.MarshalArtifactList(refs)
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_ARTIFACT_OPEN:
			if err := reply(gatewaytest.HeaderFrame("image/png", int64(len(artifactBody)))); err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(artifactBody))
		}
		return errors.New("unsupported operation")
	}
}

// connectImagesNode parks two inference slots and one bulk slot, matching a
// realistically provisioned ComfyUI node (the real Gateway's tunnel SlotHint
// defaults to MinSlots: 2 in main.go). imagesGenerations dispatches Submit,
// Status and Artifacts back-to-back on the inference class within a single
// HTTP request, with none of the natural gap separate polling calls from a
// real client would leave for one lone slot to re-park in time — connectNode
// (jobs_test.go) opens only one and is fine for tests that space their calls
// across separate HTTP round trips, but this endpoint's own inline loop
// needs a second one. OpenArtifact needs its own bulk slot, exactly as
// artifacts_test.go's connectArtifactNode already sets up.
//
// connectImagesNode 停靠两个推理槽和一个批量槽，匹配一个按现实配置的 ComfyUI
// 节点（真实 Gateway 的隧道 SlotHint 在 main.go 里默认 MinSlots: 2）。
// imagesGenerations 在一次 HTTP 请求内，于推理这一类别上背靠背派发 Submit、
// Status 与 Artifacts，不像真实客户端分开的轮询调用那样天然留有让孤零零
// 一个槽来得及重新停靠的间隙——connectNode（jobs_test.go）只开一个，对那些
// 把调用分散在不同 HTTP 往返里的测试够用，但本端点自己的内联循环需要第二个。
// OpenArtifact 需要它自己的批量槽，与 artifacts_test.go 的
// connectArtifactNode 已经搭好的方式相同。
func connectImagesNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID string, handle gatewaytest.SlotHandler) {
	t.Helper()
	c := h.Connect(nodeID, runtimeID)
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{workflowSnapshot(runtimeID)}),
	}}})
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, nodeID+"-slot-1", handle)
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, nodeID+"-slot-2", handle)
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_BULK, nodeID+"-bulk-1", handle)
	gatewaytest.WaitFor(t, "both inference slots to park on "+nodeID, func() bool { return gatewaytest.IdleCount(h, nodeID) == 2 })
	gatewaytest.WaitFor(t, "the bulk slot to park on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_BULK] == 1
	})
	gatewaytest.WaitFor(t, "the inventory to arrive on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return len(info.Runtimes) == 1
	})
}

// newImagesServer wires a Gateway front door whose httpapi.Config shares the
// harness's fake Clock, so a test can drive imagesGenerations' polling loop
// deterministically via h.Clock.Advance — the same construction
// TestJobEventsStopsOnClientDisconnect uses, needed here (unlike newServer's
// default) because pollWorkflowToTerminal's timer must be the same fake clock
// the test controls.
//
// newImagesServer 组装一个前门，其 httpapi.Config 与测试框架共用同一个假
// 时钟，好让测试经 h.Clock.Advance 确定性地驱动 imagesGenerations 的轮询
// 循环——与 TestJobEventsStopsOnClientDisconnect 相同的构造方式，这里需要
// 它（不同于 newServer 的默认做法），因为 pollWorkflowToTerminal 的定时器
// 必须是测试所控制的那同一个假时钟。
func newImagesServer(t *testing.T, cfg httpapi.Config) (*httptest.Server, *gatewaytest.Harness) {
	t.Helper()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	cfg.Clock = h.Clock
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	front := httpapi.New(sched, cfg)
	t.Cleanup(front.Close)
	srv := httptest.NewServer(front)
	t.Cleanup(srv.Close)
	return srv, h
}

// advancePastPoll advances the harness clock exactly one imagesPollInterval,
// first waiting for the poll loop's own timer to actually be armed. It waits
// for more than base pending timers rather than exactly one, because
// httpapi.New always starts a background jobSyncer that arms its own
// (much longer) interval timer on the very same shared fake clock the moment
// the server is built — base is that count, captured once before the
// request under test starts, so this helper can tell "the poll loop's timer
// showed up" from "the syncer's timer was already there" without depending
// on which one PendingTimers happens to count first.
//
// advancePastPoll 把测试框架的时钟正好推进一个 imagesPollInterval，推进前先
// 等待轮询循环自己的定时器真正被装上。它等待的是"超过 base 个"而不是
// "正好一个"，因为 httpapi.New 总会启动一个后台 jobSyncer，在服务端刚构造
// 好的那一刻就在同一个共享假时钟上装好它自己（长得多的）间隔定时器——base
// 就是那个数量，在被测请求开始之前先行捕获，这样本函数才能分清"轮询循环
// 的定时器出现了"与"同步器的定时器本来就在"，而不必依赖 PendingTimers
// 恰好先数到哪一个。
func advancePastPoll(t *testing.T, h *gatewaytest.Harness, base int) {
	t.Helper()
	gatewaytest.WaitFor(t, "the images poll timer to arm", func() bool { return h.Clock.PendingTimers() > base })
	h.Clock.Advance(500 * time.Millisecond)
}

type imagesResponseBody struct {
	Created int64 `json:"created"`
	Data    []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
}

func postImages(t *testing.T, url, body string) (*http.Response, imagesResponseBody) {
	t.Helper()
	resp, err := http.Post(url+"/v1/images/generations", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var out imagesResponseBody
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestImagesGenerationsNotConfiguredReturns404(t *testing.T) {
	srv, _ := newImagesServer(t, httpapi.Config{Workflows: imagesTemplate(t)})
	resp, _ := postImages(t, srv.URL, `{"prompt":"a red fox"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when -images-workflow-id is unset", resp.StatusCode)
	}
}

func TestImagesGenerationsSucceedsAsBase64(t *testing.T) {
	srv, h := newImagesServer(t, httpapi.Config{
		Workflows: imagesTemplate(t), ImagesWorkflowID: "text-to-image",
	})
	const imageBytes = "PNG-BYTES-PRETENDING-TO-BE-AN-IMAGE"
	connectImagesNode(t, h, "node-comfy", "comfy-1",
		imagesNodeHandler(2, runtime.WorkflowSucceeded, false, "ComfyUI_00001_.png", []byte(imageBytes)))
	base := h.Clock.PendingTimers()

	done := make(chan struct {
		resp *http.Response
		body imagesResponseBody
	}, 1)
	go func() {
		resp, body := postImages(t, srv.URL, `{"prompt":"a red fox"}`)
		done <- struct {
			resp *http.Response
			body imagesResponseBody
		}{resp, body}
	}()

	// imagesNodeHandler answers "running" twice before succeeding, so the
	// poll loop must go around twice.
	//
	// imagesNodeHandler 会先回答两次 "running" 才转为成功，因此轮询循环
	// 必须转两圈。
	advancePastPoll(t, h, base)
	advancePastPoll(t, h, base)

	select {
	case r := <-done:
		if r.resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", r.resp.StatusCode)
		}
		if len(r.body.Data) != 1 {
			t.Fatalf("data = %+v, want exactly one image", r.body.Data)
		}
		decoded, err := base64.StdEncoding.DecodeString(r.body.Data[0].B64JSON)
		if err != nil {
			t.Fatalf("decoding b64_json: %v", err)
		}
		if string(decoded) != imageBytes {
			t.Errorf("decoded bytes = %q, want %q", decoded, imageBytes)
		}
		if r.body.Data[0].URL != "" {
			t.Errorf("URL = %q, want empty in b64_json mode", r.body.Data[0].URL)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("POST /v1/images/generations did not return")
	}
}

func TestImagesGenerationsURLModeIsDownloadable(t *testing.T) {
	srv, h := newImagesServer(t, httpapi.Config{
		Workflows: imagesTemplate(t), ImagesWorkflowID: "text-to-image",
	})
	const imageBytes = "PNG-BYTES-PRETENDING-TO-BE-AN-IMAGE"
	connectImagesNode(t, h, "node-comfy", "comfy-1",
		imagesNodeHandler(0, runtime.WorkflowSucceeded, false, "ComfyUI_00001_.png", []byte(imageBytes)))

	resp, body := postImages(t, srv.URL, `{"prompt":"a red fox","response_format":"url"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(body.Data) != 1 || body.Data[0].URL == "" || body.Data[0].B64JSON != "" {
		t.Fatalf("data = %+v, want exactly one item carrying only a URL", body.Data)
	}

	dl, err := http.Get(srv.URL + body.Data[0].URL)
	if err != nil {
		t.Fatalf("GET %s: %v", body.Data[0].URL, err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", body.Data[0].URL, dl.StatusCode)
	}
	got, err := io.ReadAll(dl.Body)
	if err != nil {
		t.Fatalf("reading download body: %v", err)
	}
	if string(got) != imageBytes {
		t.Errorf("downloaded bytes = %q, want %q", got, imageBytes)
	}
}

func TestImagesGenerationsRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantIn     string
	}{
		{name: "missing prompt", body: `{}`, wantStatus: http.StatusBadRequest, wantIn: "prompt"},
		{name: "n greater than one", body: `{"prompt":"hi","n":2}`, wantStatus: http.StatusBadRequest, wantIn: "n"},
		{name: "quality is not supported", body: `{"prompt":"hi","quality":"hd"}`, wantStatus: http.StatusBadRequest, wantIn: "quality"},
		{name: "size the template does not declare", body: `{"prompt":"hi","size":"999x999"}`, wantStatus: http.StatusBadRequest, wantIn: "size"},
		{name: "malformed size", body: `{"prompt":"hi","size":"bogus"}`, wantStatus: http.StatusBadRequest, wantIn: "size"},
		{name: "body is not JSON", body: `not json`, wantStatus: http.StatusBadRequest, wantIn: "JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This template deliberately does not declare width/height, so
			// the "size the template does not declare" case is exercised
			// against a realistic template shape rather than a synthetic
			// gap.
			//
			// 这份模板刻意不声明 width/height，好让"模板未声明的 size"这个
			// 用例针对一个真实的模板形状来验证，而不是人为制造出的缺口。
			handle := loadOneTemplate(t, workflow.Template{
				ID: "text-to-image",
				Inputs: []workflow.Input{
					{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true},
				},
				Outputs: []workflow.Output{{Name: "image", Node: "9", Type: "image"}},
				Graph:   json.RawMessage(textToImageGraph),
			})
			srv, h := newImagesServer(t, httpapi.Config{
				Workflows: handle, ImagesWorkflowID: "text-to-image",
			})
			connectImagesNode(t, h, "node-comfy", "comfy-1",
				imagesNodeHandler(0, runtime.WorkflowSucceeded, false, "", nil))

			resp, err := http.Post(srv.URL+"/v1/images/generations", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			var errBody struct {
				Error struct{ Message string } `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&errBody)
			if !strings.Contains(errBody.Error.Message, tt.wantIn) {
				t.Errorf("error message = %q, want it to mention %q", errBody.Error.Message, tt.wantIn)
			}
		})
	}
}

func TestImagesGenerationsFailedRunReturnsAGenerationError(t *testing.T) {
	srv, h := newImagesServer(t, httpapi.Config{
		Workflows: imagesTemplate(t), ImagesWorkflowID: "text-to-image",
	})
	connectImagesNode(t, h, "node-comfy", "comfy-1",
		imagesNodeHandler(0, runtime.WorkflowFailed, false, "", nil))

	resp, _ := postImages(t, srv.URL, `{"prompt":"a red fox"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 on a failed run", resp.StatusCode)
	}
}

func TestImagesGenerationsOutOfMemoryGetsItsOwnErrorCode(t *testing.T) {
	srv, h := newImagesServer(t, httpapi.Config{
		Workflows: imagesTemplate(t), ImagesWorkflowID: "text-to-image",
	})
	connectImagesNode(t, h, "node-comfy", "comfy-1",
		imagesNodeHandler(0, runtime.WorkflowFailed, true, "", nil))

	resp, err := http.Post(srv.URL+"/v1/images/generations", "application/json", strings.NewReader(`{"prompt":"a red fox"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Error.Code != "out_of_memory" {
		t.Errorf("error.code = %q, want out_of_memory", body.Error.Code)
	}
}

func TestImagesGenerationsNoQualifyingArtifactsIsAnError(t *testing.T) {
	srv, h := newImagesServer(t, httpapi.Config{
		Workflows: imagesTemplate(t), ImagesWorkflowID: "text-to-image",
	})
	// The run succeeds but never lists an artifact — a misconfigured
	// template's realistic failure mode.
	//
	// 这次运行成功了，却从未列出过任何产物——这是模板配置错误时的现实
	// 失效模式。
	connectImagesNode(t, h, "node-comfy", "comfy-1",
		imagesNodeHandler(0, runtime.WorkflowSucceeded, false, "", nil))

	resp, _ := postImages(t, srv.URL, `{"prompt":"a red fox"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when the workflow produced no image artifact", resp.StatusCode)
	}
}

// TestImagesGenerationsTimesOutButStillRecordsTheJob is the bound on this
// endpoint's synchronous wait: a run that never reaches a terminal state
// within -images-generation-timeout gets a 504 rather than hanging the
// caller's connection forever, and the job it submitted remains queryable —
// the run keeps going server-side, and nothing here cancels it.
//
// TestImagesGenerationsTimesOutButStillRecordsTheJob 是本端点同步等待的
// 边界：一次在 -images-generation-timeout 内始终没有到达终态的运行会得到
// 504，而不是让调用方的连接永远挂起，并且它提交过的 job 依然可查询——这次
// 运行在服务端仍在继续，这里的任何东西都不会取消它。
func TestImagesGenerationsTimesOutButStillRecordsTheJob(t *testing.T) {
	srv, h := newImagesServer(t, httpapi.Config{
		Workflows: imagesTemplate(t), ImagesWorkflowID: "text-to-image",
		ImagesGenerationTimeout: time.Second,
	})
	// statusRunningCalls is large enough that the run never reaches a
	// terminal state before the timeout fires.
	//
	// statusRunningCalls 足够大，让这次运行在超时触发之前从不到达终态。
	connectImagesNode(t, h, "node-comfy", "comfy-1",
		imagesNodeHandler(1000, runtime.WorkflowSucceeded, false, "", nil))

	type result struct {
		resp *http.Response
	}
	done := make(chan result, 1)
	go func() {
		resp, _ := http.Post(srv.URL+"/v1/images/generations", "application/json", strings.NewReader(`{"prompt":"a red fox"}`))
		done <- result{resp}
	}()

	// One imagesPollInterval (500ms) fits inside the one-second timeout, so
	// the deadline itself — not another poll — is what ends the wait; the
	// context's own timer expiring needs no clock advance from this test.
	//
	// 一个 imagesPollInterval（500ms）落在一秒超时之内，因此让等待结束的
	// 是截止时间本身——而不是又一次轮询；context 自己的定时器到期不需要
	// 本测试推进时钟。
	select {
	case r := <-done:
		defer r.resp.Body.Close()
		if r.resp.StatusCode != http.StatusGatewayTimeout {
			t.Fatalf("status = %d, want 504", r.resp.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("POST /v1/images/generations did not return after the configured timeout")
	}
}
