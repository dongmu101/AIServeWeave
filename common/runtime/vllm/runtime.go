// Package vllm adapts an already-running vLLM OpenAI-compatible server to
// the runtime.InferenceRuntime contract.
//
// The adapter touches five endpoints and no others: /version and
// /v1/models to establish identity, /health for liveness, and
// /v1/chat/completions and /v1/embeddings to serve requests. vLLM also
// exposes administrative routes — LoRA adapter loading/unloading, sleep and
// wake, and in some deployments a shutdown route — which can change or stop
// a server other tenants are using. This adapter never calls them, because
// discovering an endpoint is not a reason to operate it.
package vllm

import (
	"context"
	"fmt"
	"net/http"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/oaibase"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/openai"
)

const (
	versionPath = "/version"
	healthPath  = "/health"
	modelsPath  = "/v1/models"
)

// Runtime is the vLLM adapter. It is safe for concurrent use; the
// capability snapshot published by Discover is replaced atomically and only
// read on the request path.
//
// The request path (Chat, ChatStream, Embed, the concurrency limiter and
// the capability gate) is shared with the other OpenAI-compatible adapters
// and lives in internal/oaibase; this type adds vLLM's identity, health and
// model-discovery protocol on top.
type Runtime struct {
	base *oaibase.Base
}

// Compile-time proof that the adapter satisfies the inference contract.
var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New builds a vLLM Runtime for a normalized, validated Config. It performs
// no network I/O; Manager owns Probe, Discover and scheduling.
//
// New has the signature of runtime.Factory, so it can be registered with
// runtime.Registry.Register(runtime.KindVLLM, vllm.New).
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	base, err := oaibase.New(cfg, runtime.KindVLLM, deps)
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

// Embed requests embeddings. vLLM's /v1/embeddings only works when the
// served model is an embedding model, which no endpoint reports, so the
// capability stays unknown and this call is refused until an operator
// declares embeddings support for the instance through
// Config.CapabilityOverrides.
func (r *Runtime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return r.base.Embed(ctx, req)
}

// Close releases the instance's concurrency budget and makes every
// subsequent call fail with ErrorClosed. It is idempotent.
func (r *Runtime) Close() error { return r.base.Close() }

// Probe verifies the endpoint is a vLLM server by requiring both GET
// /version — which vLLM serves and a bare OpenAI-compatible server such as
// SGLang does not — and GET /v1/models.
//
// A /version response without a version field still verifies identity: the
// route answering at all is the vLLM-specific evidence, and vLLM has
// changed that payload's shape before. The version is reported empty
// instead, which leaves the capability profile unapplied and is surfaced by
// Discover as a warning rather than silently assumed away.
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if err := r.base.CheckOpen("probe"); err != nil {
		return runtime.ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	version, err := r.fetchVersion(ctx, "probe")
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", versionPath)
	}
	models, err := openai.ListModels(ctx, r.base.Client())
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", modelsPath)
	}

	return runtime.ProbeResult{
		Kind:             runtime.KindVLLM,
		Version:          version,
		IdentityVerified: true,
		Evidence: fmt.Sprintf("GET %s reported version %s; GET %s listed %d model(s)",
			versionPath, versionOrUnknown(version), modelsPath, len(models)),
		ProbedAt: r.base.Clock().Now(),
	}, nil
}

// Health calls GET /health, which reports engine liveness without running
// inference, so a saturated server still answers.
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	clock := r.base.Clock()
	if err := r.base.CheckOpen("health"); err != nil {
		return runtime.HealthReport{State: runtime.StateClosed, CheckedAt: clock.Now()}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	start := clock.Now()
	// out is nil on purpose: vLLM's /health answers 200 with an empty body,
	// so any non-2xx is the signal and there is nothing to decode.
	err := r.base.Client().Do(ctx, "health", http.MethodGet, healthPath, nil, nil)
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

// Discover reports the served models and the capability set for the
// reported version. vLLM exposes no per-model capability metadata, so every
// model carries the runtime-level conclusion; anything the version profile
// does not cover stays unknown rather than being inferred from the model
// name or from which endpoints happen to exist.
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.base.CheckOpen("discover"); err != nil {
		return runtime.Discovery{}, err
	}

	version, err := r.fetchVersion(ctx, "discover")
	if err != nil {
		return runtime.Discovery{}, err
	}
	ids, err := openai.ListModels(ctx, r.base.Client())
	if err != nil {
		return runtime.Discovery{}, err
	}

	runtimeCaps := runtime.Merge(runtimeProfile.Resolve(version), r.base.OverrideCapabilities())

	var warnings []string
	if version == "" {
		warnings = append(warnings, fmt.Sprintf(
			"%s did not report a version; no capability profile applied, so every capability stays unknown until declared through capability overrides",
			versionPath))
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

type versionResponse struct {
	Version string `json:"version"`
}

// fetchVersion reads GET /version. A missing version field is not an error:
// it yields an empty string, which callers treat as "version unknown".
func (r *Runtime) fetchVersion(ctx context.Context, operation string) (string, error) {
	var resp versionResponse
	if err := r.base.Client().Do(ctx, operation, http.MethodGet, versionPath, nil, &resp); err != nil {
		return "", err
	}
	return resp.Version, nil
}

func versionOrUnknown(version string) string {
	if version == "" {
		return "(absent)"
	}
	return version
}
