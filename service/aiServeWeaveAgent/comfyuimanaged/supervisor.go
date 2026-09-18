package comfyuimanaged

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/common/runtime"
)

// Supervisor reacts to remotely triggered lifecycle actions
// (STATUS.md's P2 ComfyUI Managed Docker deployment, subtask two) against
// this Agent's one locally-declared Managed ComfyUI Spec. It never accepts a
// Spec from the caller — only a closed Start/Stop/Restart action against the
// Spec it was constructed with — because the Spec's image and mount paths
// must never cross the tunnel in either direction: see this package's and
// tunnel.proto's "no run this command" boundary, and
// common/comfyuimanagedstatus's package doc.
//
// Supervisor 对 Agent 本地已声明的这一个 Managed ComfyUI Spec 响应远程触发的
// 生命周期动作（STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务二）。它从不
// 接受调用方传入的 Spec——只接受针对构造时给定的 Spec 的一个封闭的
// Start/Stop/Restart 动作——因为 Spec 的镜像与挂载路径无论哪个方向都不能跨越
// 隧道：见本包与 tunnel.proto 的"不表达 run this command"边界，以及
// common/comfyuimanagedstatus 的包文档。
type Supervisor struct {
	launcher         *Launcher
	manager          runtime.Manager
	spec             Spec
	waitReadyTimeout time.Duration
	clock            runtime.Clock
	logger           *slog.Logger
	ctx              context.Context // outlives any single Trigger call, for its background goroutine

	mu   sync.Mutex
	busy bool

	// onIdle, when set, is called after each Trigger's background goroutine
	// finishes. It exists only so tests can synchronize on a completed
	// Trigger without a real time.Sleep (AGENTS.md's convention against
	// advancing time with a real sleep applies to this concurrency handoff
	// too, not just simulated timers); it is always nil in production.
	//
	// onIdle 在设置时于每次 Trigger 的后台 goroutine 结束后被调用。它的唯一用
	// 途是让测试无需真实 time.Sleep 就能等到一次 Trigger 完成（AGENTS.md 里
	// "不用真实 time.Sleep 推进时间"的约定同样适用于这种并发交接，不只是模拟
	// 定时器）；生产环境里始终为 nil。
	onIdle func()
}

// NewSupervisor builds a Supervisor for spec. ctx is the Agent's own
// lifetime context — Trigger's background work runs under it, the same way
// modelpull.NewPuller accepts its long-lived ctx at construction rather than
// per call.
//
// NewSupervisor 为 spec 构造一个 Supervisor。ctx 是 Agent 自己的生命周期
// context——Trigger 的后台工作在它之下运行，与 modelpull.NewPuller 在构造时
// 而不是每次调用时接受长生命周期 ctx 是同一种做法。
func NewSupervisor(ctx context.Context, launcher *Launcher, manager runtime.Manager, spec Spec, waitReadyTimeout time.Duration, clock runtime.Clock, logger *slog.Logger) *Supervisor {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{
		launcher:         launcher,
		manager:          manager,
		spec:             spec,
		waitReadyTimeout: waitReadyTimeout,
		clock:            clock,
		logger:           logger,
		ctx:              ctx,
	}
}

// Start brings the container up (adopting an already-running one, same as
// Launcher.Start) and registers it with Manager if it is not already
// registered — the same Start -> WaitReady -> Manager.Add sequence Agent
// boot runs, reused here so it exists exactly once. Manager.Add is skipped
// (not merely tolerated) when the runtime is already registered, since
// Manager.Add itself errors on a duplicate ID.
//
// Start 把容器带起来（与 Launcher.Start 一样会接管已在运行的容器），并在尚未
// 注册时把它注册进 Manager——与 Agent 启动时跑的 Start -> WaitReady ->
// Manager.Add 是同一套顺序，复用它使这段逻辑只存在一份。已经注册时跳过
// Manager.Add（而不只是容忍它失败），因为 Manager.Add 本身对重复 ID 会报错。
func (s *Supervisor) Start(ctx context.Context) error {
	if err := s.launcher.Start(ctx, s.spec); err != nil {
		return err
	}
	if err := s.launcher.WaitReady(ctx, s.spec, s.waitReadyTimeout); err != nil {
		return err
	}
	if _, already := s.manager.Get(s.spec.ContainerName); already {
		return nil
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", s.spec.Port)
	if err := s.manager.Add(ctx, runtime.Config{ID: s.spec.ContainerName, Kind: runtime.KindComfyUI, BaseURL: baseURL}); err != nil {
		return fmt.Errorf("comfyui managed: registering %s: %w", baseURL, err)
	}
	return nil
}

// Stop deregisters the runtime from Manager first, so the scheduler stops
// dispatching new requests to it before the container is torn down, then
// stops and removes the container. Deregistering an instance that was never
// registered is not an error, matching Manager.Remove's own tolerance for an
// unknown ID.
//
// Stop 先取消 Manager 里的运行时注册,让调度器在容器被拆除前先停止向它派发新
// 请求,再停止并移除容器。取消注册一个从未注册过的实例不算错误，与
// Manager.Remove 本身对未知 ID 的容忍度一致。
func (s *Supervisor) Stop(ctx context.Context) error {
	if err := s.manager.Remove(ctx, s.spec.ContainerName); err != nil {
		s.logger.Warn("comfyui managed: deregistering before stop", slog.String("container", s.spec.ContainerName), slog.Any("error", err))
	}
	return s.launcher.Stop(ctx, s.spec.ContainerName)
}

// Trigger runs action in the background under the Supervisor's own
// long-lived context, so a slow Docker pull or WaitReady poll never blocks
// the tunnel Control session's frame loop. A Trigger received while another
// is still in flight is dropped with a log line, not queued: there is only
// one container, so a queued Stop behind an in-flight Start (or vice versa)
// would just replay the same non-composable race Docker itself would
// refuse.
//
// Trigger 在 Supervisor 自己的长生命周期 context 下后台执行 action，这样一次
// 缓慢的 Docker 拉取或 WaitReady 轮询就不会阻塞隧道 Control 会话的帧循环。收到
// 一个 Trigger 时如果已有另一个动作在途，直接丢弃并记日志，不排队——只有一个
// 容器，把 Stop 排在一个在途 Start 之后（或反过来）只是在重演 Docker 自己都会
// 拒绝的同一种不可组合的竞争。
func (s *Supervisor) Trigger(action comfyuimanagedstatus.Action) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		s.logger.Warn("comfyui managed: dropping action, another action is already in flight",
			slog.String("action", action.String()))
		return
	}
	s.busy = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			onIdle := s.onIdle
			s.mu.Unlock()
			if onIdle != nil {
				onIdle()
			}
		}()

		var err error
		switch action {
		case comfyuimanagedstatus.ActionStart:
			err = s.Start(s.ctx)
		case comfyuimanagedstatus.ActionStop:
			err = s.Stop(s.ctx)
		case comfyuimanagedstatus.ActionRestart:
			if err = s.Stop(s.ctx); err == nil {
				err = s.Start(s.ctx)
			}
		default:
			s.logger.Warn("comfyui managed: ignoring unrecognized action", slog.String("action", action.String()))
			return
		}
		if err != nil {
			s.logger.Error("comfyui managed: action failed",
				slog.String("action", action.String()), slog.String("container", s.spec.ContainerName), slog.Any("error", err))
		}
	}()
}

// Snapshot reports the container's current state via a fresh docker inspect
// (Launcher.Status), matching Puller.Snapshot's "always current, poll-based"
// contract that the tunnel Control session's throttled reporting relies on.
// It always returns exactly one entry, since a Supervisor exists only when
// Managed mode is configured.
//
// Snapshot 经一次实时的 docker inspect（Launcher.Status）报告容器当前状态，与
// Puller.Snapshot"始终反映当下、靠轮询获取"的约定一致，隧道 Control 会话的
// 节流上报依赖这一点。它总是恰好返回一个条目，因为只有配置了 Managed 模式时
// Supervisor 才会存在。
func (s *Supervisor) Snapshot() []comfyuimanagedstatus.Status {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()

	state, err := s.launcher.Status(ctx, s.spec.ContainerName)
	now := s.clock.Now()
	if err != nil {
		s.logger.Warn("comfyui managed: status check failed", slog.String("container", s.spec.ContainerName), slog.Any("error", err))
		state = comfyuimanagedstatus.StateUnspecified
	}
	return []comfyuimanagedstatus.Status{{
		ContainerName: s.spec.ContainerName,
		State:         state,
		UpdatedAt:     now,
	}}
}
