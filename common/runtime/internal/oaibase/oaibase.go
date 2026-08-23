// Package oaibase holds the request path every OpenAI-compatible inference
// adapter shares: the HTTP client, the concurrency limiter, the capability
// snapshot published by Discover, and the capability gate that guards Chat,
// ChatStream and Embed.
//
// What the adapters do not share is how a backend proves its identity and
// reports its models — Probe, Health and Discover stay with vllm, sglang
// and ollama, which is exactly where the protocol differences live. This
// package exists so those differences are the only thing each adapter has
// to write.
package oaibase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/openai"
)

// Base is the shared half of an inference adapter. It is safe for
// concurrent use; the capability snapshot is replaced atomically and the
// request path only reads it.
type Base struct {
	cfg    runtime.Config
	kind   runtime.Kind
	logger *slog.Logger
	clock  runtime.Clock

	client  *openai.Client
	limiter *runtime.Limiter

	// discovery holds the most recent successful Discover result and is the
	// only capability evidence the request path consults. It is nil until
	// an adapter publishes one, which makes every capability read as
	// unknown — Manager runs Discover before an instance becomes visible,
	// so requests never legitimately arrive before that.
	discovery atomic.Pointer[runtime.Discovery]

	closed atomic.Bool
}

// New builds a Base for a normalized, validated Config. It performs no
// network I/O. kind must be the Kind the calling adapter serves, and cfg's
// Kind must agree with it.
func New(cfg runtime.Config, kind runtime.Kind, deps runtime.Dependencies) (*Base, error) {
	if cfg.Kind == "" {
		cfg.Kind = kind
	}
	if cfg.Kind != kind {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: cfg.ID,
			Kind:      cfg.Kind,
			Operation: "create_runtime",
			Message:   fmt.Sprintf("the %s adapter cannot serve kind %q", kind, cfg.Kind),
		}
	}

	client, err := openai.NewClient(openai.ClientConfig{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Headers:    cfg.Headers,
		HTTPClient: deps.HTTPClient,
		Kind:       kind,
		RuntimeID:  cfg.ID,
	})
	if err != nil {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			RuntimeID: cfg.ID,
			Kind:      kind,
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

	return &Base{
		cfg:     cfg,
		kind:    kind,
		logger:  logger,
		clock:   clock,
		client:  client,
		limiter: runtime.NewLimiter(cfg.MaxConcurrent),
	}, nil
}

// Config returns the normalized configuration this instance was built with.
func (b *Base) Config() runtime.Config { return b.cfg }

// Client returns the shared OpenAI-compatible HTTP client, which adapters
// also use for their backend's native endpoints so URL joining, auth
// headers, size limits and error mapping stay in one place.
func (b *Base) Client() *openai.Client { return b.client }

// Clock returns the injected clock, so adapters timestamp probe and health
// results from the same source the Manager's scheduling uses.
func (b *Base) Clock() runtime.Clock { return b.clock }

// Logger returns the instance's logger.
func (b *Base) Logger() *slog.Logger { return b.logger }

// Descriptor reports the instance's stable identity and scheduling summary.
func (b *Base) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{
		ID:            b.cfg.ID,
		Kind:          b.kind,
		BaseURL:       b.cfg.BaseURL,
		MaxConcurrent: b.cfg.MaxConcurrent,
		Exclusive:     b.cfg.Exclusive,
	}
}

// PublishDiscovery atomically replaces the capability snapshot the request
// path reads. Adapters call it at the end of a successful Discover.
func (b *Base) PublishDiscovery(d runtime.Discovery) {
	b.discovery.Store(&d)
}

// Chat sends a non-streaming chat completion, gated on the capability
// snapshot from the last Discover: chat itself, plus tools and structured
// output when the request uses them.
func (b *Base) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	release, err := b.BeginRequest("chat", req.Model, ChatCapabilities(req)...)
	if err != nil {
		return runtime.ChatResponse{}, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, b.cfg.RequestTimeout)
	defer cancel()
	return openai.Chat(ctx, b.client, req)
}

// ChatStream sends a streaming chat completion. The returned stream holds
// the instance's concurrency slot until it is closed, so callers must close
// it — including after Recv returns io.EOF.
func (b *Base) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	caps := append(ChatCapabilities(req), runtime.CapabilityChatStream)
	release, err := b.BeginRequest("chat_stream", req.Model, caps...)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, b.cfg.RequestTimeout)
	stream, err := openai.ChatStream(ctx, b.client, req, b.cfg.StreamIdleTimeout)
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	return &guardedStream{Stream: stream, cancel: cancel, release: release}, nil
}

// Embed requests embeddings, gated on the embeddings capability of the
// requested model, so a model the backend never confirmed as an embedding
// model is rejected locally instead of upstream.
func (b *Base) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	release, err := b.BeginRequest("embed", req.Model, runtime.CapabilityEmbeddings)
	if err != nil {
		return runtime.EmbeddingResponse{}, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, b.cfg.RequestTimeout)
	defer cancel()
	return openai.Embed(ctx, b.client, req)
}

// Close releases the instance's concurrency budget and makes every
// subsequent call fail with ErrorClosed. It is idempotent. Streams already
// handed to callers are not closed here; their own Close still works.
func (b *Base) Close() error {
	if b.closed.Swap(true) {
		return nil
	}
	b.limiter.Close()
	return nil
}

// ChatCapabilities is the capability set a chat request must satisfy: chat
// always, plus one per optional feature the request actually uses. Requests
// are rejected rather than having an unsupported field silently dropped.
func ChatCapabilities(req runtime.ChatRequest) []runtime.Capability {
	caps := []runtime.Capability{runtime.CapabilityChat}
	if len(req.Tools) > 0 {
		caps = append(caps, runtime.CapabilityTools)
	}
	if req.ResponseFormat != nil {
		caps = append(caps, runtime.CapabilityStructuredOutput)
	}
	return caps
}

// BeginRequest runs the shared request-path preamble: closed check,
// capability gate, then concurrency slot. The returned release must be
// called exactly once. Adapters only need it directly for operations Base
// does not already implement.
func (b *Base) BeginRequest(operation, model string, required ...runtime.Capability) (func(), error) {
	if err := b.CheckOpen(operation); err != nil {
		return nil, err
	}
	caps := b.CapabilitiesFor(model)
	for _, capability := range required {
		if err := caps.Require(capability); err != nil {
			return nil, b.capabilityError(operation, model, capability, err)
		}
	}
	release, err := b.limiter.Acquire()
	if err != nil {
		return nil, b.Annotate(operation, err)
	}
	return release, nil
}

// CapabilitiesFor returns the capability evidence that governs a request
// for model: the model's own intersected set when the last Discover saw it,
// and the runtime-level set otherwise. Before the first published Discovery
// it returns nil, which resolves every capability to unknown.
func (b *Base) CapabilitiesFor(model string) runtime.CapabilitySet {
	discovery := b.discovery.Load()
	if discovery == nil {
		return nil
	}
	for _, m := range discovery.Models {
		if m.ID == model {
			return m.Capabilities
		}
	}
	return discovery.Capabilities
}

// OverrideCapabilities converts Config.CapabilityOverrides into evidence
// tagged as config-sourced, so a snapshot always shows whether support was
// observed or asserted by an administrator.
func (b *Base) OverrideCapabilities() runtime.CapabilitySet {
	if len(b.cfg.CapabilityOverrides) == 0 {
		return nil
	}
	set := make(runtime.CapabilitySet, len(b.cfg.CapabilityOverrides))
	for capability, level := range b.cfg.CapabilityOverrides {
		set[capability] = runtime.CapabilityEvidence{
			Capability: capability,
			Level:      level,
			Source:     runtime.SourceConfigOverride,
			Detail:     "declared by runtime configuration, not observed from the backend",
		}
	}
	return set
}

// CheckOpen reports an ErrorClosed RuntimeError once Close has been called.
func (b *Base) CheckOpen(operation string) error {
	if b.closed.Load() {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorClosed,
			RuntimeID: b.cfg.ID,
			Kind:      b.kind,
			Operation: operation,
			Message:   "runtime is closed",
			Cause:     runtime.ErrRuntimeClosed,
		}
	}
	return nil
}

// Annotate fills this instance's identity into an error raised by a shared
// helper that does not know which runtime it is serving.
func (b *Base) Annotate(operation string, err error) error {
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return err
	}
	annotated := *rerr
	annotated.RuntimeID = b.cfg.ID
	annotated.Kind = b.kind
	annotated.Operation = operation
	return &annotated
}

// ProbeMismatch reclassifies a failure on an endpoint that is supposed to
// identify the backend as ErrorProbeMismatch: something answered, but not
// as this kind of backend, so it must not be adopted. Connection failures
// and 5xx pass through unchanged — those describe a broken or unreachable
// server, not the wrong kind of server, and a Manager that saw a mismatch
// would drop the instance instead of retrying it.
func (b *Base) ProbeMismatch(err error, operation, path string) error {
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return err
	}
	mismatch := rerr.Code == runtime.ErrorProtocol ||
		(rerr.StatusCode >= 400 && rerr.StatusCode < 500)
	if !mismatch {
		return err
	}
	return &runtime.RuntimeError{
		Code:       runtime.ErrorProbeMismatch,
		RuntimeID:  b.cfg.ID,
		Kind:       b.kind,
		Operation:  operation,
		StatusCode: rerr.StatusCode,
		Message:    fmt.Sprintf("%s did not answer as a %s server: %s", path, b.kind, rerr.Message),
		Cause:      err,
	}
}

// Errorf builds a RuntimeError carrying this instance's identity, for
// adapter-specific failures that are not produced by a shared helper.
func (b *Base) Errorf(code runtime.ErrorCode, operation, format string, args ...any) error {
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: b.cfg.ID,
		Kind:      b.kind,
		Operation: operation,
		Message:   fmt.Sprintf(format, args...),
	}
}

// capabilityError re-stamps the bare error from CapabilitySet.Require with
// this instance's identity and the operation that was refused, keeping the
// original Cause (ErrCapabilityUnknown / ErrCapabilityUnsupported) intact.
func (b *Base) capabilityError(operation, model string, capability runtime.Capability, err error) error {
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return err
	}
	return &runtime.RuntimeError{
		Code:      rerr.Code,
		RuntimeID: b.cfg.ID,
		Kind:      b.kind,
		Operation: operation,
		Message:   fmt.Sprintf("model %q: %s", model, rerr.Message),
		Cause:     rerr.Cause,
	}
}

// ErrorSummary extracts the already-sanitized message from a RuntimeError
// for use in a HealthReport, avoiding an unsanitized error string.
func ErrorSummary(err error) string {
	var rerr *runtime.RuntimeError
	if errors.As(err, &rerr) && rerr.Message != "" {
		return rerr.Message
	}
	return "health check failed"
}

// ConflictWarnings surfaces same-priority capability conflicts that
// runtime.Merge resolved conservatively — it records them in the winning
// evidence's Detail — into Discovery.Warnings.
func ConflictWarnings(set runtime.CapabilitySet) []string {
	var warnings []string
	for capability, ev := range set {
		if strings.HasPrefix(ev.Detail, "conflicting ") {
			warnings = append(warnings, fmt.Sprintf("%s: %s", capability, ev.Detail))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// guardedStream ties the OpenAI stream's lifetime to the request-scoped
// context and the concurrency slot, so closing the stream — the caller's
// documented obligation — also stops the timeout context and returns the
// slot.
type guardedStream struct {
	runtime.Stream[runtime.ChatEvent]
	cancel    context.CancelFunc
	release   func()
	closeOnce sync.Once
}

func (s *guardedStream) Close() error {
	err := s.Stream.Close()
	s.closeOnce.Do(func() {
		s.cancel()
		s.release()
	})
	return err
}
