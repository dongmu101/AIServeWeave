package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// workflowCapableSnapshot describes one ComfyUI instance. Its capabilities
// sit on the runtime, not on a model: a ComfyUI server's "models" are the
// checkpoint files it has on disk, and none of them is what a caller names
// when submitting a workflow.
//
// workflowCapableSnapshot 描述一个 ComfyUI 实例。它的能力挂在 runtime 上而不是模型
// 上：ComfyUI 的「模型」是它磁盘上的 checkpoint 文件，提交工作流时调用方指名的不是
// 其中任何一个。
func workflowCapableSnapshot(runtimeID string) runtime.Snapshot {
	return runtime.Snapshot{
		Descriptor: runtime.Descriptor{ID: runtimeID, Kind: runtime.KindComfyUI, BaseURL: "http://127.0.0.1:8188", MaxConcurrent: 2},
		State:      runtime.StateHealthy,
		Discovery: runtime.Discovery{
			Capabilities: runtime.CapabilitySet{
				runtime.CapabilityWorkflowExecution: {Level: runtime.SupportSupported},
				runtime.CapabilityWorkflowEvents:    {Level: runtime.SupportSupported},
				runtime.CapabilityWorkflowCancel:    {Level: runtime.SupportSupported},
				runtime.CapabilityArtifactRead:      {Level: runtime.SupportSupported},
			},
			Models: []runtime.Model{{ID: "flux1-dev.safetensors"}},
		},
	}
}

// workflowHandler answers Submit with a run id tagged by source, and Status
// with a running snapshot.
//
// workflowHandler 用带 source 标记的 run id 应答 Submit，用 running 快照应答 Status。
func workflowHandler(source string, count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		switch req.GetOperation() {
		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
			payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "prompt-" + source})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_WORKFLOW_STATUS:
			payload, err := tunnelwire.MarshalWorkflowStatus(runtime.WorkflowStatus{
				State:        runtime.WorkflowRunning,
				ErrorSummary: "served by " + source,
			})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		}
		return errors.New("unsupported operation")
	}
}

// submitEchoHandler replies with a run id built from the template bytes it
// actually received, so a test can assert the graph survived the tunnel.
//
// submitEchoHandler 用它实际收到的模板字节构造 run id 回复，好让测试断言图确实过了
// 隧道。
func submitEchoHandler() gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		var template []byte
		for _, chunk := range body {
			template = append(template, chunk...)
		}
		payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: string(template)})
		if err != nil {
			return err
		}
		return reply(gatewaytest.DataFrame(payload))
	}
}

func TestSubmitWorkflowDispatchesToAWorkflowCapableNode(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-chat", "ollama-1", chatCapableSnapshot("ollama-1", "qwen3:8b"), chatHandler("node-chat", nil))
	connectNode(t, h, "node-comfy", "comfy-1", workflowCapableSnapshot("comfy-1"), submitEchoHandler())

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	run, candidate, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{
		Template: json.RawMessage(`{"3":{"class_type":"KSampler","inputs":{}}}`),
		ClientID: "job-1",
	})
	if err != nil {
		t.Fatalf("SubmitWorkflow() error = %v, want nil", err)
	}
	if candidate.NodeID != "node-comfy" {
		t.Errorf("candidate.NodeID = %q, want node-comfy", candidate.NodeID)
	}
	if want := `{"3":{"class_type":"KSampler","inputs":{}}}`; run.ID != want {
		t.Errorf("the node received template %q, want %q", run.ID, want)
	}
}

func TestSubmitWorkflowReturnsErrNoCapableNodeWhenNothingRunsWorkflows(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-chat", "ollama-1", chatCapableSnapshot("ollama-1", "qwen3:8b"), chatHandler("node-chat", nil))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	_, _, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(`{}`)})
	if !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Errorf("err = %v, want ErrNoCapableNode", err)
	}
}

// TestSubmitWorkflowRetriesOnlyBeforeTheBackendCouldHaveSeenIt is the
// scheduling half of README's "ComfyUI 作业一旦开始执行，不应自动迁移到其他节点".
// A submit that failed on the way in may be retried elsewhere; one that
// failed with the backend's own error may already sit in ComfyUI's queue,
// and retrying it would generate a second image nobody asked for.
//
// TestSubmitWorkflowRetriesOnlyBeforeTheBackendCouldHaveSeenIt 是 README
// 「ComfyUI 作业一旦开始执行，不应自动迁移到其他节点」在调度这一侧的落实。在去程上
// 就失败的提交可以换节点重试；带着后端自身错误失败的那个，可能已经躺在 ComfyUI 的
// 队列里，重试会生成第二张没人要的图。
func TestSubmitWorkflowRetriesOnlyBeforeTheBackendCouldHaveSeenIt(t *testing.T) {
	tests := []struct {
		name        string
		failWith    *gatewaytest.WireError
		wantRetried bool
	}{
		{
			name:        "backpressure never reached the backend",
			failWith:    &gatewaytest.WireError{Code: "backpressure", Message: "no capacity", Retryable: true},
			wantRetried: true,
		},
		{
			name:        "connection failed before the backend answered",
			failWith:    &gatewaytest.WireError{Code: "connection_failed", Message: "dial failed", Retryable: true},
			wantRetried: true,
		},
		{
			name:        "upstream error may already have queued the workflow",
			failWith:    &gatewaytest.WireError{Code: "upstream_error", Message: "backend said no", Retryable: true},
			wantRetried: false,
		},
		{
			name:        "timeout leaves the submit's fate unknown",
			failWith:    &gatewaytest.WireError{Code: "timeout", Message: "took too long", Retryable: true},
			wantRetried: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := gatewaytest.NewHarness(t, tunnelserver.Config{})
			var failingCount, workingCount atomic.Int32
			failing := func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
				failingCount.Add(1)
				return tt.failWith
			}
			// The failing node has two idle slots, so it sorts ahead of the
			// working one and is always tried first.
			//
			// 失败节点有两个空闲槽，因此排在可用节点前面，总是先被尝试。
			connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), failing, failing)
			connectNode(t, h, "node-b", "comfy-1", workflowCapableSnapshot("comfy-1"), workflowHandler("node-b", &workingCount))

			sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
			run, candidate, err := sched.SubmitWorkflow(context.Background(), runtime.WorkflowRequest{
				Template: json.RawMessage(`{}`),
			})

			if tt.wantRetried {
				if err != nil {
					t.Fatalf("SubmitWorkflow() error = %v, want it to fail over", err)
				}
				if candidate.NodeID != "node-b" || run.ID != "prompt-node-b" {
					t.Errorf("SubmitWorkflow() went to %+v with run %q, want node-b", candidate, run.ID)
				}
				return
			}
			if err == nil {
				t.Fatalf("SubmitWorkflow() error = nil, want the failure to be returned rather than retried elsewhere")
			}
			if got := workingCount.Load(); got != 0 {
				t.Errorf("the second node was contacted %d times, want 0", got)
			}
		})
	}
}

// TestWorkflowStatusAsksTheNodeThatRanIt confirms status follows the stored
// candidate rather than re-selecting: another node has never heard of this
// run id, so a fresh selection would answer about nothing.
//
// TestWorkflowStatusAsksTheNodeThatRanIt 确认状态查询跟随已存的候选而不是重新选择：
// 别的节点从未听说过这个 run id，重新选择等于在问一个不存在的东西。
func TestWorkflowStatusAsksTheNodeThatRanIt(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var aCount, bCount atomic.Int32
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), workflowHandler("node-a", &aCount), workflowHandler("node-a", &aCount))
	connectNode(t, h, "node-b", "comfy-1", workflowCapableSnapshot("comfy-1"), workflowHandler("node-b", &bCount))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	status, err := sched.WorkflowStatus(context.Background(),
		scheduler.Candidate{NodeID: "node-b", RuntimeID: "comfy-1"}, "prompt-1")
	if err != nil {
		t.Fatalf("WorkflowStatus() error = %v, want nil", err)
	}
	if status.State != runtime.WorkflowRunning {
		t.Errorf("status.State = %q, want %q", status.State, runtime.WorkflowRunning)
	}
	if status.ErrorSummary != "served by node-b" {
		t.Errorf("status came from %q, want node-b", status.ErrorSummary)
	}
	if aCount.Load() != 0 {
		t.Errorf("node-a was contacted %d times, want 0", aCount.Load())
	}
}

// eventStreamHandler answers Subscribe with three events, the last of them
// terminal.
//
// eventStreamHandler 用三个事件应答 Subscribe，最后一个是终态事件。
func eventStreamHandler(source string, count *atomic.Int32) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if count != nil {
			count.Add(1)
		}
		if req.GetOperation() != tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE {
			return errors.New("unsupported operation")
		}
		for _, ev := range []runtime.WorkflowEvent{
			{Type: runtime.WorkflowEventStarted, RunID: "prompt-1"},
			{Type: runtime.WorkflowEventProgress, RunID: "prompt-1", NodeID: source},
			{Type: runtime.WorkflowEventSucceeded, RunID: "prompt-1"},
		} {
			payload, err := tunnelwire.MarshalWorkflowEvent(ev)
			if err != nil {
				return err
			}
			if err := reply(gatewaytest.DataFrame(payload)); err != nil {
				return err
			}
		}
		return nil
	}
}

// TestWorkflowEventsAsksTheNodeThatRanIt is Status's argument applied to the
// event stream: a subscription is to one ComfyUI's WebSocket, so it can only
// be opened where the run actually is.
//
// TestWorkflowEventsAsksTheNodeThatRanIt 是 Status 那条理由在事件流上的同一应用：
// 订阅连的是某一个 ComfyUI 的 WebSocket，因此只能在运行真正所在的地方打开。
func TestWorkflowEventsAsksTheNodeThatRanIt(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var aCount, bCount atomic.Int32
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"),
		eventStreamHandler("node-a", &aCount), eventStreamHandler("node-a", &aCount))
	connectNode(t, h, "node-b", "comfy-1", workflowCapableSnapshot("comfy-1"), eventStreamHandler("node-b", &bCount))

	stream, err := sched(h).WorkflowEvents(context.Background(),
		scheduler.Candidate{NodeID: "node-b", RuntimeID: "comfy-1"}, "prompt-1")
	if err != nil {
		t.Fatalf("WorkflowEvents() error = %v, want nil", err)
	}
	defer stream.Close()

	var got []runtime.WorkflowEventType
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv() error = %v, want nil or EOF", err)
		}
		got = append(got, ev.Type)
	}
	want := []runtime.WorkflowEventType{runtime.WorkflowEventStarted, runtime.WorkflowEventProgress, runtime.WorkflowEventSucceeded}
	if len(got) != len(want) {
		t.Fatalf("received %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
	if aCount.Load() != 0 {
		t.Errorf("node-a was contacted %d times, want 0", aCount.Load())
	}
}

func sched(h *gatewaytest.Harness) *scheduler.Scheduler {
	return scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
}

// TestCancelWorkflowAsksTheNodeThatRanIt is the third application of the same
// rule: an interrupt is meaningful only to the ComfyUI whose queue holds the
// run.
//
// TestCancelWorkflowAsksTheNodeThatRanIt 是同一条规则的第三次应用：中断只对队列里
// 装着这次运行的那个 ComfyUI 有意义。
func TestCancelWorkflowAsksTheNodeThatRanIt(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var aCount, bCount atomic.Int32
	cancelHandler := func(count *atomic.Int32) gatewaytest.SlotHandler {
		return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			count.Add(1)
			if req.GetOperation() != tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL {
				return errors.New("unsupported operation")
			}
			return nil
		}
	}
	connectNode(t, h, "node-a", "comfy-1", workflowCapableSnapshot("comfy-1"), cancelHandler(&aCount), cancelHandler(&aCount))
	connectNode(t, h, "node-b", "comfy-1", workflowCapableSnapshot("comfy-1"), cancelHandler(&bCount))

	err := sched(h).CancelWorkflow(context.Background(),
		scheduler.Candidate{NodeID: "node-b", RuntimeID: "comfy-1"}, "prompt-1")
	if err != nil {
		t.Fatalf("CancelWorkflow() error = %v, want nil", err)
	}
	if bCount.Load() != 1 {
		t.Errorf("node-b was contacted %d times, want 1", bCount.Load())
	}
	if aCount.Load() != 0 {
		t.Errorf("node-a was contacted %d times, want 0", aCount.Load())
	}
}
