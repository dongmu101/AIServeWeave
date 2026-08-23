package runtimetest

import (
	"context"
	"sync"

	"AIServeWeave/common/runtime"
)

// Runtime is a scriptable runtime.Runtime: each method calls the
// corresponding *Func field if set, and otherwise returns a harmless
// default (StateHealthy for Health, zero values and a nil error
// everywhere else). Close is additionally counted so tests can assert it
// was called the expected number of times.
type Runtime struct {
	DescriptorFunc func() runtime.Descriptor
	ProbeFunc      func(ctx context.Context) (runtime.ProbeResult, error)
	HealthFunc     func(ctx context.Context) (runtime.HealthReport, error)
	DiscoverFunc   func(ctx context.Context) (runtime.Discovery, error)
	CloseFunc      func() error

	mu         sync.Mutex
	closeCalls int
}

func (r *Runtime) Descriptor() runtime.Descriptor {
	if r.DescriptorFunc != nil {
		return r.DescriptorFunc()
	}
	return runtime.Descriptor{}
}

func (r *Runtime) Probe(ctx context.Context) (runtime.ProbeResult, error) {
	if r.ProbeFunc != nil {
		return r.ProbeFunc(ctx)
	}
	return runtime.ProbeResult{}, nil
}

func (r *Runtime) Health(ctx context.Context) (runtime.HealthReport, error) {
	if r.HealthFunc != nil {
		return r.HealthFunc(ctx)
	}
	return runtime.HealthReport{State: runtime.StateHealthy}, nil
}

func (r *Runtime) Discover(ctx context.Context) (runtime.Discovery, error) {
	if r.DiscoverFunc != nil {
		return r.DiscoverFunc(ctx)
	}
	return runtime.Discovery{}, nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	r.closeCalls++
	r.mu.Unlock()
	if r.CloseFunc != nil {
		return r.CloseFunc()
	}
	return nil
}

// CloseCalls reports how many times Close has been called.
func (r *Runtime) CloseCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeCalls
}

var _ runtime.Runtime = (*Runtime)(nil)
