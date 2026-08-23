package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"AIServeWeave/common/runtime"
)

const (
	// maxNodeTypes bounds how many node type names one Discover keeps. A
	// stock ComfyUI reports a few hundred; a heavily modded one can report
	// thousands, and the list is only used for scheduling decisions.
	maxNodeTypes = 5000

	// maxModelFiles bounds how many model files one Discover keeps across
	// all folders, for the same reason.
	maxModelFiles = 5000

	// modelFolderConcurrency bounds how many /models/{folder} requests
	// Discover issues at once, so a server with many folders is not hit
	// with a burst.
	modelFolderConcurrency = 4
)

// Runtime is the ComfyUI adapter. It is safe for concurrent use.
//
// Unlike the LLM adapters it implements runtime.WorkflowRuntime: ComfyUI
// executes asynchronous graphs, not synchronous completions, so forcing it
// behind the inference interface would mean inventing a request/response
// shape the backend does not have.
type Runtime struct {
	cfg    runtime.Config
	logger *slog.Logger
	clock  runtime.Clock

	client  *Client
	limiter *runtime.Limiter
	events  *eventMux

	discovery atomic.Pointer[runtime.Discovery]
	closed    atomic.Bool

	// submitted maps an upper-layer idempotency key to the prompt id it
	// produced, so a retried Submit returns the original run instead of
	// queueing the same workflow twice. ComfyUI has no idempotency of its
	// own, and a duplicate submission costs real GPU time and produces a
	// second set of outputs.
	submitMu  sync.Mutex
	submitted map[string]runtime.WorkflowRun
}

// Compile-time proof that the adapter satisfies the workflow contract.
var _ runtime.WorkflowRuntime = (*Runtime)(nil)

// New builds a ComfyUI Runtime for a normalized, validated Config. It
// performs no network I/O; Manager owns Probe, Discover and scheduling.
//
// New has the signature of runtime.Factory, so it can be registered with
// runtime.Registry.Register(runtime.KindComfyUI, comfyui.New).
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	if cfg.Kind == "" {
		cfg.Kind = runtime.KindComfyUI
	}
	if cfg.Kind != runtime.KindComfyUI {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: cfg.ID,
			Kind:      cfg.Kind,
			Operation: "create_runtime",
			Message:   fmt.Sprintf("the comfyui adapter cannot serve kind %q", cfg.Kind),
		}
	}
	if deps.WSDialer == nil {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: cfg.ID,
			Kind:      runtime.KindComfyUI,
			Operation: "create_runtime",
			Message:   "a WSDialer is required: ComfyUI reports progress only over its WebSocket",
		}
	}

	client, err := NewClient(ClientConfig{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Headers:    cfg.Headers,
		HTTPClient: deps.HTTPClient,
		RuntimeID:  cfg.ID,
	})
	if err != nil {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: cfg.ID,
			Kind:      runtime.KindComfyUI,
			Operation: "create_runtime",
			Message:   err.Error(),
			Cause:     err,
		}
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clock := deps.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}

	return &Runtime{
		cfg:       cfg,
		logger:    logger,
		clock:     clock,
		client:    client,
		limiter:   runtime.NewLimiter(cfg.MaxConcurrent),
		events:    newEventMux(client, deps.WSDialer, clock, logger, newClientID()),
		submitted: make(map[string]runtime.WorkflowRun),
	}, nil
}

// Descriptor reports the instance's stable identity and scheduling summary.
func (r *Runtime) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{
		ID:            r.cfg.ID,
		Kind:          runtime.KindComfyUI,
		BaseURL:       r.cfg.BaseURL,
		MaxConcurrent: r.cfg.MaxConcurrent,
		Exclusive:     r.cfg.Exclusive,
	}
}

// Probe verifies the endpoint is a ComfyUI server via GET /system_stats,
// which reports the running version and devices without touching the queue.
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if err := r.checkOpen("probe"); err != nil {
		return runtime.ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	defer cancel()

	stats, err := r.client.SystemStats(ctx, "probe")
	if err != nil {
		return runtime.ProbeResult{}, r.asProbeMismatch(err, "probe", pathSystemStats)
	}
	if len(stats.Devices) == 0 && stats.System.ComfyUIVersion == "" && stats.System.PythonVersion == "" {
		// A 200 with none of these fields is some other service answering
		// on this path, not a ComfyUI server.
		return runtime.ProbeResult{}, &runtime.RuntimeError{
			Code:      runtime.ErrorProbeMismatch,
			RuntimeID: r.cfg.ID,
			Kind:      runtime.KindComfyUI,
			Operation: "probe",
			Message:   fmt.Sprintf("%s answered without any ComfyUI system or device fields", pathSystemStats),
		}
	}

	return runtime.ProbeResult{
		Kind:             runtime.KindComfyUI,
		Version:          stats.System.ComfyUIVersion,
		IdentityVerified: true,
		Evidence: fmt.Sprintf("GET %s reported ComfyUI version %s on %d device(s)",
			pathSystemStats, versionOrUnknown(stats.System.ComfyUIVersion), len(stats.Devices)),
		ProbedAt: r.clock.Now(),
	}, nil
}

// Health calls GET /system_stats, which answers from process state without
// queueing work, so a server busy rendering still reports promptly.
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	if err := r.checkOpen("health"); err != nil {
		return runtime.HealthReport{State: runtime.StateClosed, CheckedAt: r.clock.Now()}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	defer cancel()

	start := r.clock.Now()
	_, err := r.client.SystemStats(ctx, "health")
	checkedAt := r.clock.Now()
	if err != nil {
		return runtime.HealthReport{
			State:        runtime.StateUnhealthy,
			Latency:      checkedAt.Sub(start),
			CheckedAt:    checkedAt,
			ErrorSummary: errorSummary(err),
		}, err
	}
	return runtime.HealthReport{
		State:     runtime.StateHealthy,
		Latency:   checkedAt.Sub(start),
		CheckedAt: checkedAt,
	}, nil
}

// Discover reports the running version, the node types the server can
// execute and the model files it can load. Only /system_stats is required:
// the rest are newer routes that older builds do not serve, and a missing
// one produces a warning rather than an unusable instance.
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.checkOpen("discover"); err != nil {
		return runtime.Discovery{}, err
	}

	stats, err := r.client.SystemStats(ctx, "discover")
	if err != nil {
		return runtime.Discovery{}, err
	}

	var warnings []string
	nodeTypes, nodeWarnings := r.discoverNodeTypes(ctx)
	warnings = append(warnings, nodeWarnings...)

	models, modelWarnings := r.discoverModels(ctx)
	warnings = append(warnings, modelWarnings...)

	if _, err := r.client.Features(ctx); err != nil {
		// /features is informational and absent on older builds; it must
		// not decide whether an instance is usable.
		warnings = append(warnings, fmt.Sprintf("optional %s was not usable: %s", pathFeatures, errorSummary(err)))
	}

	caps := runtime.Merge(endpointCapabilities(), r.overrideCapabilities())
	warnings = append(warnings, conflictWarnings(caps)...)

	discovery := runtime.Discovery{
		Version:      stats.System.ComfyUIVersion,
		Models:       models,
		NodeTypes:    nodeTypes,
		Capabilities: caps,
		Warnings:     warnings,
		DiscoveredAt: r.clock.Now(),
	}
	r.discovery.Store(&discovery)
	return discovery, nil
}

func (r *Runtime) discoverNodeTypes(ctx context.Context) ([]string, []string) {
	keys, truncated, err := r.client.ObjectInfoKeys(ctx, maxNodeTypes)
	if err != nil {
		return nil, []string{fmt.Sprintf("optional %s was not usable: %s", pathObjectInfo, errorSummary(err))}
	}
	sort.Strings(keys)
	if truncated {
		return keys, []string{fmt.Sprintf("%s reported more than %d node types; the list was truncated", pathObjectInfo, maxNodeTypes)}
	}
	return keys, nil
}

// discoverModels lists each model folder's files, bounded in both fan-out
// and total size. Capabilities are left unknown: a checkpoint file's name
// says nothing about what it can do, and guessing from it is exactly the
// inference this package refuses to make.
func (r *Runtime) discoverModels(ctx context.Context) ([]runtime.Model, []string) {
	folders, err := r.client.ModelFolders(ctx)
	if err != nil {
		return nil, []string{fmt.Sprintf("optional %s was not usable: %s", pathModels, errorSummary(err))}
	}

	var (
		mu       sync.Mutex
		models   []runtime.Model
		warnings []string
		wg       sync.WaitGroup
		sem      = make(chan struct{}, modelFolderConcurrency)
	)
	for _, folder := range folders {
		wg.Add(1)
		go func(folder string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			files, err := r.client.ModelFiles(ctx, folder)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("model folder %q could not be listed: %s", folder, errorSummary(err)))
				return
			}
			for _, file := range files {
				models = append(models, runtime.Model{ID: folder + "/" + file})
			}
		}(folder)
	}
	wg.Wait()

	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	sort.Strings(warnings)
	if len(models) > maxModelFiles {
		models = models[:maxModelFiles]
		warnings = append(warnings, fmt.Sprintf("more than %d model files were reported; the list was truncated", maxModelFiles))
	}
	return models, warnings
}

// Submit queues an API Format workflow. The event stream is connected
// first: ComfyUI starts executing as soon as the queue accepts the job, and
// a connection opened afterwards would miss the beginning of the run.
func (r *Runtime) Submit(ctx context.Context, req runtime.WorkflowRequest) (runtime.WorkflowRun, error) {
	if err := r.checkOpen("submit"); err != nil {
		return runtime.WorkflowRun{}, err
	}
	if err := r.requireCapability("submit", runtime.CapabilityWorkflowExecution); err != nil {
		return runtime.WorkflowRun{}, err
	}
	if !isJSONObject(req.Template) {
		return runtime.WorkflowRun{}, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: r.cfg.ID,
			Kind:      runtime.KindComfyUI,
			Operation: "submit",
			Message:   "template must be a JSON object in ComfyUI API Format",
		}
	}

	if run, ok := r.lookupSubmitted(req.IdempotencyKey); ok {
		return run, nil
	}

	release, err := r.limiter.Acquire()
	if err != nil {
		return runtime.WorkflowRun{}, r.annotate("submit", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()

	if err := r.events.ensureConnected(ctx); err != nil {
		return runtime.WorkflowRun{}, err
	}

	clientID := req.ClientID
	if clientID == "" {
		clientID = r.events.clientID
	}
	resp, err := r.client.SubmitPrompt(ctx, promptRequest{
		Prompt:   req.Template,
		ClientID: clientID,
	})
	if err != nil {
		return runtime.WorkflowRun{}, err
	}

	run := runtime.WorkflowRun{
		ID:          resp.PromptID,
		RuntimeID:   r.cfg.ID,
		SubmittedAt: r.clock.Now(),
	}
	r.recordSubmitted(req.IdempotencyKey, run)
	return run, nil
}

// Subscribe returns the event stream for one run. Callers must close it.
//
// Events are best-effort by design: they can be missed across a reconnect
// and are dropped for a subscriber that stops reading, so Status — which
// reads History — remains the authority on how a run ended.
func (r *Runtime) Subscribe(ctx context.Context, runID string) (runtime.Stream[runtime.WorkflowEvent], error) {
	if err := r.checkOpen("subscribe"); err != nil {
		return nil, err
	}
	if err := r.requireCapability("subscribe", runtime.CapabilityWorkflowEvents); err != nil {
		return nil, err
	}
	if err := r.events.ensureConnected(ctx); err != nil {
		return nil, err
	}
	return r.events.subscribe(runID)
}

// Status reports a run's state from the server's own records: History when
// the run has finished, the queue while it is waiting or executing. Events
// are never consulted, so a status is correct even for a caller that
// subscribed late or was disconnected.
func (r *Runtime) Status(ctx context.Context, runID string) (runtime.WorkflowStatus, error) {
	if err := r.checkOpen("status"); err != nil {
		return runtime.WorkflowStatus{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()

	entry, found, err := r.client.History(ctx, "status", runID)
	if err != nil {
		return runtime.WorkflowStatus{}, err
	}
	if found {
		return statusFromHistory(entry), nil
	}

	queue, err := r.client.Queue(ctx, "status")
	if err != nil {
		return runtime.WorkflowStatus{}, err
	}
	if queue.isRunning(runID) {
		return runtime.WorkflowStatus{State: runtime.WorkflowRunning}, nil
	}
	if position := queue.Position(runID); position > 0 {
		return runtime.WorkflowStatus{State: runtime.WorkflowPending, QueuePosition: position}, nil
	}

	// Neither queued nor recorded. ComfyUI keeps history in memory, so this
	// is what a restarted server looks like; reporting it as pending would
	// leave the caller waiting for a run that no longer exists.
	return runtime.WorkflowStatus{
		State:        runtime.WorkflowFailed,
		ErrorSummary: "the run is in neither the queue nor the history; the server may have been restarted",
	}, nil
}

// statusFromHistory normalizes one history entry. The status string decides
// success or failure, and the message log decides whether a failure was
// actually an interruption — ComfyUI records both as a non-success run.
func statusFromHistory(entry historyEntry) runtime.WorkflowStatus {
	interrupted, summary := scanHistoryMessages(entry.Status.Messages)

	switch {
	case interrupted:
		return runtime.WorkflowStatus{State: runtime.WorkflowCancelled, ErrorSummary: summary}
	case entry.Status.StatusStr == "success":
		return runtime.WorkflowStatus{State: runtime.WorkflowSucceeded}
	case entry.Status.StatusStr == "error":
		if summary == "" {
			summary = "the workflow failed; see the run's history for the failing node"
		}
		return runtime.WorkflowStatus{State: runtime.WorkflowFailed, ErrorSummary: summary}
	case !entry.Status.Completed:
		return runtime.WorkflowStatus{State: runtime.WorkflowRunning}
	default:
		return runtime.WorkflowStatus{State: runtime.WorkflowSucceeded}
	}
}

// scanHistoryMessages walks the [event_name, payload] pairs a history entry
// carries, looking only for the two that change the normalized state.
func scanHistoryMessages(messages [][]json.RawMessage) (interrupted bool, summary string) {
	for _, message := range messages {
		if len(message) == 0 {
			continue
		}
		var name string
		if err := json.Unmarshal(message[0], &name); err != nil {
			continue
		}
		switch name {
		case "execution_interrupted":
			interrupted = true
		case "execution_error":
			if len(message) > 1 {
				summary = truncate(errorMessageFromHistory(message[1]), maxErrorMessageLen)
			}
		}
	}
	return interrupted, summary
}

func errorMessageFromHistory(payload json.RawMessage) string {
	var detail struct {
		NodeID           any    `json:"node_id"`
		NodeType         string `json:"node_type"`
		ExceptionType    string `json:"exception_type"`
		ExceptionMessage string `json:"exception_message"`
	}
	if err := json.Unmarshal(payload, &detail); err != nil {
		return "the workflow failed"
	}
	node := ""
	switch id := detail.NodeID.(type) {
	case string:
		node = id
	case float64:
		node = strconv.FormatInt(int64(id), 10)
	}
	if node == "" && detail.NodeType == "" {
		return detail.ExceptionMessage
	}
	return fmt.Sprintf("node %s (%s): %s %s", node, detail.NodeType, detail.ExceptionType, detail.ExceptionMessage)
}

// Cancel stops a run, but only where doing so cannot disturb someone else's
// work.
//
// A pending run is removed from the queue by id, which is unambiguous. A
// running one can only be stopped with POST /interrupt, and that route
// interrupts whatever the server is executing without taking an id — so it
// is used only on an instance configured as exclusive and only after the
// queue confirms the running job is the target. Anywhere else this returns
// ErrCancelUnsupported rather than risk cancelling another caller's run.
func (r *Runtime) Cancel(ctx context.Context, runID string) error {
	if err := r.checkOpen("cancel"); err != nil {
		return err
	}
	if err := r.requireCapability("cancel", runtime.CapabilityWorkflowCancel); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()

	queue, err := r.client.Queue(ctx, "cancel")
	if err != nil {
		return err
	}

	if queue.Position(runID) > 0 {
		return r.client.DeleteFromQueue(ctx, runID)
	}

	if queue.isRunning(runID) {
		switch {
		case !r.cfg.Exclusive:
			return r.cancelUnsupported("the run is already executing and this instance is shared; interrupting it would stop whatever the server is running for other callers")
		case len(queue.RunningIDs) != 1:
			return r.cancelUnsupported("the server reports more than one running job, so an interrupt cannot be aimed at this run")
		default:
			return r.client.Interrupt(ctx)
		}
	}

	if _, found, err := r.client.History(ctx, "cancel", runID); err != nil {
		return err
	} else if found {
		return r.cancelUnsupported("the run has already finished")
	}
	return r.cancelUnsupported("the run is in neither the queue nor the history")
}

func (r *Runtime) cancelUnsupported(message string) error {
	return &runtime.RuntimeError{
		Code:      runtime.ErrorCancelUnsupported,
		RuntimeID: r.cfg.ID,
		Kind:      runtime.KindComfyUI,
		Operation: "cancel",
		Message:   message,
		Cause:     runtime.ErrCancelUnsupported,
	}
}

// OpenArtifact streams one output file from GET /view. The body is handed
// to the caller unbuffered and must be closed by them; it stops with an
// error rather than growing past the configured artifact size cap.
func (r *Runtime) OpenArtifact(ctx context.Context, ref runtime.ArtifactRef) (runtime.Artifact, error) {
	if err := r.checkOpen("open_artifact"); err != nil {
		return runtime.Artifact{}, err
	}
	if err := r.requireCapability("open_artifact", runtime.CapabilityArtifactRead); err != nil {
		return runtime.Artifact{}, err
	}
	if ref.Filename == "" {
		return runtime.Artifact{}, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: r.cfg.ID,
			Kind:      runtime.KindComfyUI,
			Operation: "open_artifact",
			Message:   "artifact reference has no filename",
		}
	}

	resp, err := r.client.View(ctx, ref)
	if err != nil {
		return runtime.Artifact{}, err
	}
	limit := r.client.MaxArtifactBytes()
	if resp.ContentLength > limit {
		resp.Body.Close()
		return runtime.Artifact{}, &runtime.RuntimeError{
			Code:       runtime.ErrorResponseTooLarge,
			RuntimeID:  r.cfg.ID,
			Kind:       runtime.KindComfyUI,
			Operation:  "open_artifact",
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("artifact is %d bytes, above the %d byte limit", resp.ContentLength, limit),
		}
	}

	return runtime.Artifact{
		Ref:         ref,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        resp.ContentLength,
		Body:        &limitedBody{inner: resp.Body, remaining: limit, runtimeID: r.cfg.ID},
	}, nil
}

// Artifacts lists the output files a finished run produced, read from its
// History entry.
//
// It is an adapter extension rather than part of WorkflowRuntime: the
// interface takes an ArtifactRef but offers no way to enumerate them, and a
// caller that missed the node_output events — after a reconnect, or after
// an Agent restart — has no other way to find its own outputs. Promoting it
// to the interface is an upper-layer decision, so it stays available
// through a type assertion for now.
func (r *Runtime) Artifacts(ctx context.Context, runID string) ([]runtime.ArtifactRef, error) {
	if err := r.checkOpen("artifacts"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()

	entry, found, err := r.client.History(ctx, "artifacts", runID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return artifactRefsFromOutputs(runID, entry.Outputs), nil
}

// artifactRefsFromOutputs walks a history entry's per-node outputs. Node
// types name their output lists differently — images, gifs, audio, video,
// and whatever a custom node invents — so any array whose elements carry a
// filename is treated as artifacts, rather than hard-coding the handful of
// keys a stock install happens to use.
//
// Results are ordered by node id and then by output key, both compared as
// strings. That makes the order stable across calls, which is what callers
// need; it is not the graph's execution order, which History does not
// report.
func artifactRefsFromOutputs(runID string, outputs map[string]json.RawMessage) []runtime.ArtifactRef {
	type fileRef struct {
		Filename  string `json:"filename"`
		Subfolder string `json:"subfolder"`
		Type      string `json:"type"`
	}

	var refs []runtime.ArtifactRef
	nodeIDs := make([]string, 0, len(outputs))
	for nodeID := range outputs {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)

	for _, nodeID := range nodeIDs {
		var byKey map[string]json.RawMessage
		if err := json.Unmarshal(outputs[nodeID], &byKey); err != nil {
			continue
		}
		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, key := range keys {
			var files []fileRef
			if err := json.Unmarshal(byKey[key], &files); err != nil {
				continue
			}
			for _, file := range files {
				if file.Filename == "" {
					continue
				}
				refs = append(refs, runtime.ArtifactRef{
					RunID:     runID,
					Filename:  file.Filename,
					Subfolder: file.Subfolder,
					Type:      file.Type,
				})
			}
		}
	}
	return refs
}

// Close stops the event stream, releases the concurrency budget and makes
// every subsequent call fail with ErrorClosed. It is idempotent.
func (r *Runtime) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	r.limiter.Close()
	return r.events.close()
}

// EventStreamStats reports what the event multiplexer has seen: reconnects,
// discarded binary preview frames, and events dropped for subscribers that
// fell behind. It exists for observability and tests, not for the request
// path.
func (r *Runtime) EventStreamStats() (reconnects, binaryFrames, droppedEvents int64) {
	return r.events.reconnects.Load(), r.events.binaryFrames.Load(), r.events.droppedEvent.Load()
}

// endpointCapabilities is the capability evidence a reachable ComfyUI
// provides. These four are structural: every ComfyUI build serves /prompt,
// /ws, /queue and /view, so a server that answered discovery serves them
// too. What varies is not whether cancellation exists but whether it is
// safe to use, which the Cancel documentation and the exclusive flag
// govern — not this capability.
func endpointCapabilities() runtime.CapabilitySet {
	detail := fmt.Sprintf("GET %s answered; ComfyUI serves %s, %s, %s and %s in the same server",
		pathSystemStats, pathPrompt, pathWebSocket, pathQueue, pathView)
	set := make(runtime.CapabilitySet, 4)
	for _, capability := range []runtime.Capability{
		runtime.CapabilityWorkflowExecution,
		runtime.CapabilityWorkflowEvents,
		runtime.CapabilityWorkflowCancel,
		runtime.CapabilityArtifactRead,
	} {
		set[capability] = runtime.CapabilityEvidence{
			Capability: capability,
			Level:      runtime.SupportSupported,
			Source:     runtime.SourceEndpoint,
			Detail:     detail,
		}
	}
	return set
}

func (r *Runtime) overrideCapabilities() runtime.CapabilitySet {
	if len(r.cfg.CapabilityOverrides) == 0 {
		return nil
	}
	set := make(runtime.CapabilitySet, len(r.cfg.CapabilityOverrides))
	for capability, level := range r.cfg.CapabilityOverrides {
		set[capability] = runtime.CapabilityEvidence{
			Capability: capability,
			Level:      level,
			Source:     runtime.SourceConfigOverride,
			Detail:     "declared by runtime configuration, not observed from the backend",
		}
	}
	return set
}

// requireCapability gates one operation against the last Discover, matching
// how the inference adapters gate their request path.
func (r *Runtime) requireCapability(operation string, capability runtime.Capability) error {
	var caps runtime.CapabilitySet
	if discovery := r.discovery.Load(); discovery != nil {
		caps = discovery.Capabilities
	}
	err := caps.Require(capability)
	if err == nil {
		return nil
	}
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return err
	}
	return &runtime.RuntimeError{
		Code:      rerr.Code,
		RuntimeID: r.cfg.ID,
		Kind:      runtime.KindComfyUI,
		Operation: operation,
		Message:   rerr.Message,
		Cause:     rerr.Cause,
	}
}

func (r *Runtime) lookupSubmitted(key string) (runtime.WorkflowRun, bool) {
	if key == "" {
		return runtime.WorkflowRun{}, false
	}
	r.submitMu.Lock()
	defer r.submitMu.Unlock()
	run, ok := r.submitted[key]
	return run, ok
}

func (r *Runtime) recordSubmitted(key string, run runtime.WorkflowRun) {
	if key == "" {
		return
	}
	r.submitMu.Lock()
	defer r.submitMu.Unlock()
	r.submitted[key] = run
}

func (r *Runtime) checkOpen(operation string) error {
	if r.closed.Load() {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorClosed,
			RuntimeID: r.cfg.ID,
			Kind:      runtime.KindComfyUI,
			Operation: operation,
			Message:   "runtime is closed",
			Cause:     runtime.ErrRuntimeClosed,
		}
	}
	return nil
}

func (r *Runtime) annotate(operation string, err error) error {
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return err
	}
	annotated := *rerr
	annotated.RuntimeID = r.cfg.ID
	annotated.Kind = runtime.KindComfyUI
	annotated.Operation = operation
	return &annotated
}

// asProbeMismatch reclassifies a 4xx on the identifying endpoint as
// ErrorProbeMismatch; connection failures and 5xx stay as they are, since
// they describe an unreachable or broken server rather than the wrong kind.
func (r *Runtime) asProbeMismatch(err error, operation, path string) error {
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return err
	}
	if rerr.Code != runtime.ErrorProtocol && (rerr.StatusCode < 400 || rerr.StatusCode >= 500) {
		return err
	}
	return &runtime.RuntimeError{
		Code:       runtime.ErrorProbeMismatch,
		RuntimeID:  r.cfg.ID,
		Kind:       runtime.KindComfyUI,
		Operation:  operation,
		StatusCode: rerr.StatusCode,
		Message:    fmt.Sprintf("%s did not answer as a ComfyUI server: %s", path, rerr.Message),
		Cause:      err,
	}
}

// limitedBody stops an artifact download once it exceeds the configured
// cap, instead of truncating silently or buffering without bound.
type limitedBody struct {
	inner     io.ReadCloser
	remaining int64
	runtimeID string
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, &runtime.RuntimeError{
			Code:      runtime.ErrorResponseTooLarge,
			RuntimeID: b.runtimeID,
			Kind:      runtime.KindComfyUI,
			Operation: "open_artifact",
			Message:   "artifact exceeded the maximum allowed size",
		}
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.inner.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.inner.Close() }

// errorSummary extracts the already-sanitized message from a RuntimeError
// for use in a HealthReport or a warning, avoiding an unsanitized error
// string.
func errorSummary(err error) string {
	var rerr *runtime.RuntimeError
	if errors.As(err, &rerr) && rerr.Message != "" {
		return rerr.Message
	}
	return "the request failed"
}

// conflictWarnings surfaces same-priority capability conflicts that
// runtime.Merge resolved conservatively into Discovery.Warnings.
func conflictWarnings(set runtime.CapabilitySet) []string {
	var warnings []string
	for capability, ev := range set {
		if strings.HasPrefix(ev.Detail, "conflicting ") {
			warnings = append(warnings, fmt.Sprintf("%s: %s", capability, ev.Detail))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// isJSONObject reports whether raw is a JSON object. The leading-brace
// check is what rejects a literal null, which unmarshals into a map
// without error and would otherwise be submitted as an empty workflow.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe map[string]json.RawMessage
	return json.Unmarshal(trimmed, &probe) == nil
}

func versionOrUnknown(version string) string {
	if version == "" {
		return "(absent)"
	}
	return version
}
