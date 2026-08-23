package runtimetest_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/runtimetest"
)

func TestRuntimeDefaultsAreUsableWithoutConfiguringAnyFunc(t *testing.T) {
	r := &runtimetest.Runtime{}
	ctx := context.Background()

	if _, err := r.Probe(ctx); err != nil {
		t.Fatalf("default Probe() error = %v, want nil", err)
	}
	health, err := r.Health(ctx)
	if err != nil || health.State != runtime.StateHealthy {
		t.Fatalf("default Health() = %+v, %v, want StateHealthy, nil", health, err)
	}
	if _, err := r.Discover(ctx); err != nil {
		t.Fatalf("default Discover() error = %v, want nil", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("default Close() error = %v, want nil", err)
	}
}

func TestRuntimeOverridesAreHonored(t *testing.T) {
	wantErr := errors.New("probe failed")
	r := &runtimetest.Runtime{
		DescriptorFunc: func() runtime.Descriptor { return runtime.Descriptor{ID: "r1", Kind: runtime.KindVLLM} },
		ProbeFunc: func(ctx context.Context) (runtime.ProbeResult, error) {
			return runtime.ProbeResult{}, wantErr
		},
	}

	if got := r.Descriptor(); got.ID != "r1" || got.Kind != runtime.KindVLLM {
		t.Fatalf("Descriptor() = %+v, want overridden values", got)
	}
	if _, err := r.Probe(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Probe() error = %v, want %v", err, wantErr)
	}
}

func TestRuntimeCloseCallsAreCounted(t *testing.T) {
	r := &runtimetest.Runtime{}
	if got := r.CloseCalls(); got != 0 {
		t.Fatalf("CloseCalls() before any Close = %d, want 0", got)
	}
	r.Close()
	r.Close()
	if got := r.CloseCalls(); got != 2 {
		t.Fatalf("CloseCalls() after two Close calls = %d, want 2", got)
	}
}
