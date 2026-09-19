package tunnel

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// This file is the Control stream itself: the heartbeat, the runtime status
// reports, the configuration the replica pushes down, the roster it
// broadcasts, and the graceful drain. It runs entirely in one goroutine —
// the loop in runControl — plus one reader goroutine, so nothing here needs
// a send lock: a gRPC stream tolerates exactly one concurrent sender, and
// there is exactly one.
//
// The Control stream is also this replica's liveness verdict. If it breaks,
// the replica drops the node from its scheduling candidates without waiting
// for any data slot to time out; the other replicas are unaffected, because
// they have their own streams and their own Clients.

// runControl runs the Control stream until it breaks, the replica asks for
// shutdown, or ctx is cancelled and the drain completes.
func (c *Client) runControl(ctx context.Context, stream ControlStream, reader *controlReader) error {
	sess := &controlSession{client: c, stream: stream, reported: map[string][]byte{}}

	// The replica gets the full picture immediately rather than up to a
	// minute later, so it can schedule to this node from the first heartbeat.
	if err := sess.report(true); err != nil {
		return c.streamError("initial status report", err)
	}
	if err := sess.forceReportModelPull(); err != nil {
		return c.streamError("initial model pull report", err)
	}
	if err := sess.forceReportComfyUIManaged(); err != nil {
		return c.streamError("initial comfyui managed report", err)
	}

	heartbeat := newRearmingTimer(c.clock, c.cfg.HeartbeatInterval)
	defer heartbeat.stop()
	statusPoll := newRearmingTimer(c.clock, c.cfg.StatusPollInterval)
	defer statusPoll.stop()
	statusFull := newRearmingTimer(c.clock, c.cfg.StatusFullInterval)
	defer statusFull.stop()

	for {
		select {
		case <-ctx.Done():
			return c.drain(stream, "agent shutting down", c.cfg.DrainTimeout, nil)

		case err := <-reader.errs:
			return c.streamError("recv", err)

		case frame := <-reader.frames:
			if err := sess.handle(ctx, frame); err != nil {
				return err
			}

		case <-heartbeat.C():
			if err := sess.beat(); err != nil {
				return err
			}
			heartbeat.arm()

		case <-statusPoll.C():
			if err := sess.report(false); err != nil {
				return c.streamError("status report", err)
			}
			if err := sess.reportModelPull(false); err != nil {
				return c.streamError("model pull report", err)
			}
			if err := sess.reportComfyUIManaged(false); err != nil {
				return c.streamError("comfyui managed report", err)
			}
			statusPoll.arm()

		case <-statusFull.C():
			if err := sess.report(true); err != nil {
				return c.streamError("status report", err)
			}
			statusFull.arm()
		}
	}
}

// controlSession is the mutable state of one Control stream.
type controlSession struct {
	client *Client
	stream ControlStream

	// unacked counts heartbeats sent since the last HeartbeatAck.
	unacked int
	// reported maps runtime id to the material part of the last reported
	// snapshot, so a report is only sent when something actually changed.
	reported map[string][]byte
	// reportedModelPull is the deterministic encoding of the last
	// ModelPullReport sent, so reportModelPull only sends again once
	// something in it actually changed (STATUS.md's P2 model distribution
	// subtask 2). nil before the first report, which always differs from
	// any real encoding and so is always sent.
	reportedModelPull []byte
	// reportedComfyUIManaged is reportedModelPull's counterpart for
	// ComfyUIManagedReport (STATUS.md's P2 ComfyUI Managed Docker
	// deployment, subtask 2).
	reportedComfyUIManaged []byte
}

// handle dispatches one frame from the replica.
func (s *controlSession) handle(ctx context.Context, frame *tunnelv1.GatewayControl) error {
	c := s.client
	switch body := frame.GetBody().(type) {
	case *tunnelv1.GatewayControl_HbAck:
		s.ack(body.HbAck)
		return nil

	case *tunnelv1.GatewayControl_Ping:
		return s.send(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Pong{
			Pong: &tunnelv1.Pong{SentUnixMs: body.Ping.GetSentUnixMs()},
		}}, "pong")

	case *tunnelv1.GatewayControl_Config:
		s.applyConfig(ctx, body.Config)
		// Success or failure is observed through the status report rather
		// than a dedicated ack frame, so one always follows.
		return s.forceReport()

	case *tunnelv1.GatewayControl_ModelPullTrigger:
		if c.cfg.ModelPuller != nil {
			c.cfg.ModelPuller.Trigger(tunnelwire.ModelPullTriggerFromProto(body.ModelPullTrigger))
		}
		// Same rule as GatewayControl_Config: no dedicated ack, the next
		// status report (forced here) is how the Gateway observes an
		// unknown name being rejected or a known one starting to move.
		return s.forceReportModelPull()

	case *tunnelv1.GatewayControl_ComfyuiManagedAction:
		if c.cfg.ComfyUIManaged != nil {
			c.cfg.ComfyUIManaged.Trigger(tunnelwire.ComfyUIManagedActionFromProto(body.ComfyuiManagedAction))
		}
		// Same rule as GatewayControl_ModelPullTrigger: no dedicated ack,
		// the next report (forced here) is how the Gateway observes the
		// action taking effect.
		return s.forceReportComfyUIManaged()

	case *tunnelv1.GatewayControl_ComfyuiManagedCustomNodeInstall:
		if c.cfg.ComfyUIManaged != nil {
			c.cfg.ComfyUIManaged.TriggerCustomNodeInstall(tunnelwire.ComfyUIManagedCustomNodeInstallTriggerFromProto(body.ComfyuiManagedCustomNodeInstall))
		}
		// Same rule as GatewayControl_ComfyuiManagedAction: no dedicated
		// ack, the next report (forced here) is how the Gateway observes
		// the install taking effect.
		return s.forceReportComfyUIManaged()

	case *tunnelv1.GatewayControl_Roster:
		if c.cfg.OnRoster != nil {
			c.cfg.OnRoster(body.Roster)
		}
		return nil

	case *tunnelv1.GatewayControl_SlotHint:
		// The hint is advice; the pool clamps it to the locally configured
		// limits before acting on it.
		if pool := c.currentPool(); pool != nil {
			pool.ApplyHint(body.SlotHint)
		}
		if c.cfg.OnSlotHint != nil {
			c.cfg.OnSlotHint(body.SlotHint)
		}
		return nil

	case *tunnelv1.GatewayControl_Shutdown:
		grace := body.Shutdown.GetGracePeriod().AsDuration()
		if grace <= 0 || grace > c.cfg.DrainTimeout {
			grace = c.cfg.DrainTimeout
		}
		c.logger.Info("gateway requested shutdown; draining",
			slog.String("reason", body.Shutdown.GetReason()),
			slog.Duration("grace", grace))
		return c.drain(s.stream, body.Shutdown.GetReason(), grace, ErrShutdownRequested)

	case *tunnelv1.GatewayControl_Ack:
		// A second HelloAck is harmless but means the replica is confused
		// about the stream's state; note it and carry on.
		c.logger.Warn("gateway sent a duplicate HelloAck")
		return nil

	default:
		// Unknown frames are ignored on purpose: a newer Gateway must be able
		// to add control frames without breaking older Agents.
		c.logger.Debug("ignoring unknown control frame", slog.String("frame", controlFrameName(frame)))
		return nil
	}
}

// beat sends one heartbeat, or declares the replica dead once
// HeartbeatFailureThreshold heartbeats have gone unanswered. Dying here is
// what makes the Control stream the liveness verdict: the run loop reconnects
// with backoff and the replica drops this node in the meantime.
func (s *controlSession) beat() error {
	c := s.client
	if s.unacked >= c.cfg.HeartbeatFailureThreshold {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorConnection,
			Operation: clientOperation,
			Message:   "gateway did not answer the last " + strconv.Itoa(s.unacked) + " heartbeats",
			Retryable: true,
		}
	}

	frame := &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{Heartbeat: &tunnelv1.Heartbeat{
		SentUnixMs:       c.clock.Now().UnixMilli(),
		InflightRequests: int32(c.inFlight()),
	}}}
	if err := s.send(frame, "heartbeat"); err != nil {
		return err
	}
	s.unacked++
	return nil
}

// ack records the round trip of an answered heartbeat and clears the miss
// counter.
func (s *controlSession) ack(ack *tunnelv1.HeartbeatAck) {
	c := s.client
	s.unacked = 0
	if sent := ack.GetSentUnixMs(); sent > 0 {
		rtt := max(c.clock.Now().Sub(time.UnixMilli(sent)), 0)
		c.mu.Lock()
		c.rtt = rtt
		rec := c.metrics
		c.mu.Unlock()
		rec.HeartbeatRTT(rtt)
	}
}

// report sends a RuntimeStatus. A full report carries every instance and is
// the 60s reconciliation; a partial one carries only what changed since the
// last report and is sent as soon as a change is observed. Nothing is sent
// when a partial report would be empty, which is the common case.
//
// A change to the set of instances always forces a full report: a removal
// cannot be expressed by sending a subset.
func (s *controlSession) report(full bool) error {
	snaps := s.client.cfg.Manager.Snapshot()
	keys := make(map[string][]byte, len(snaps))
	changed := make([]*tunnelv1.RuntimeSnapshot, 0, len(snaps))
	all := make([]*tunnelv1.RuntimeSnapshot, 0, len(snaps))

	setChanged := len(snaps) != len(s.reported)
	for _, snap := range snaps {
		pb := tunnelwire.SnapshotToProto(snap)
		all = append(all, pb)

		key, err := materialSnapshotKey(pb)
		if err != nil {
			return err
		}
		id := snap.Descriptor.ID
		keys[id] = key
		prev, seen := s.reported[id]
		if !seen {
			setChanged = true
		}
		if !seen || !bytes.Equal(prev, key) {
			changed = append(changed, pb)
		}
	}

	switch {
	case full || setChanged:
		if err := s.sendStatus(all, true); err != nil {
			return err
		}
	case len(changed) > 0:
		if err := s.sendStatus(changed, false); err != nil {
			return err
		}
	default:
		return nil
	}
	s.reported = keys
	return nil
}

// forceReport sends a full report regardless of what changed, which is how a
// configuration change is acknowledged.
func (s *controlSession) forceReport() error {
	s.reported = map[string][]byte{}
	return s.report(true)
}

func (s *controlSession) sendStatus(snaps []*tunnelv1.RuntimeSnapshot, full bool) error {
	return s.send(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Snapshots:  snaps,
		Full:       full,
		ReportedAt: timestamppb.New(s.client.clock.Now()),
	}}}, "runtime status")
}

// reportModelPull sends a ModelPullReport when the ModelPuller's snapshot has
// changed since the last one sent (or force is true). Unlike report/
// sendStatus there is no full/partial split: a manifest is operator-authored
// and small, so the whole known set is always sent, and "changed" is decided
// by comparing a deterministic encoding of that whole set rather than
// tracking each name's material part separately — the complexity
// materialSnapshotKey needs to stay a busy loop free of noisy timestamp
// churn does not pay for itself at this size (STATUS.md's P2 model
// distribution subtask 2).
//
// reportModelPull 在 ModelPuller 的快照自上次发送后发生变化（或 force 为
// true）时发送一次 ModelPullReport。与 report/sendStatus 不同，这里不做
// full/partial 拆分：清单是运维手写的、条目数量有限，所以总是发送完整的
// 已知集合，"是否变化"靠比较整个集合的确定性编码，而不是像 materialSnapshotKey
// 那样逐条追踪——那份复杂度是为了避免时间戳噪声造成忙循环，在这个规模上不
// 值得（STATUS.md P2 模型分发子任务二）。
func (s *controlSession) reportModelPull(force bool) error {
	puller := s.client.cfg.ModelPuller
	if puller == nil {
		return nil
	}
	report := tunnelwire.ModelPullReportToProto(puller.Snapshot())
	key, err := proto.MarshalOptions{Deterministic: true}.Marshal(report)
	if err != nil {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			Operation: clientOperation,
			Message:   "cannot encode a model pull report",
			Cause:     err,
		}
	}
	if !force && bytes.Equal(key, s.reportedModelPull) {
		return nil
	}
	if err := s.send(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ModelPull{ModelPull: report}}, "model pull report"); err != nil {
		return err
	}
	s.reportedModelPull = key
	return nil
}

// forceReportModelPull sends a ModelPullReport regardless of what changed,
// the ModelPuller counterpart to forceReport.
func (s *controlSession) forceReportModelPull() error {
	return s.reportModelPull(true)
}

// reportComfyUIManaged sends a ComfyUIManagedReport when the Supervisor's
// snapshot has changed since the last one sent (or force is true) —
// reportModelPull's counterpart for STATUS.md's P2 ComfyUI Managed Docker
// deployment, subtask 2. There is always at most one entry (an Agent
// manages at most one Managed instance today), so the same
// "whole-set deterministic encoding" comparison reportModelPull uses applies
// unchanged.
//
// reportComfyUIManaged 在 Supervisor 的快照自上次发送后发生变化（或 force 为
// true）时发送一次 ComfyUIManagedReport——是 reportModelPull 在 STATUS.md P2
// ComfyUI Managed Docker 部署子任务二里的对应物。今天一个 Agent 最多管理一个
// Managed 实例，因此总是最多一个条目，reportModelPull 那套"整个集合的确定性
// 编码"比较方式原样适用。
func (s *controlSession) reportComfyUIManaged(force bool) error {
	manager := s.client.cfg.ComfyUIManaged
	if manager == nil {
		return nil
	}
	report := tunnelwire.ComfyUIManagedReportToProto(manager.Snapshot())
	key, err := proto.MarshalOptions{Deterministic: true}.Marshal(report)
	if err != nil {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			Operation: clientOperation,
			Message:   "cannot encode a comfyui managed report",
			Cause:     err,
		}
	}
	if !force && bytes.Equal(key, s.reportedComfyUIManaged) {
		return nil
	}
	if err := s.send(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ComfyuiManaged{ComfyuiManaged: report}}, "comfyui managed report"); err != nil {
		return err
	}
	s.reportedComfyUIManaged = key
	return nil
}

// forceReportComfyUIManaged sends a ComfyUIManagedReport regardless of what
// changed, the Supervisor counterpart to forceReportModelPull.
func (s *controlSession) forceReportComfyUIManaged() error {
	return s.reportComfyUIManaged(true)
}

// applyConfig installs one control-plane configuration change. Failures are
// logged and left to surface in the following status report; they never break
// the stream, because a bad configuration is the control plane's problem, not
// a reason to drop a healthy tunnel.
func (s *controlSession) applyConfig(ctx context.Context, pb *tunnelv1.RuntimeConfig) {
	c := s.client
	id := pb.GetRuntimeId()
	log := c.logger.With(slog.String("runtime_id", id), slog.String("action", pb.GetAction().String()))

	switch pb.GetAction() {
	case tunnelv1.ConfigAction_CONFIG_ACTION_REMOVE:
		// Removal names no address, so it needs no allowlist check.
		if err := c.cfg.Manager.Remove(ctx, id); err != nil {
			log.Error("control-plane runtime removal failed", slog.String("error", err.Error()))
			return
		}
		log.Info("runtime removed by the control plane")

	case tunnelv1.ConfigAction_CONFIG_ACTION_ADD, tunnelv1.ConfigAction_CONFIG_ACTION_REPLACE:
		cfg, apiKeyRef := tunnelwire.ConfigFromProto(pb.GetSpec())
		if cfg.ID == "" {
			cfg.ID = id
		}
		if !c.runtimeAllowed(cfg.ID) {
			// The last line of defence: a compromised Gateway must not be
			// able to point this Agent at an address the operator never
			// declared.
			log.Error("refusing a control-plane runtime outside the local allowlist")
			return
		}
		if apiKeyRef != "" {
			key, err := c.resolveSecret(ctx, apiKeyRef)
			if err != nil {
				log.Error("cannot resolve the runtime credential", slog.String("error", err.Error()))
				return
			}
			cfg.APIKey = key
		}

		apply := c.cfg.Manager.Add
		if pb.GetAction() == tunnelv1.ConfigAction_CONFIG_ACTION_REPLACE {
			apply = c.cfg.Manager.Replace
		}
		if err := apply(ctx, cfg); err != nil {
			log.Error("control-plane runtime configuration failed", slog.String("error", err.Error()))
			return
		}
		log.Info("runtime configured by the control plane")

	default:
		log.Error("ignoring a runtime configuration with no action")
	}
}

// send writes one frame, tagging a failure with what was being sent.
func (s *controlSession) send(frame *tunnelv1.AgentControl, what string) error {
	if err := s.stream.Send(frame); err != nil {
		return s.client.streamError("send "+what, err)
	}
	return nil
}

// -----------------------------------------------------------------------
// Draining
// -----------------------------------------------------------------------

// drain performs the graceful shutdown of one tunnel: tell the replica to
// stop dispatching, half-close so it knows nothing more is coming, then give
// in-flight requests up to grace to finish. It returns cause unchanged so the
// caller distinguishes "the Agent is stopping" (nil) from "the replica asked
// us to leave" (ErrShutdownRequested).
func (c *Client) drain(stream ControlStream, reason string, grace time.Duration, cause error) error {
	c.setState(StateDraining)

	deadline := c.clock.Now().Add(grace)
	draining := &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Draining{Draining: &tunnelv1.Draining{
		Reason:         reason,
		DeadlineUnixMs: deadline.UnixMilli(),
	}}}
	if err := stream.Send(draining); err != nil {
		// The stream is already gone: there is nobody left to drain towards,
		// which is not a failure of the shutdown itself.
		c.logger.Debug("could not announce draining", slog.String("error", err.Error()))
		return cause
	}
	if err := stream.CloseSend(); err != nil {
		c.logger.Debug("could not half-close the control stream", slog.String("error", err.Error()))
	}

	c.waitInFlight(deadline)
	c.logger.Info("tunnel drained", slog.String("reason", reason))
	return cause
}

// waitInFlight blocks until no request is running or the deadline passes.
func (c *Client) waitInFlight(deadline time.Time) {
	for c.inFlight() > 0 {
		remaining := deadline.Sub(c.clock.Now())
		if remaining <= 0 {
			c.logger.Warn("draining deadline reached with requests still in flight",
				slog.Int("inflight", c.inFlight()))
			return
		}
		wait := min(c.cfg.StatusPollInterval, remaining)
		timer, stop := c.clock.NewTimer(wait)
		<-timer
		stop()
	}
}

// inFlight counts the requests running on this tunnel: the slot pool's own
// tally, unless the caller installed a counter of its own.
func (c *Client) inFlight() int {
	if c.cfg.InFlight != nil {
		return c.cfg.InFlight()
	}
	if pool := c.currentPool(); pool != nil {
		return pool.InFlight()
	}
	return 0
}

// runtimeAllowed reports whether id is in the local allowlist. An empty
// allowlist means the caller applied no narrowing, per ClientConfig.
func (c *Client) runtimeAllowed(id string) bool {
	if id == "" {
		return false
	}
	if len(c.cfg.AllowedRuntimes) == 0 {
		return true
	}
	return slices.Contains(c.cfg.AllowedRuntimes, id)
}

// resolveSecret turns an api_key_ref into a credential. Neither the reference
// nor the credential is ever logged.
func (c *Client) resolveSecret(ctx context.Context, ref string) (string, error) {
	if c.cfg.Secrets == nil {
		return "", &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			Operation: clientOperation,
			Message:   "the control plane named a secret but no secret resolver is configured",
		}
	}
	key, err := c.cfg.Secrets.Resolve(ctx, ref)
	if err != nil {
		return "", &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			Operation: clientOperation,
			Message:   "cannot resolve the named secret",
			Cause:     err,
		}
	}
	return key, nil
}

// -----------------------------------------------------------------------
// Stream reader
// -----------------------------------------------------------------------

// controlReader turns the blocking Recv of a stream into channels the control
// loop can select on. It owns exactly one goroutine, which ends when the
// stream fails or the stream context is cancelled — the caller cancels that
// context before calling wait, so no reader outlives its connection attempt.
type controlReader struct {
	frames chan *tunnelv1.GatewayControl
	errs   chan error
	done   chan struct{}
}

func startControlReader(ctx context.Context, stream ControlStream) *controlReader {
	r := &controlReader{
		frames: make(chan *tunnelv1.GatewayControl),
		errs:   make(chan error, 1),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(r.done)
		for {
			frame, err := stream.Recv()
			if err != nil {
				select {
				case r.errs <- err:
				case <-ctx.Done():
				}
				return
			}
			select {
			case r.frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	return r
}

// wait blocks until the reader goroutine has exited.
func (r *controlReader) wait() { <-r.done }

// -----------------------------------------------------------------------
// Timers
// -----------------------------------------------------------------------

// rearmingTimer is a periodic timer built from runtime.Clock, which has no
// ticker. It is armed on creation and re-armed by the loop after each fire,
// so a slow iteration delays the next tick instead of queueing ticks up.
type rearmingTimer struct {
	clock    runtime.Clock
	interval time.Duration
	ch       <-chan time.Time
	stopFn   func() bool
}

func newRearmingTimer(clock runtime.Clock, interval time.Duration) *rearmingTimer {
	t := &rearmingTimer{clock: clock, interval: interval}
	t.arm()
	return t
}

func (t *rearmingTimer) C() <-chan time.Time { return t.ch }

func (t *rearmingTimer) arm() {
	t.ch, t.stopFn = t.clock.NewTimer(t.interval)
}

func (t *rearmingTimer) stop() {
	if t.stopFn != nil {
		t.stopFn()
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// materialSnapshotKey renders the part of a snapshot worth reporting a change
// for. Timestamps and the health-check latency are cleared first: they move
// on every health check even when nothing about the runtime changed, and
// reporting on them would turn the change-triggered report into a busy loop.
func materialSnapshotKey(pb *tunnelv1.RuntimeSnapshot) ([]byte, error) {
	clone, ok := proto.Clone(pb).(*tunnelv1.RuntimeSnapshot)
	if !ok {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			Operation: clientOperation,
			Message:   "cannot clone a runtime snapshot",
		}
	}
	clone.UpdatedAt = nil
	if clone.Probe != nil {
		clone.Probe.ProbedAt = nil
	}
	if clone.Health != nil {
		clone.Health.CheckedAt = nil
		clone.Health.Latency = nil
	}
	if clone.Discovery != nil {
		clone.Discovery.DiscoveredAt = nil
	}

	key, err := proto.MarshalOptions{Deterministic: true}.Marshal(clone)
	if err != nil {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			Operation: clientOperation,
			Message:   "cannot encode a runtime snapshot",
			Cause:     err,
		}
	}
	return key, nil
}

// controlFrameName renders a Gateway frame's body for a log line, without
// touching its contents.
func controlFrameName(frame *tunnelv1.GatewayControl) string {
	switch frame.GetBody().(type) {
	case *tunnelv1.GatewayControl_Ack:
		return "HelloAck"
	case *tunnelv1.GatewayControl_HbAck:
		return "HeartbeatAck"
	case *tunnelv1.GatewayControl_Config:
		return "RuntimeConfig"
	case *tunnelv1.GatewayControl_SlotHint:
		return "SlotHint"
	case *tunnelv1.GatewayControl_Roster:
		return "GatewayRoster"
	case *tunnelv1.GatewayControl_Shutdown:
		return "Shutdown"
	case *tunnelv1.GatewayControl_Ping:
		return "Ping"
	case *tunnelv1.GatewayControl_ModelPullTrigger:
		return "ModelPullTrigger"
	case nil:
		return "empty frame"
	default:
		return "unknown frame"
	}
}
