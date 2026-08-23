// Package sglang adapts an already-running SGLang OpenAI-compatible server
// to the runtime.InferenceRuntime contract.
//
// Two things make this adapter different from its siblings. First, SGLang
// serves no endpoint that identifies it: its OpenAI-compatible responses
// are indistinguishable from vLLM's, so the runtime kind comes from
// configuration and every ProbeResult reports IdentityVerified=false.
// Second, GET /health is not present in every release, so the adapter
// detects its absence and degrades health checks to GET /v1/models,
// recording the degradation where operators can see it.
//
// SGLang also exposes a native /generate endpoint. It is deliberately not
// implemented: maintaining a second generation protocol alongside the
// OpenAI-compatible one would double the surface this package has to keep
// correct, for no capability the shared path does not already cover.
package sglang

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/oaibase"
	"AIServeWeave/common/runtime/openai"
)

const (
	healthPath     = "/health"
	modelsPath     = "/v1/models"
	serverInfoPath = "/get_server_info"
)

// degradedHealthWarning is the note published in Discovery.Warnings — and
// through it in Snapshot.Degraded — while health checks are falling back to
// /v1/models because /health is absent.
const degradedHealthWarning = "GET " + healthPath + " is not served by this instance; health checks fall back to GET " + modelsPath + ", which proves the HTTP server is up but not that the inference engine is"

// Runtime is the SGLang adapter. It is safe for concurrent use; the
// capability snapshot published by Discover is replaced atomically and only
// read on the request path.
//
// The request path (Chat, ChatStream, Embed, the concurrency limiter and
// the capability gate) is shared with the other OpenAI-compatible adapters
// and lives in internal/oaibase; this type adds SGLang's health degradation
// and optional private server-info lookup on top.
type Runtime struct {
	base *oaibase.Base

	// healthDegraded records that GET /health answered 404, so Health uses
	// GET /v1/models instead. It is set by Probe and, if a server drops the
	// route mid-life, by Health itself; it is never cleared automatically,
	// because a route that disappeared once must not silently become the
	// health signal again without a fresh Probe.
	healthDegraded atomic.Bool
}

// Compile-time proof that the adapter satisfies the inference contract.
var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New builds an SGLang Runtime for a normalized, validated Config. It
// performs no network I/O; Manager owns Probe, Discover and scheduling.
//
// New has the signature of runtime.Factory, so it can be registered with
// runtime.Registry.Register(runtime.KindSGLang, sglang.New).
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	base, err := oaibase.New(cfg, runtime.KindSGLang, deps)
	if err != nil {
		return nil, err
	}
	return &Runtime{base: base}, nil
}

// Descriptor reports the instance's stable identity and scheduling summary.
func (r *Runtime) Descriptor() runtime.Descriptor { return r.base.Descriptor() }

// Chat sends a non-streaming chat completion, gated on the capabilities the
// last Discover confirmed.
func (r *Runtime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	return r.base.Chat(ctx, req)
}

// ChatStream sends a streaming chat completion. The returned stream holds
// the instance's concurrency slot until it is closed, so callers must close
// it — including after Recv returns io.EOF.
func (r *Runtime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	return r.base.ChatStream(ctx, req)
}

// Embed requests embeddings. SGLang serves /v1/embeddings only for
// embedding models and reports nothing about which model class is loaded,
// so the capability stays unknown and this call is refused until an
// operator declares support through Config.CapabilityOverrides.
func (r *Runtime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return r.base.Embed(ctx, req)
}

// Close releases the instance's concurrency budget and makes every
// subsequent call fail with ErrorClosed. It is idempotent.
func (r *Runtime) Close() error { return r.base.Close() }

// Probe checks that the endpoint serves GET /v1/models and records whether
// GET /health exists, which decides how later health checks are performed.
//
// IdentityVerified is always false: an OpenAI-compatible response cannot
// tell SGLang apart from vLLM, so the kind is taken from configuration and
// the snapshot says so plainly rather than implying a verification that did
// not happen.
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if err := r.base.CheckOpen("probe"); err != nil {
		return runtime.ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	models, err := openai.ListModels(ctx, r.base.Client())
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", modelsPath)
	}

	healthNote := "GET " + healthPath + " is present"
	if r.detectHealthRoute(ctx) {
		healthNote = "GET " + healthPath + " is absent; health checks use GET " + modelsPath
	}

	return runtime.ProbeResult{
		Kind:             runtime.KindSGLang,
		Version:          "", // SGLang has no version endpoint; Discover may recover one
		IdentityVerified: false,
		Evidence: fmt.Sprintf("GET %s listed %d model(s); %s; runtime kind asserted by configuration, since an OpenAI-compatible response cannot distinguish SGLang from vLLM",
			modelsPath, len(models), healthNote),
		ProbedAt: r.base.Clock().Now(),
	}, nil
}

// Health calls GET /health, or GET /v1/models on an instance whose /health
// route is absent. Neither runs inference, so a saturated server still
// answers.
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	clock := r.base.Clock()
	if err := r.base.CheckOpen("health"); err != nil {
		return runtime.HealthReport{State: runtime.StateClosed, CheckedAt: clock.Now()}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	start := clock.Now()
	err := r.checkHealth(ctx)
	checkedAt := clock.Now()
	if err != nil {
		return runtime.HealthReport{
			State:        runtime.StateUnhealthy,
			Latency:      checkedAt.Sub(start),
			CheckedAt:    checkedAt,
			ErrorSummary: oaibase.ErrorSummary(err),
		}, err
	}
	return runtime.HealthReport{
		State:     runtime.StateHealthy,
		Latency:   checkedAt.Sub(start),
		CheckedAt: checkedAt,
	}, nil
}

// checkHealth runs one health check, switching to the degraded path if
// /health turns out to be missing — including on a server that drops the
// route between checks, which is why the 404 is handled here and not only
// in Probe.
func (r *Runtime) checkHealth(ctx context.Context) error {
	if !r.healthDegraded.Load() {
		// out is nil on purpose: /health answers with an empty body, so any
		// non-2xx is the signal and there is nothing to decode.
		err := r.base.Client().Do(ctx, "health", http.MethodGet, healthPath, nil, nil)
		if err == nil {
			return nil
		}
		if !isNotFound(err) {
			return err
		}
		r.healthDegraded.Store(true)
		r.base.Logger().Warn("sglang health endpoint is absent; degrading to the models endpoint",
			"runtime_id", r.base.Config().ID, "endpoint", healthPath, "fallback", modelsPath)
	}
	_, err := openai.ListModels(ctx, r.base.Client())
	return err
}

// detectHealthRoute probes GET /health once and reports whether the route
// is absent. A 5xx is not absence — the route exists and the engine is
// unwell, which is the health check's job to report, not the probe's.
func (r *Runtime) detectHealthRoute(ctx context.Context) (degraded bool) {
	err := r.base.Client().Do(ctx, "probe", http.MethodGet, healthPath, nil, nil)
	if err != nil && isNotFound(err) {
		r.healthDegraded.Store(true)
		return true
	}
	return r.healthDegraded.Load()
}

// Discover reports the served models plus whatever the optional private
// server-info endpoint reveals about the running version. SGLang exposes no
// per-model capability metadata, so every model carries the runtime-level
// conclusion.
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.base.CheckOpen("discover"); err != nil {
		return runtime.Discovery{}, err
	}

	ids, err := openai.ListModels(ctx, r.base.Client())
	if err != nil {
		return runtime.Discovery{}, err
	}

	version, warnings := r.fetchServerVersion(ctx)
	runtimeCaps := runtime.Merge(
		runtimeProfile.Resolve(version),
		endpointCapabilities(),
		r.base.OverrideCapabilities(),
	)

	if r.healthDegraded.Load() {
		warnings = append(warnings, degradedHealthWarning)
	}
	if version == "" {
		warnings = append(warnings, fmt.Sprintf(
			"the running version could not be determined from %s; capabilities beyond the OpenAI-compatible core stay unknown until declared through capability overrides",
			serverInfoPath))
	}
	warnings = append(warnings, oaibase.ConflictWarnings(runtimeCaps)...)

	models := make([]runtime.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, runtime.Model{
			ID:           id,
			Capabilities: runtime.Intersect(runtimeCaps, nil),
		})
	}

	discovery := runtime.Discovery{
		Version:      version,
		Models:       models,
		Capabilities: runtimeCaps,
		Warnings:     warnings,
		DiscoveredAt: r.base.Clock().Now(),
	}
	r.base.PublishDiscovery(discovery)
	return discovery, nil
}

// ListModels reports the models the server currently serves, with the same
// capability resolution Discover applies.
func (r *Runtime) ListModels(ctx context.Context) ([]runtime.Model, error) {
	discovery, err := r.Discover(ctx)
	if err != nil {
		return nil, err
	}
	return discovery.Models, nil
}

// endpointCapabilities is the evidence Probe and Discover actually observe:
// the server answered GET /v1/models, and SGLang's OpenAI-compatible server
// registers the chat and completions routes in the same server as that one.
//
// This is the only capability conclusion this adapter draws without a
// version, and it is confined to the OpenAI-compatible core on purpose.
// Unlike vLLM, SGLang has no version endpoint, so requiring a version for
// even the core would leave every instance unusable until an operator
// declared overrides — while everything genuinely version-dependent (tools,
// structured output, embeddings) still stays unknown.
func endpointCapabilities() runtime.CapabilitySet {
	const detail = "GET " + modelsPath + " answered, and SGLang's OpenAI-compatible server registers the chat and completions routes alongside it; this says nothing about optional features"
	set := make(runtime.CapabilitySet, 3)
	for _, capability := range []runtime.Capability{
		runtime.CapabilityChat,
		runtime.CapabilityChatStream,
		runtime.CapabilityCompletions,
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

// serverInfoResponse models only the one field this adapter reads from
// SGLang's private /get_server_info payload. Every other field — server
// args, load counters, scheduler internals — is ignored on purpose: the
// response is not a stable contract, and letting it into the public model
// or capability contract would tie this package to SGLang's internals.
type serverInfoResponse struct {
	Version string `json:"version"`
}

// fetchServerVersion reads the optional private server-info endpoint. Every
// failure mode — route absent, unparsable body, changed field names — is
// reported as a warning and leaves the version empty, because Discover must
// still succeed for an instance whose private endpoint has drifted.
func (r *Runtime) fetchServerVersion(ctx context.Context) (version string, warnings []string) {
	var info serverInfoResponse
	if err := r.base.Client().Do(ctx, "discover", http.MethodGet, serverInfoPath, nil, &info); err != nil {
		return "", []string{fmt.Sprintf("optional %s was not usable: %s", serverInfoPath, oaibase.ErrorSummary(err))}
	}
	if info.Version == "" {
		return "", []string{fmt.Sprintf("%s did not report a version field", serverInfoPath)}
	}
	return info.Version, nil
}

// isNotFound reports whether err is an upstream 404, which is how a missing
// route is distinguished from a route that exists and failed.
func isNotFound(err error) bool {
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		return false
	}
	return rerr.StatusCode == http.StatusNotFound
}
