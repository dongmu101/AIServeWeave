package runtime

import "fmt"

// Capability names a single, independently verifiable feature a Runtime or
// Model may support. Adapters must use these constants rather than string
// literals so the capability vocabulary stays closed.
type Capability string

const (
	CapabilityChat              Capability = "chat"
	CapabilityChatStream        Capability = "chat_stream"
	CapabilityCompletions       Capability = "completions"
	CapabilityEmbeddings        Capability = "embeddings"
	CapabilityResponses         Capability = "responses"
	CapabilityVision            Capability = "vision"
	CapabilityTools             Capability = "tools"
	CapabilityParallelToolCalls Capability = "parallel_tool_calls"
	CapabilityStructuredOutput  Capability = "structured_output"
	CapabilityReasoning         Capability = "reasoning"
	CapabilityWorkflowExecution Capability = "workflow_execution"
	CapabilityWorkflowEvents    Capability = "workflow_events"
	CapabilityWorkflowCancel    Capability = "workflow_cancel"
	CapabilityArtifactRead      Capability = "artifact_read"
)

// CapabilitySource records how a piece of capability evidence was obtained,
// used to rank conflicting evidence for the same Capability.
type CapabilitySource string

const (
	SourceEndpoint       CapabilitySource = "endpoint"
	SourceModelMetadata  CapabilitySource = "model_metadata"
	SourceRuntimeProfile CapabilitySource = "runtime_profile"
	SourceConfigOverride CapabilitySource = "config_override"
)

// sourcePriority ranks CapabilitySource from most to least trusted:
// config_override > endpoint > model_metadata > runtime_profile.
var sourcePriority = map[CapabilitySource]int{
	SourceConfigOverride: 4,
	SourceEndpoint:       3,
	SourceModelMetadata:  2,
	SourceRuntimeProfile: 1,
}

type SupportLevel string

const (
	SupportUnknown     SupportLevel = "unknown"
	SupportSupported   SupportLevel = "supported"
	SupportUnsupported SupportLevel = "unsupported"
)

// levelRank orders SupportLevel for conservative same-source conflict
// resolution: unsupported beats supported beats unknown.
func levelRank(l SupportLevel) int {
	switch l {
	case SupportUnsupported:
		return 2
	case SupportSupported:
		return 1
	default:
		return 0
	}
}

type CapabilityEvidence struct {
	Capability Capability
	Level      SupportLevel
	Source     CapabilitySource
	Detail     string
}

// CapabilitySet is the full, evidence-backed capability picture for a
// runtime instance or a single model. A missing entry means unknown, not
// unsupported — always read through Resolve rather than a map index.
type CapabilitySet map[Capability]CapabilityEvidence

// Resolve returns the final evidence for c. A capability absent from s is
// reported as SupportUnknown rather than zero-valued, so callers never
// mistake "no evidence" for "explicitly unsupported".
func (s CapabilitySet) Resolve(c Capability) CapabilityEvidence {
	if ev, ok := s[c]; ok {
		return ev
	}
	return CapabilityEvidence{Capability: c, Level: SupportUnknown}
}

// Require is the call-time gate: it returns nil when c is supported, and
// otherwise a *RuntimeError with Code ErrorCapability wrapping
// ErrCapabilityUnsupported or ErrCapabilityUnknown depending on which the
// evidence indicates.
func (s CapabilitySet) Require(c Capability) error {
	ev := s.Resolve(c)
	switch ev.Level {
	case SupportSupported:
		return nil
	case SupportUnsupported:
		return &RuntimeError{
			Code:      ErrorCapability,
			Operation: "capability_check",
			Message:   fmt.Sprintf("capability %q is not supported", c),
			Cause:     ErrCapabilityUnsupported,
		}
	default:
		return &RuntimeError{
			Code:      ErrorCapability,
			Operation: "capability_check",
			Message:   fmt.Sprintf("capability %q support is unknown", c),
			Cause:     ErrCapabilityUnknown,
		}
	}
}

// Merge combines capability sets from possibly many sources into one,
// keeping for each Capability the evidence from the highest-priority
// CapabilitySource. When two inputs disagree at the same priority, the
// result converges conservatively (unsupported > supported > unknown) and
// the winning evidence's Detail is annotated with the conflict so callers
// can surface it (e.g. into Discovery.Warnings). Inputs are never mutated.
func Merge(sets ...CapabilitySet) CapabilitySet {
	merged := make(CapabilitySet)
	rank := make(map[Capability]int, len(merged))

	for _, set := range sets {
		for cap, ev := range set {
			cur, exists := merged[cap]
			if !exists {
				merged[cap] = ev
				rank[cap] = sourcePriority[ev.Source]
				continue
			}
			curRank := rank[cap]
			evRank := sourcePriority[ev.Source]
			switch {
			case evRank > curRank:
				merged[cap] = ev
				rank[cap] = evRank
			case evRank < curRank:
				// Lower-priority source; ignore.
			default:
				merged[cap] = resolveConflict(cap, cur, ev)
			}
		}
	}
	return merged
}

func resolveConflict(cap Capability, a, b CapabilityEvidence) CapabilityEvidence {
	if a.Level == b.Level {
		return a
	}
	winner := a
	if levelRank(b.Level) > levelRank(a.Level) {
		winner = b
	}
	winner.Detail = fmt.Sprintf(
		"conflicting %s evidence for %q: %s vs %s; resolved to %s (conservative)",
		winner.Source, cap, a.Level, b.Level, winner.Level,
	)
	return winner
}

// Intersect combines a runtime-level capability set with a model-level
// capability set: a runtime-unsupported capability stays unsupported
// regardless of model evidence, an unknown-at-runtime capability defers to
// the model's evidence, and a runtime-only capability the model has no
// opinion on passes through unchanged.
func Intersect(runtimeCaps, modelCaps CapabilitySet) CapabilitySet {
	result := make(CapabilitySet, len(runtimeCaps)+len(modelCaps))
	for cap, modelEv := range modelCaps {
		if runtimeEv, ok := runtimeCaps[cap]; ok && runtimeEv.Level == SupportUnsupported {
			result[cap] = runtimeEv
			continue
		}
		result[cap] = modelEv
	}
	for cap, runtimeEv := range runtimeCaps {
		if _, exists := result[cap]; !exists {
			result[cap] = runtimeEv
		}
	}
	return result
}
