// Package comfyuimanaged starts, adopts, and stops a single ComfyUI Docker
// container on this node (STATUS.md's P2 ComfyUI Managed Docker deployment,
// subtask one — see
// docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-design.md).
// It only manages the container: once the container's port accepts
// connections, the caller registers it with runtime.Manager exactly like an
// External ComfyUI instance, so comfyui.Runtime's existing Probe/Discover
// still perform the ComfyUI-specific identity and capability checks — this
// package never speaks the ComfyUI wire protocol. Like hostresources, it
// shells out to a platform tool (the docker CLI) instead of adding an SDK
// dependency, so the Agent's dependency line stays exactly what AGENTS.md
// requires.
//
// comfyuimanaged 在本节点启动、接管或停止单个 ComfyUI Docker 容器（STATUS.md 的
// P2 ComfyUI Managed Docker 部署子任务一，设计文档见
// docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-design.md）。
// 它只管理容器本身：容器端口能连通之后，调用方按 External 实例同样的方式把它注册进
// runtime.Manager，因此 comfyui.Runtime 既有的 Probe/Discover 仍然承担 ComfyUI
// 专属的身份与能力校验——本包从不说 ComfyUI 的线上协议。与 hostresources 一样，它
// shell out 到平台工具（docker CLI）而不是新增 SDK 依赖，这样 Agent 的依赖线就仍是
// AGENTS.md 要求的样子。
package comfyuimanaged

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/common/runtime"
)

// comfyUIContainerPort is the port ComfyUI's own server listens on inside
// the container; it is never configurable because it is the image's
// contract, not this node's.
const comfyUIContainerPort = 8188

// dockerTimeout bounds every docker CLI invocation, so a hung daemon never
// blocks Start/Stop/Status indefinitely.
const dockerTimeout = 30 * time.Second

// waitReadyPollInterval is how often WaitReady retries its TCP dial.
const waitReadyPollInterval = 500 * time.Millisecond

// Spec describes one ComfyUI container this Agent manages via the docker
// CLI. Every field comes from this node's own flags (STATUS.md's P2 ComfyUI
// Managed Docker deployment, subtask one): Start never accepts a Spec
// pushed by the Gateway or control plane.
//
// Spec 描述本 Agent 经 docker CLI 管理的一个 ComfyUI 容器。每个字段都来自本节点
// 自己的 flag（STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务一）：Start 从不
// 接受 Gateway 或控制面下发的 Spec。
type Spec struct {
	ContainerName string            // docker container name; must be unique on this host / docker 容器名，本机唯一
	Image         string            // must carry an explicit, non-"latest" tag / 必须携带显式、非 "latest" 的 tag
	Port          int               // host binds 127.0.0.1:Port to the container's comfyUIContainerPort / 宿主机绑定 127.0.0.1:Port 到容器的 comfyUIContainerPort
	GPUDevices    []string          // e.g. []string{"0"}; empty omits --gpus entirely / 例如 []string{"0"}；空则完全不传 --gpus
	ModelPaths    map[string]string // container path -> host path, mounted read-only / 容器路径 -> 宿主机路径，只读挂载
	StoragePaths  map[string]string // container path -> host path, mounted read-write / 容器路径 -> 宿主机路径，读写挂载
	MemoryLimit   string            // docker --memory value (e.g. "32g"); empty is unlimited / docker --memory 的值（如 "32g"）；空表示不限
	Command       []string          // optional entrypoint override; nil uses the image's own / 可选的入口命令覆盖；nil 时使用镜像自带的
}

// validate rejects a Spec before any docker command runs, so a
// misconfigured tag or missing name never reaches the shell.
func (s Spec) validate() error {
	if s.ContainerName == "" {
		return errors.New("comfyuimanaged: ContainerName is required")
	}
	if s.Port <= 0 {
		return fmt.Errorf("comfyuimanaged: Port must be positive, got %d", s.Port)
	}
	tag, ok := imageTag(s.Image)
	if !ok || tag == "" {
		return fmt.Errorf("comfyuimanaged: Image %q must carry an explicit tag", s.Image)
	}
	if tag == "latest" {
		return fmt.Errorf(`comfyuimanaged: Image %q must not use the "latest" tag; pin a version`, s.Image)
	}
	return nil
}

// imageTag extracts the tag from the last path segment of image, so a
// registry host with its own port (e.g. "ghcr.io:5000/example/comfyui:1.4.2")
// is never mistaken for a tag separator.
func imageTag(image string) (tag string, ok bool) {
	last := image
	if slash := strings.LastIndex(image, "/"); slash >= 0 {
		last = image[slash+1:]
	}
	colon := strings.LastIndex(last, ":")
	if colon < 0 {
		return "", false
	}
	return last[colon+1:], true
}

// State is the closed set of container-lifecycle states Status can report.
// It is a type alias for common/comfyuimanagedstatus.State (STATUS.md's P2
// ComfyUI Managed Docker deployment, subtask two) rather than a separate
// package-local enum, so this package's values cross the tunnel without a
// conversion step — the same "State is defined once, in common/" precedent
// modelpull follows by using common/modelpullstatus.State directly. It is a
// subset of the seven states README.md's Managed deployment sketch names
// (pending/installing/starting/ready/degraded/stopped/failed): this package
// never returns "installing" (Start's image pull is logged, not exported as
// a state — see Start's doc comment) or "ready"/"degraded", which would
// require combining this package's container-level view with
// runtime.HealthReport from an already-registered comfyui.Runtime — that
// combination has no consumer yet and stays out of scope (subtask two's
// design doc, known-gaps section).
//
// State 是 Status 能报告的封闭状态集合，是 common/comfyuimanagedstatus.State
// （STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务二）的类型别名，而不是本包
// 另定义的一套枚举——这样本包的取值跨隧道时不需要转换，与 modelpull 直接使用
// common/modelpullstatus.State 是同一种"State 只在 common 里定义一次"的先例。
// 它是 README.md Managed 部署草图七个状态
// （pending/installing/starting/ready/degraded/stopped/failed）的子集：本包从不
// 返回 "installing"（Start 拉取镜像只记日志，不导出成状态，见 Start 的文档注释）或
// "ready"/"degraded"——后两者需要把本包的容器级视图与已注册 comfyui.Runtime 的
// runtime.HealthReport 结合，这个组合目前没有消费方，保持不在范围内（子任务二设计
// 文档的已知缺口一节）。
type State = comfyuimanagedstatus.State

const (
	StatePending  = comfyuimanagedstatus.StatePending  // no container by this name exists yet / 尚不存在同名容器
	StateStarting = comfyuimanagedstatus.StateStarting // container created/restarting, port not yet answering / 容器已创建或正在重启，端口尚未应答
	StateRunning  = comfyuimanagedstatus.StateRunning  // container process is running / 容器进程正在运行
	StateStopped  = comfyuimanagedstatus.StateStopped  // container exited cleanly (exit code 0) / 容器正常退出（退出码 0）
	StateFailed   = comfyuimanagedstatus.StateFailed   // container exited non-zero, or docker itself errored / 容器非零退出，或 docker 本身报错
)

// Launcher manages one Spec's container via the docker CLI. It is the only
// type in this package that shells out. The zero value is not usable; build
// one with NewLauncher.
//
// Launcher 经 docker CLI 管理一个 Spec 对应的容器，是本包里唯一 shell out 的类型。
// 零值不可用，用 NewLauncher 构造。
type Launcher struct {
	clock  runtime.Clock
	logger *slog.Logger
}

// NewLauncher builds a Launcher. A nil clock defaults to the real wall
// clock; a nil logger defaults to slog.Default().
//
// NewLauncher 构造一个 Launcher。clock 为 nil 时用真实系统时钟；logger 为 nil 时
// 用 slog.Default()。
func NewLauncher(clock runtime.Clock, logger *slog.Logger) *Launcher {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Launcher{clock: clock, logger: logger}
}

// Start makes spec's container exist and run. A container already running
// under spec.ContainerName is adopted as-is (idempotent across Agent
// restarts — it never recreates a container just because Start was called
// again). A container that exists but is not running is removed and
// recreated, so the Spec currently in effect always wins over a stale one.
// The image is pulled first if not already present locally; that step is
// logged (slog.Info) but not exposed as a State value, because subtask one
// never has a concurrent caller polling Status while Start runs — see
// State's doc comment. Start returns once the container is created and
// docker reports it running; it does not wait for ComfyUI's HTTP port to
// answer (see WaitReady) or attempt any ComfyUI protocol call.
//
// Start 让 spec 对应的容器存在并运行。已经在以 spec.ContainerName 运行的容器会被
// 原样接管（跨 Agent 重启幂等——绝不会因为再次调用 Start 就重建一个正在运行的容器）。
// 存在但未运行的容器会被移除并重建，确保当前生效的 Spec 始终胜过陈旧配置。镜像本地
// 不存在时先拉取；这一步只记日志（slog.Info），不导出成 State 取值，因为子任务一里
// Start 执行期间从来没有并发调用方在轮询 Status——见 State 的文档注释。Start 在
// docker 报告容器已在运行后即返回；它不等待 ComfyUI 的 HTTP 端口应答（见
// WaitReady），也不发起任何 ComfyUI 协议调用。
func (l *Launcher) Start(ctx context.Context, spec Spec) error {
	if err := spec.validate(); err != nil {
		return err
	}

	state, err := l.Status(ctx, spec.ContainerName)
	if err != nil {
		return err
	}
	switch state {
	case StateRunning:
		l.logger.Info("comfyuimanaged: adopting already-running container", slog.String("container", spec.ContainerName))
		return nil
	case StatePending:
		// nothing to remove before creating
	default:
		l.logger.Info("comfyuimanaged: removing stale container before recreating",
			slog.String("container", spec.ContainerName), slog.String("state", state.String()))
		if _, err := l.runDocker(ctx, "rm", "-f", spec.ContainerName); err != nil {
			return fmt.Errorf("comfyuimanaged: removing stale container %s: %w", spec.ContainerName, err)
		}
	}

	present, err := l.imagePresent(ctx, spec.Image)
	if err != nil {
		return err
	}
	if !present {
		l.logger.Info("comfyuimanaged: pulling image", slog.String("image", spec.Image))
		if _, err := l.runDocker(ctx, "pull", spec.Image); err != nil {
			return fmt.Errorf("comfyuimanaged: pulling image %s: %w", spec.Image, err)
		}
	}

	if _, err := l.runDocker(ctx, buildRunArgs(spec)...); err != nil {
		return fmt.Errorf("comfyuimanaged: starting container %s: %w", spec.ContainerName, err)
	}
	l.logger.Info("comfyuimanaged: container started", slog.String("container", spec.ContainerName), slog.Int("port", spec.Port))
	return nil
}

// Stop stops and removes the named container. Stopping an already-stopped
// or nonexistent container is not an error, so callers never have to check
// Status first.
//
// Stop 停止并移除指定容器。停止一个已经停止或不存在的容器不算错误，调用方无需先查
// Status。
func (l *Launcher) Stop(ctx context.Context, containerName string) error {
	if _, err := l.runDocker(ctx, "stop", containerName); err != nil && !isNotFound(err) {
		return fmt.Errorf("comfyuimanaged: stopping container %s: %w", containerName, err)
	}
	if _, err := l.runDocker(ctx, "rm", containerName); err != nil && !isNotFound(err) {
		return fmt.Errorf("comfyuimanaged: removing container %s: %w", containerName, err)
	}
	return nil
}

// Status reports containerName's current State via `docker inspect`. A
// nonexistent container reports StatePending, not an error — the same
// "declared but not yet realized" reading a Spec gets before Start is ever
// called.
//
// Status 经 docker inspect 报告 containerName 当前的 State。不存在的容器报告
// StatePending 而不是错误——与一个 Spec 在 Start 被调用前"已声明但尚未实现"的解
// 读一致。
func (l *Launcher) Status(ctx context.Context, containerName string) (State, error) {
	out, err := l.runDocker(ctx, "inspect", "--format", "{{.State.Status}}|{{.State.ExitCode}}", containerName)
	if err != nil {
		if isNotFound(err) {
			return StatePending, nil
		}
		return comfyuimanagedstatus.StateUnspecified, err
	}
	status, exitCodeText, ok := strings.Cut(out, "|")
	if !ok {
		return comfyuimanagedstatus.StateUnspecified, fmt.Errorf("comfyuimanaged: unexpected docker inspect output %q", out)
	}
	switch status {
	case "created", "restarting":
		return StateStarting, nil
	case "running":
		return StateRunning, nil
	case "exited", "dead":
		if exitCode, convErr := strconv.Atoi(exitCodeText); convErr == nil && exitCode == 0 {
			return StateStopped, nil
		}
		return StateFailed, nil
	default:
		return comfyuimanagedstatus.StateUnspecified, fmt.Errorf("comfyuimanaged: unrecognized docker container status %q", status)
	}
}

// WaitReady polls 127.0.0.1:spec.Port until a TCP connection succeeds or
// timeout elapses, using the Launcher's injected clock so tests never wait
// on a real timer. It deliberately never speaks HTTP or ComfyUI's own wire
// protocol: that verification belongs to runtime.Manager.Add's existing
// Probe/Discover call, which the caller runs immediately after WaitReady
// succeeds — duplicating that check here would re-implement code that
// already exists and is already tested.
//
// WaitReady 轮询 127.0.0.1:spec.Port 直到一次 TCP 连接成功或超时，使用 Launcher
// 注入的时钟，测试因此从不等待真实定时器。它刻意从不说 HTTP 或 ComfyUI 自己的线上
// 协议：那部分校验属于调用方在 WaitReady 成功后立即执行的既有 runtime.Manager.Add
// 的 Probe/Discover 调用——在这里重复做一遍等于重新实现一份已经存在、已经测试过的
// 代码。
func (l *Launcher) WaitReady(ctx context.Context, spec Spec, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", spec.Port)
	deadline := l.clock.Now().Add(timeout)
	var lastErr error
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		if !l.clock.Now().Before(deadline) {
			return fmt.Errorf("comfyuimanaged: %s did not accept connections within %s: %w", addr, timeout, lastErr)
		}
		ch, stop := l.clock.NewTimer(waitReadyPollInterval)
		select {
		case <-ctx.Done():
			stop()
			return ctx.Err()
		case <-ch:
		}
	}
}

// imagePresent reports whether image already exists in the local docker
// image store, so Start can skip a redundant pull.
func (l *Launcher) imagePresent(ctx context.Context, image string) (bool, error) {
	_, err := l.runDocker(ctx, "image", "inspect", image)
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

// buildRunArgs is a pure function: it never touches the network or a
// process, so its output is easy to assert in tests without a real docker
// daemon. Mount paths are sorted by container path first, so the same Spec
// always produces the same argument list.
func buildRunArgs(spec Spec) []string {
	args := []string{
		"run", "-d",
		"--name", spec.ContainerName,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", spec.Port, comfyUIContainerPort),
	}
	if len(spec.GPUDevices) > 0 {
		args = append(args, "--gpus", "device="+strings.Join(spec.GPUDevices, ","))
	}
	if spec.MemoryLimit != "" {
		args = append(args, "--memory", spec.MemoryLimit)
	}
	args = append(args, mountArgs(spec.ModelPaths, true)...)
	args = append(args, mountArgs(spec.StoragePaths, false)...)
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args
}

// mountArgs renders paths (container path -> host path) as sorted -v flags.
func mountArgs(paths map[string]string, readOnly bool) []string {
	if len(paths) == 0 {
		return nil
	}
	containerPaths := make([]string, 0, len(paths))
	for containerPath := range paths {
		containerPaths = append(containerPaths, containerPath)
	}
	sort.Strings(containerPaths)
	args := make([]string, 0, len(containerPaths)*2)
	for _, containerPath := range containerPaths {
		mount := paths[containerPath] + ":" + containerPath
		if readOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	return args
}

// isNotFound reports whether err is docker's "no such container/image/
// object" answer, which this package treats as a normal outcome
// (StatePending, or "nothing to stop") rather than a failure. The exact
// wording and capitalization differ across docker CLI versions (e.g. "No
// such container" vs. "no such object"), so the match is case-insensitive
// and looks only for the stable "no such" fragment.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such")
}

// runDocker runs the docker CLI with a fixed timeout and returns its
// trimmed stdout. Unlike hostresources.runCommand's zero-argument probes,
// args here are built from this node's own flag-configured Spec (see the
// design doc's dependency-and-security section) rather than being
// unconditionally fixed, but they never come from the Gateway or control
// plane in subtask one.
func (l *Launcher) runDocker(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("comfyuimanaged: docker %s: %s", args[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
