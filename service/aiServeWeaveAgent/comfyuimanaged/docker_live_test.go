package comfyuimanaged

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestLiveDocker exercises Start/Status/Stop and idempotent adoption against
// a real docker daemon (STATUS.md's P2 ComfyUI Managed Docker deployment,
// subtask one — see the design doc's 5.4 section). It uses
// curlimages/curl:8.11.1 with a long-running Command override rather than a
// real ComfyUI image: this validates the Docker lifecycle machinery itself,
// not the ComfyUI wire protocol or GPU passthrough, which need a Linux
// NVIDIA host this environment does not have (same boundary A06/P10 already
// recorded). Gated behind AISW_DOCKER_LIVE_TEST, off by default, mirroring
// ollama/live_test.go and comfyui/live_test.go's env-var-gated precedent so
// the default `go test ./...` never depends on a real Docker daemon.
//
// TestLiveDocker 用真实 Docker daemon 驱动 Start/Status/Stop 与幂等接管
// （STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务一，见设计文档 5.4 节）。它用
// curlimages/curl:8.11.1 配合 Command 覆盖跑一个长驻进程，而不是真的 ComfyUI
// 镜像：验证的是 Docker 生命周期管理机制本身，不是 ComfyUI 线上协议或 GPU 直通——
// 后两者需要本环境没有的 Linux NVIDIA 主机（与 A06/P10 已记录的边界一致）。经
// AISW_DOCKER_LIVE_TEST 门控，默认关闭，与 ollama/live_test.go、comfyui/live_test.go
// 的环境变量门控先例一致，让默认的 `go test ./...` 从不依赖真实 Docker daemon。
func TestLiveDocker(t *testing.T) {
	if os.Getenv("AISW_DOCKER_LIVE_TEST") == "" {
		t.Skip("AISW_DOCKER_LIVE_TEST not set; skipping real Docker test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	l := NewLauncher(nil, slog.New(slog.DiscardHandler))
	spec := Spec{
		ContainerName: "aiserveweave-comfyuimanaged-live-test",
		Image:         "curlimages/curl:8.11.1",
		Port:          38188,
		Command:       []string{"sleep", "300"},
	}

	t.Cleanup(func() {
		if err := l.Stop(context.Background(), spec.ContainerName); err != nil {
			t.Logf("cleanup: Stop() error = %v", err)
		}
	})

	if err := l.Start(ctx, spec); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	state, err := l.Status(ctx, spec.ContainerName)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if state != StateRunning {
		t.Fatalf("Status() = %v, want %v", state, StateRunning)
	}

	// Start again: idempotent adoption must not fail or recreate the
	// container.
	if err := l.Start(ctx, spec); err != nil {
		t.Fatalf("second Start() (adoption) error = %v", err)
	}

	if err := l.Stop(ctx, spec.ContainerName); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	state, err = l.Status(ctx, spec.ContainerName)
	if err != nil {
		t.Fatalf("Status() after Stop() error = %v", err)
	}
	if state != StatePending {
		t.Fatalf("Status() after Stop() = %v, want %v", state, StatePending)
	}
}
