// Package ollama adapts an already-running Ollama server to the
// runtime.InferenceRuntime contract. Identity, health and model metadata
// come from Ollama's native /api endpoints, while inference reuses the
// shared OpenAI-compatible client in runtime/openai — Ollama's native
// generate protocol is deliberately not implemented, so this package never
// maintains a second generation protocol.
package ollama

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/oaibase"
)

const (
	apiVersionPath = "/api/version"
	apiTagsPath    = "/api/tags"
	apiShowPath    = "/api/show"

	// showConcurrency bounds how many /api/show calls Discover issues at
	// once. Each one can force Ollama to read model metadata from disk, so
	// discovery of a host with dozens of models must not arrive as dozens
	// of simultaneous requests.
	showConcurrency = 4
)

// ollamaCapabilityMap translates the capability strings Ollama reports for
// a model (via /api/tags and /api/show) into this package's capability
// vocabulary. Ollama's list is exhaustive for a given model, so a mapped
// capability that is absent from the list is reported as explicitly
// unsupported rather than unknown — that is what keeps Embed from being
// offered on a chat-only model.
//
// Ollama's "insert" (fill-in-the-middle) capability has no runtime.Capability
// counterpart and is ignored rather than mapped onto an approximate one.
var ollamaCapabilityMap = map[string][]runtime.Capability{
	"completion": {runtime.CapabilityChat, runtime.CapabilityChatStream, runtime.CapabilityCompletions},
	"tools":      {runtime.CapabilityTools},
	"vision":     {runtime.CapabilityVision},
	"thinking":   {runtime.CapabilityReasoning},
	"embedding":  {runtime.CapabilityEmbeddings},
}

// mappedModelCapabilities is every runtime.Capability ollamaCapabilityMap can
// produce, i.e. exactly the capabilities a model capability list is
// authoritative about.
var mappedModelCapabilities = func() []runtime.Capability {
	seen := make(map[runtime.Capability]bool)
	var all []runtime.Capability
	for _, caps := range ollamaCapabilityMap {
		for _, c := range caps {
			if !seen[c] {
				seen[c] = true
				all = append(all, c)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all
}()

// Runtime is the Ollama adapter. It is safe for concurrent use; the
// capability snapshot published by Discover is replaced atomically and only
// read on the request path.
//
// The request path (Chat, ChatStream, Embed, the concurrency limiter and
// the capability gate) is shared with the other OpenAI-compatible adapters
// and lives in internal/oaibase; this type adds Ollama's native identity,
// health and model-discovery protocol on top.
type Runtime struct {
	base *oaibase.Base

	// showCache memoizes /api/show results by model digest so a periodic
	// Discover does not re-read metadata for models that have not changed.
	// A digest changes whenever the model does, so entries never go stale.
	showMu    sync.Mutex
	showCache map[string]runtime.CapabilitySet
}

// Compile-time proof that the adapter satisfies the inference contract.
var _ runtime.InferenceRuntime = (*Runtime)(nil)

// New builds an Ollama Runtime for a normalized, validated Config. It
// performs no network I/O; Manager owns Probe, Discover and scheduling.
//
// New has the signature of runtime.Factory, so it can be registered with
// runtime.Registry.Register(runtime.KindOllama, ollama.New).
func New(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
	base, err := oaibase.New(cfg, runtime.KindOllama, deps)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		base:      base,
		showCache: make(map[string]runtime.CapabilitySet),
	}, nil
}

// Descriptor reports the instance's stable identity and scheduling summary.
func (r *Runtime) Descriptor() runtime.Descriptor { return r.base.Descriptor() }

// Chat sends a non-streaming chat completion over the OpenAI-compatible
// endpoint, gated on the capabilities the last Discover confirmed for the
// requested model.
func (r *Runtime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	return r.base.Chat(ctx, req)
}

// ChatStream sends a streaming chat completion. The returned stream holds
// the instance's concurrency slot until it is closed, so callers must close
// it — including after Recv returns io.EOF.
func (r *Runtime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	return r.base.ChatStream(ctx, req)
}

// Embed requests embeddings over the OpenAI-compatible endpoint. The
// capability gate rejects models Ollama does not report as embedding
// models, so a chat model never reaches the backend as an embedding
// request.
func (r *Runtime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	return r.base.Embed(ctx, req)
}

// Close releases the instance's concurrency budget and makes every
// subsequent call fail with ErrorClosed. It is idempotent.
func (r *Runtime) Close() error { return r.base.Close() }

// Probe verifies that the endpoint really is an Ollama server by calling
// two native endpoints, /api/version and /api/tags. A backend that only
// answers the OpenAI-compatible routes fails here with ErrorProbeMismatch
// rather than being adopted as an Ollama instance.
func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if err := r.base.CheckOpen("probe"); err != nil {
		return runtime.ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	version, err := r.fetchVersion(ctx, "probe")
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", apiVersionPath)
	}
	tags, err := r.fetchTags(ctx, "probe")
	if err != nil {
		return runtime.ProbeResult{}, r.base.ProbeMismatch(err, "probe", apiTagsPath)
	}

	return runtime.ProbeResult{
		Kind:             runtime.KindOllama,
		Version:          version,
		IdentityVerified: true,
		Evidence: fmt.Sprintf("GET %s reported version %s; GET %s listed %d model(s)",
			apiVersionPath, version, apiTagsPath, len(tags)),
		ProbedAt: r.base.Clock().Now(),
	}, nil
}

// Health calls GET /api/version, which answers without loading a model or
// running inference, so a busy server still reports promptly.
func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	clock := r.base.Clock()
	if err := r.base.CheckOpen("health"); err != nil {
		return runtime.HealthReport{State: runtime.StateClosed, CheckedAt: clock.Now()}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.base.Config().ProbeTimeout)
	defer cancel()

	start := clock.Now()
	_, err := r.fetchVersion(ctx, "health")
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

// Discover reads the model inventory from /api/tags, filling in per-model
// capabilities from /api/show for servers whose /api/tags response predates
// the inline capabilities field. The resulting snapshot replaces the one
// the request path reads.
func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if err := r.base.CheckOpen("discover"); err != nil {
		return runtime.Discovery{}, err
	}

	version, err := r.fetchVersion(ctx, "discover")
	if err != nil {
		return runtime.Discovery{}, err
	}
	tags, err := r.fetchTags(ctx, "discover")
	if err != nil {
		return runtime.Discovery{}, err
	}

	runtimeCaps := runtime.Merge(runtimeProfile.Resolve(version), r.base.OverrideCapabilities())

	modelCaps, warnings := r.resolveModelCapabilities(ctx, tags)
	models := make([]runtime.Model, 0, len(tags))
	for i, tag := range tags {
		models = append(models, runtime.Model{
			ID:           tag.Name,
			Capabilities: runtime.Intersect(runtimeCaps, modelCaps[i]),
		})
	}
	warnings = append(warnings, oaibase.ConflictWarnings(runtimeCaps)...)

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

// ListModels reports the models currently present on the server, with the
// same capability resolution Discover applies.
func (r *Runtime) ListModels(ctx context.Context) ([]runtime.Model, error) {
	discovery, err := r.Discover(ctx)
	if err != nil {
		return nil, err
	}
	return discovery.Models, nil
}

// --- native Ollama API ------------------------------------------------

type versionResponse struct {
	Version string `json:"version"`
}

type tagsResponse struct {
	Models []tagModel `json:"models"`
}

// tagModel is one entry of /api/tags. Capabilities is present only on newer
// servers; when it is missing, Discover falls back to /api/show.
type tagModel struct {
	Name         string   `json:"name"`
	Model        string   `json:"model"`
	Digest       string   `json:"digest"`
	ModifiedAt   string   `json:"modified_at"`
	Capabilities []string `json:"capabilities"`
}

type showRequest struct {
	Model string `json:"model"`
}

type showResponse struct {
	Capabilities []string `json:"capabilities"`
}

func (r *Runtime) fetchVersion(ctx context.Context, operation string) (string, error) {
	var resp versionResponse
	if err := r.base.Client().Do(ctx, operation, http.MethodGet, apiVersionPath, nil, &resp); err != nil {
		return "", err
	}
	if resp.Version == "" {
		return "", r.base.Errorf(runtime.ErrorProtocol, operation,
			"%s response did not contain a version", apiVersionPath)
	}
	return resp.Version, nil
}

func (r *Runtime) fetchTags(ctx context.Context, operation string) ([]tagModel, error) {
	var resp tagsResponse
	if err := r.base.Client().Do(ctx, operation, http.MethodGet, apiTagsPath, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Models, nil
}

// resolveModelCapabilities returns one capability set per tag, in the same
// order. Servers that inline capabilities in /api/tags are answered without
// any extra request; the rest are filled in from /api/show, at most
// showConcurrency at a time and skipping models whose digest is already
// cached. A failed /api/show leaves that model's capabilities unknown and
// adds a warning instead of failing the whole discovery.
func (r *Runtime) resolveModelCapabilities(ctx context.Context, tags []tagModel) ([]runtime.CapabilitySet, []string) {
	sets := make([]runtime.CapabilitySet, len(tags))
	warnings := make([]string, len(tags))

	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, showConcurrency)
	)
	for i, tag := range tags {
		if tag.Capabilities != nil {
			sets[i] = modelCapabilities(tag.Capabilities, runtime.SourceModelMetadata, apiTagsPath)
			continue
		}
		if cached, ok := r.cachedShow(tag.Digest); ok {
			sets[i] = cached
			continue
		}

		wg.Add(1)
		go func(i int, tag tagModel) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				warnings[i] = fmt.Sprintf("model %q: capability lookup cancelled before %s", tag.Name, apiShowPath)
				return
			}

			caps, err := r.fetchShow(ctx, tag.Name)
			if err != nil {
				warnings[i] = fmt.Sprintf("model %q: %s failed, capabilities left unknown: %s",
					tag.Name, apiShowPath, oaibase.ErrorSummary(err))
				return
			}
			sets[i] = caps
			r.storeShow(tag.Digest, caps)
		}(i, tag)
	}
	wg.Wait()

	collected := make([]string, 0, len(tags))
	for _, w := range warnings {
		if w != "" {
			collected = append(collected, w)
		}
	}
	return sets, collected
}

func (r *Runtime) fetchShow(ctx context.Context, model string) (runtime.CapabilitySet, error) {
	var resp showResponse
	if err := r.base.Client().Do(ctx, "discover", http.MethodPost, apiShowPath, showRequest{Model: model}, &resp); err != nil {
		return nil, err
	}
	return modelCapabilities(resp.Capabilities, runtime.SourceModelMetadata, apiShowPath), nil
}

func (r *Runtime) cachedShow(digest string) (runtime.CapabilitySet, bool) {
	if digest == "" {
		return nil, false
	}
	r.showMu.Lock()
	defer r.showMu.Unlock()
	caps, ok := r.showCache[digest]
	return caps, ok
}

func (r *Runtime) storeShow(digest string, caps runtime.CapabilitySet) {
	if digest == "" {
		return
	}
	r.showMu.Lock()
	defer r.showMu.Unlock()
	r.showCache[digest] = caps
}

// modelCapabilities converts one model's Ollama capability list into
// evidence. The list is treated as exhaustive: every capability this
// package can map is recorded as supported or unsupported, never left
// unknown, so a chat-only model reports embeddings as unsupported rather
// than inheriting the server-level "embeddings endpoint exists".
func modelCapabilities(reported []string, source runtime.CapabilitySource, endpoint string) runtime.CapabilitySet {
	supported := make(map[runtime.Capability]bool)
	for _, name := range reported {
		for _, capability := range ollamaCapabilityMap[name] {
			supported[capability] = true
		}
	}

	set := make(runtime.CapabilitySet, len(mappedModelCapabilities))
	for _, capability := range mappedModelCapabilities {
		level := runtime.SupportUnsupported
		if supported[capability] {
			level = runtime.SupportSupported
		}
		set[capability] = runtime.CapabilityEvidence{
			Capability: capability,
			Level:      level,
			Source:     source,
			Detail:     fmt.Sprintf("model capability list from %s", endpoint),
		}
	}
	return set
}
