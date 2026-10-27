package comfyui_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/workflow/comfyui"
)

// TestLiveComfyUI exercises the adapter against a real ComfyUI server, which
// the fake-server tests above cannot do: it is the only check that the
// endpoints, field names and capability strings this package assumes still
// match a shipping ComfyUI release (STATUS.md's A06 — the "补可复现的真实
// ComfyUI 链路验证" M0 needs).
//
// It is opt-in — set COMFYUI_BASE_URL (e.g. http://127.0.0.1:8188) to run
// it — so `go test ./...` stays hermetic on machines without a real ComfyUI
// instance. This repository's own development sandbox has neither a GPU nor
// a reachable ComfyUI server, so this test has never been run against a real
// backend in this codebase's CI or by the author of this file; running it at
// least once against a real deployment is a known, explicitly recorded gap
// (STATUS.md's A06), the same status Ollama's equivalent test held before
// someone with a real Ollama server ran it.
//
// TestLiveComfyUI 针对一个真实 ComfyUI 服务器验证适配器，这是上面那些用假服务器
// 做的测试做不到的：它是唯一能确认本包所假设的端点、字段名与能力字符串仍与一个
// 正在发行的 ComfyUI 版本相符的检查（STATUS.md 的 A06——M0 所需的「补可复现的
// 真实 ComfyUI 链路验证」）。
//
// 它是可选启用的——设置 COMFYUI_BASE_URL（如 http://127.0.0.1:8188）才会运行——
// 这样没有真实 ComfyUI 实例的机器上 `go test ./...` 仍保持自给自足。本仓库自己
// 的开发环境既没有 GPU 也没有可达的 ComfyUI 服务器，因此本测试从未在本代码库的
// CI 里、或被本文件的作者针对真实后端跑过；至少针对一次真实部署跑通它，是一项
// 已知且如实记录的缺口（STATUS.md 的 A06），与 Ollama 的等价测试在被某个拥有
// 真实 Ollama 服务器的人跑通之前所处的状态相同。
func TestLiveComfyUI(t *testing.T) {
	baseURL := os.Getenv("COMFYUI_BASE_URL")
	if baseURL == "" {
		t.Skip("set COMFYUI_BASE_URL to run the live ComfyUI test")
	}

	cfg := runtime.Config{
		ID:             "live-comfyui",
		Kind:           runtime.KindComfyUI,
		BaseURL:        baseURL,
		ProbeTimeout:   10 * time.Second,
		RequestTimeout: 30 * time.Second,
	}.Normalize()

	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport}

	rt, err := comfyui.New(cfg, runtime.Dependencies{
		HTTPClient: httpClient,
		WSDialer:   comfyui.NewDialer(httpClient),
		Clock:      runtime.NewSystemClock(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()
	workflow := rt.(*comfyui.Runtime)

	ctx := context.Background()

	probe, err := workflow.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !probe.IdentityVerified {
		t.Fatal("IdentityVerified = false against a real ComfyUI server")
	}
	t.Logf("probe: version=%s evidence=%s", probe.Version, probe.Evidence)

	health, err := workflow.Health(ctx)
	if err != nil || health.State != runtime.StateHealthy {
		t.Fatalf("Health: state=%q err=%v", health.State, err)
	}
	t.Logf("health: state=%s latency=%s", health.State, health.Latency)

	discovery, err := workflow.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, w := range discovery.Warnings {
		t.Logf("discovery warning: %s", w)
	}
	t.Logf("discover: %d node types, %d models", len(discovery.NodeTypes), len(discovery.Models))

	// Submit/Cancel/Status/artifact integrity all require an actual runnable
	// graph, which depends on whatever checkpoints and custom nodes happen
	// to be installed on the target server — there is no universal "trivial"
	// ComfyUI workflow the way an empty chat prompt exists for an LLM
	// backend. COMFYUI_TEST_WORKFLOW_FILE names a local JSON file holding a
	// real, known-good API-format graph for that specific deployment; a
	// maintainer running this test points it at one exported from their own
	// ComfyUI instance (the same "Save (API Format)" export the rest of this
	// package's fixtures describe).
	//
	// Submit/Cancel/Status/产物完整性都需要一份真正可运行的图，而这依赖于目标
	// 服务器上恰好装了哪些 checkpoint 与自定义节点——不像 LLM 后端有一个空
	// 提示词那样的通用「平凡」ComfyUI 工作流。COMFYUI_TEST_WORKFLOW_FILE 指向
	// 一个本地 JSON 文件，其中是针对那台具体部署的真实、已验证可用的 API
	// Format 图；运行本测试的维护者，把它指向从自己 ComfyUI 实例导出的一份
	// （与本包其余测试夹具所说的同一种「Save (API Format)」导出）。
	workflowFile := os.Getenv("COMFYUI_TEST_WORKFLOW_FILE")
	if workflowFile == "" {
		t.Skip("set COMFYUI_TEST_WORKFLOW_FILE to a real API-format graph exported from this server to run submit/status/cancel/artifact checks")
	}
	template, err := os.ReadFile(workflowFile)
	if err != nil {
		t.Fatalf("reading COMFYUI_TEST_WORKFLOW_FILE: %v", err)
	}

	run, err := workflow.Submit(ctx, runtime.WorkflowRequest{Template: template, IdempotencyKey: "live-test-" + time.Now().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Logf("submitted run %s", run.ID)

	status := pollUntilTerminal(t, ctx, workflow, run.ID, 5*time.Minute)
	t.Logf("final status: state=%s error=%q out_of_memory=%v", status.State, status.ErrorSummary, status.OutOfMemory)
	if status.State != runtime.WorkflowSucceeded {
		t.Fatalf("workflow did not succeed: state=%s error=%q", status.State, status.ErrorSummary)
	}

	artifacts, err := workflow.Artifacts(ctx, run.ID)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(artifacts) == 0 {
		t.Fatal("a succeeded run reported no artifacts; COMFYUI_TEST_WORKFLOW_FILE should produce at least one output")
	}
	for _, ref := range artifacts {
		artifact, err := workflow.OpenArtifact(ctx, ref)
		if err != nil {
			t.Fatalf("OpenArtifact(%+v): %v", ref, err)
		}
		body, err := io.ReadAll(artifact.Body)
		artifact.Body.Close()
		if err != nil {
			t.Fatalf("reading artifact body: %v", err)
		}
		if len(body) == 0 {
			t.Errorf("artifact %+v has an empty body", ref)
		}
		t.Logf("artifact %s/%s: %d bytes, content-type %s", ref.Subfolder, ref.Filename, len(body), artifact.ContentType)
	}

	// Cancelling an already-finished run is not an error condition this
	// adapter invents a success for — it is documented, expected behavior
	// (see runtime.go's Cancel), so this exercises that real response rather
	// than treating it as a test failure.
	//
	// 取消一个已经结束的运行，不是这个适配器要去编造成功的错误情形——这是
	// 已有文档说明、预期之中的行为（见 runtime.go 的 Cancel），因此这里练的
	// 是这个真实的响应，而不是把它当作测试失败。
	if err := workflow.Cancel(ctx, run.ID); err == nil {
		t.Error("Cancel on an already-succeeded run returned nil, want cancel_unsupported")
	} else {
		var rtErr *runtime.RuntimeError
		if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorCancelUnsupported {
			t.Errorf("Cancel on an already-succeeded run returned %v, want an ErrorCancelUnsupported RuntimeError", err)
		}
	}
}

// pollUntilTerminal polls Status until it reports a terminal state or
// timeout elapses, sleeping briefly between polls — deliberately not using
// this package's own runtime.Clock abstraction, since this is a real wall
// clock wait against a real, external, uncontrolled server.
//
// pollUntilTerminal 轮询 Status 直到它报告一个终态或超时耗尽为止，轮询之间
// 短暂休眠——刻意不使用本包自己的 runtime.Clock 抽象，因为这是针对一个真实、
// 外部、不受控制的服务器的真实墙钟等待。
func pollUntilTerminal(t *testing.T, ctx context.Context, workflow *comfyui.Runtime, runID string, timeout time.Duration) runtime.WorkflowStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		status, err := workflow.Status(ctx, runID)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		switch status.State {
		case runtime.WorkflowSucceeded, runtime.WorkflowFailed, runtime.WorkflowCancelled:
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow did not reach a terminal state within %s; last state=%s queue_position=%d", timeout, status.State, status.QueuePosition)
		}
		time.Sleep(time.Second)
	}
}
