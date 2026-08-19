package runtime_test

import (
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/runtimetest"
)

func validDeps() runtime.Dependencies {
	return runtime.Dependencies{
		HTTPClient: &http.Client{},
		Clock:      runtimetest.NewClock(time.Now()),
		Logger:     slog.Default(),
		Metrics:    noopMetrics{},
	}
}

type noopMetrics struct{}

func (noopMetrics) Counter(string, map[string]string) runtime.Counter     { return noopInstrument{} }
func (noopMetrics) Gauge(string, map[string]string) runtime.Gauge         { return noopInstrument{} }
func (noopMetrics) Histogram(string, map[string]string) runtime.Histogram { return noopInstrument{} }

type noopInstrument struct{}

func (noopInstrument) Add(float64)     {}
func (noopInstrument) Set(float64)     {}
func (noopInstrument) Observe(float64) {}

func fakeFactory(rt *runtimetest.Runtime) runtime.Factory {
	return func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		return rt, nil
	}
}

func TestRegistryRegisterDuplicateKindReturnsError(t *testing.T) {
	r := runtime.NewRegistry()
	if err := r.Register(runtime.KindVLLM, fakeFactory(&runtimetest.Runtime{})); err != nil {
		t.Fatal(err)
	}
	err := r.Register(runtime.KindVLLM, fakeFactory(&runtimetest.Runtime{}))
	if !errors.Is(err, runtime.ErrFactoryAlreadyRegistered) {
		t.Fatalf("error = %v, want wrapping ErrFactoryAlreadyRegistered", err)
	}
}

func TestRegistryCreateUnknownKindReturnsError(t *testing.T) {
	r := runtime.NewRegistry()
	_, err := r.Create(runtime.Config{ID: "r1", Kind: runtime.KindVLLM, BaseURL: "http://example.com"}, validDeps())
	if !errors.Is(err, runtime.ErrRuntimeKindUnsupported) {
		t.Fatalf("error = %v, want wrapping ErrRuntimeKindUnsupported", err)
	}
}

func TestRegistryCreateRejectsInvalidConfigBeforeCallingFactory(t *testing.T) {
	r := runtime.NewRegistry()
	called := false
	r.Register(runtime.KindVLLM, func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		called = true
		return &runtimetest.Runtime{}, nil
	})

	_, err := r.Create(runtime.Config{Kind: runtime.KindVLLM}, validDeps()) // missing id and base_url
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorInvalidConfig {
		t.Fatalf("error = %v, want ErrorInvalidConfig", err)
	}
	if called {
		t.Fatal("factory must not be called when config validation fails")
	}
}

func TestRegistryCreateRejectsMissingDependencies(t *testing.T) {
	r := runtime.NewRegistry()
	r.Register(runtime.KindVLLM, fakeFactory(&runtimetest.Runtime{}))

	_, err := r.Create(runtime.Config{ID: "r1", Kind: runtime.KindVLLM, BaseURL: "http://example.com"}, runtime.Dependencies{})
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorInvalidConfig {
		t.Fatalf("error = %v, want ErrorInvalidConfig for missing dependencies", err)
	}
}

func TestRegistryCreatePassesNormalizedConfigToFactory(t *testing.T) {
	r := runtime.NewRegistry()
	var gotCfg runtime.Config
	r.Register(runtime.KindVLLM, func(cfg runtime.Config, deps runtime.Dependencies) (runtime.Runtime, error) {
		gotCfg = cfg
		return &runtimetest.Runtime{}, nil
	})

	_, err := r.Create(runtime.Config{ID: "r1", Kind: runtime.KindVLLM, BaseURL: "http://example.com/"}, validDeps())
	if err != nil {
		t.Fatal(err)
	}
	if gotCfg.BaseURL != "http://example.com" {
		t.Fatalf("factory received BaseURL %q, want normalized (trailing slash trimmed)", gotCfg.BaseURL)
	}
	if gotCfg.MaxConcurrent == 0 {
		t.Fatalf("factory received MaxConcurrent 0, want a normalized default")
	}
}

func TestRegistryKindsReturnsSortedRegisteredKinds(t *testing.T) {
	r := runtime.NewRegistry()
	r.Register(runtime.KindOllama, fakeFactory(&runtimetest.Runtime{}))
	r.Register(runtime.KindVLLM, fakeFactory(&runtimetest.Runtime{}))

	kinds := r.Kinds()
	if len(kinds) != 2 || kinds[0] != runtime.KindOllama || kinds[1] != runtime.KindVLLM {
		t.Fatalf("Kinds() = %v, want [ollama vllm] sorted", kinds)
	}
}
