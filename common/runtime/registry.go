package runtime

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Factory constructs a Runtime for a normalized, validated Config. It must
// only build a client — it must not start health-check goroutines or
// perform network I/O; Manager owns Probe/Discover/scheduling.
type Factory func(cfg Config, deps Dependencies) (Runtime, error)

// Registry constructs Runtime instances by Kind. The default Registry
// registers its four Factories at startup and is read-only afterward.
type Registry interface {
	Register(kind Kind, factory Factory) error
	Create(cfg Config, deps Dependencies) (Runtime, error)
	Kinds() []Kind
}

type registry struct {
	mu        sync.RWMutex
	factories map[Kind]Factory
}

// NewRegistry creates an empty Registry. Kinds must be registered with
// Register before Create can construct them.
func NewRegistry() Registry {
	return &registry{factories: make(map[Kind]Factory)}
}

// Register adds factory for kind. It returns a *RuntimeError with Code
// ErrorInvalidConfig wrapping ErrFactoryAlreadyRegistered if kind already
// has a factory.
func (r *registry) Register(kind Kind, factory Factory) error {
	if factory == nil {
		return &RuntimeError{
			Code:      ErrorInvalidConfig,
			Kind:      kind,
			Operation: "register_factory",
			Message:   "factory must not be nil",
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[kind]; exists {
		return &RuntimeError{
			Code:      ErrorInvalidConfig,
			Kind:      kind,
			Operation: "register_factory",
			Message:   fmt.Sprintf("factory for kind %q is already registered", kind),
			Cause:     ErrFactoryAlreadyRegistered,
		}
	}
	r.factories[kind] = factory
	return nil
}

// Create normalizes and validates cfg, checks that deps carries every
// dependency the Kind requires, and then invokes the registered Factory. It
// returns a *RuntimeError with Code ErrorInvalidConfig — wrapping
// ErrRuntimeKindUnsupported for an unregistered Kind, or describing missing
// dependencies otherwise — without ever calling the Factory in either case.
func (r *registry) Create(cfg Config, deps Dependencies) (Runtime, error) {
	normalized := cfg.Normalize()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	if err := validateDependencies(normalized.Kind, deps); err != nil {
		return nil, err
	}

	r.mu.RLock()
	factory, ok := r.factories[normalized.Kind]
	r.mu.RUnlock()
	if !ok {
		return nil, &RuntimeError{
			Code:      ErrorInvalidConfig,
			RuntimeID: normalized.ID,
			Kind:      normalized.Kind,
			Operation: "create_runtime",
			Message:   fmt.Sprintf("kind %q is not registered", normalized.Kind),
			Cause:     ErrRuntimeKindUnsupported,
		}
	}
	return factory(normalized, deps)
}

// Kinds returns every registered Kind, sorted for deterministic output.
func (r *registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]Kind, 0, len(r.factories))
	for k := range r.factories {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// validateDependencies checks that deps carries every collaborator its
// Kind needs. WSDialer is only required for KindComfyUI; every other field
// is required for all kinds, since Dependencies' zero value is documented
// as unusable.
func validateDependencies(kind Kind, deps Dependencies) error {
	var missing []string
	if deps.HTTPClient == nil {
		missing = append(missing, "HTTPClient")
	}
	if deps.Clock == nil {
		missing = append(missing, "Clock")
	}
	if deps.Logger == nil {
		missing = append(missing, "Logger")
	}
	if deps.Metrics == nil {
		missing = append(missing, "Metrics")
	}
	if kind == KindComfyUI && deps.WSDialer == nil {
		missing = append(missing, "WSDialer")
	}
	if len(missing) == 0 {
		return nil
	}
	return &RuntimeError{
		Code:      ErrorInvalidConfig,
		Kind:      kind,
		Operation: "create_runtime",
		Message:   fmt.Sprintf("missing required dependencies: %s", strings.Join(missing, ", ")),
	}
}
