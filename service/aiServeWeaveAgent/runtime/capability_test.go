package runtime

import (
	"errors"
	"testing"
)

func TestMergePicksHighestPrioritySource(t *testing.T) {
	low := CapabilitySet{
		CapabilityChat: {Capability: CapabilityChat, Level: SupportUnsupported, Source: SourceRuntimeProfile},
	}
	high := CapabilitySet{
		CapabilityChat: {Capability: CapabilityChat, Level: SupportSupported, Source: SourceEndpoint},
	}

	merged := Merge(low, high)
	ev := merged.Resolve(CapabilityChat)
	if ev.Level != SupportSupported || ev.Source != SourceEndpoint {
		t.Fatalf("Merge() = %+v, want endpoint/supported to win over runtime_profile/unsupported", ev)
	}

	// Inputs must not be mutated by Merge.
	if low[CapabilityChat].Level != SupportUnsupported {
		t.Fatalf("Merge mutated its input: %+v", low[CapabilityChat])
	}
}

func TestMergeSameSourceConflictResolvesConservatively(t *testing.T) {
	a := CapabilitySet{
		CapabilityEmbeddings: {Capability: CapabilityEmbeddings, Level: SupportSupported, Source: SourceModelMetadata},
	}
	b := CapabilitySet{
		CapabilityEmbeddings: {Capability: CapabilityEmbeddings, Level: SupportUnsupported, Source: SourceModelMetadata},
	}

	merged := Merge(a, b)
	ev := merged.Resolve(CapabilityEmbeddings)
	if ev.Level != SupportUnsupported {
		t.Fatalf("same-source conflict resolved to %s, want %s (unsupported wins)", ev.Level, SupportUnsupported)
	}
	if ev.Detail == "" {
		t.Fatal("expected a conflict explanation in Detail")
	}
}

func TestMergeSameSourceSupportedBeatsUnknown(t *testing.T) {
	a := CapabilitySet{
		CapabilityVision: {Capability: CapabilityVision, Level: SupportUnknown, Source: SourceEndpoint},
	}
	b := CapabilitySet{
		CapabilityVision: {Capability: CapabilityVision, Level: SupportSupported, Source: SourceEndpoint},
	}

	merged := Merge(a, b)
	if got := merged.Resolve(CapabilityVision).Level; got != SupportSupported {
		t.Fatalf("got %s, want %s", got, SupportSupported)
	}
}

func TestMergeConfigOverrideWinsOverEverything(t *testing.T) {
	endpoint := CapabilitySet{
		CapabilityTools: {Capability: CapabilityTools, Level: SupportUnsupported, Source: SourceEndpoint},
	}
	override := CapabilitySet{
		CapabilityTools: {Capability: CapabilityTools, Level: SupportSupported, Source: SourceConfigOverride},
	}

	merged := Merge(endpoint, override)
	if got := merged.Resolve(CapabilityTools).Level; got != SupportSupported {
		t.Fatalf("got %s, want config_override (%s) to win", got, SupportSupported)
	}
}

func TestResolveMissingCapabilityIsUnknown(t *testing.T) {
	set := CapabilitySet{}
	ev := set.Resolve(CapabilityReasoning)
	if ev.Level != SupportUnknown {
		t.Fatalf("Resolve() of a missing capability = %s, want %s", ev.Level, SupportUnknown)
	}
}

func TestRequireSupportedReturnsNil(t *testing.T) {
	set := CapabilitySet{
		CapabilityChat: {Capability: CapabilityChat, Level: SupportSupported, Source: SourceEndpoint},
	}
	if err := set.Require(CapabilityChat); err != nil {
		t.Fatalf("Require() = %v, want nil", err)
	}
}

func TestRequireUnsupportedWrapsSentinel(t *testing.T) {
	set := CapabilitySet{
		CapabilityVision: {Capability: CapabilityVision, Level: SupportUnsupported, Source: SourceEndpoint},
	}
	err := set.Require(CapabilityVision)
	if !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("Require() error does not wrap ErrCapabilityUnsupported: %v", err)
	}
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != ErrorCapability {
		t.Fatalf("Require() error Code = %v, want %s", err, ErrorCapability)
	}
}

func TestRequireUnknownWrapsSentinel(t *testing.T) {
	set := CapabilitySet{}
	err := set.Require(CapabilityTools)
	if !errors.Is(err, ErrCapabilityUnknown) {
		t.Fatalf("Require() error does not wrap ErrCapabilityUnknown: %v", err)
	}
	var rtErr *RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != ErrorCapability {
		t.Fatalf("Require() error Code = %v, want %s", err, ErrorCapability)
	}
}

func TestIntersectRuntimeUnsupportedForcesModelUnsupported(t *testing.T) {
	runtimeCaps := CapabilitySet{
		CapabilityVision: {Capability: CapabilityVision, Level: SupportUnsupported, Source: SourceRuntimeProfile},
	}
	modelCaps := CapabilitySet{
		CapabilityVision: {Capability: CapabilityVision, Level: SupportSupported, Source: SourceModelMetadata},
	}

	result := Intersect(runtimeCaps, modelCaps)
	if got := result.Resolve(CapabilityVision).Level; got != SupportUnsupported {
		t.Fatalf("got %s, want %s (runtime-unsupported must dominate)", got, SupportUnsupported)
	}
}

func TestIntersectRuntimeUnknownUsesModelEvidence(t *testing.T) {
	runtimeCaps := CapabilitySet{
		CapabilityEmbeddings: {Capability: CapabilityEmbeddings, Level: SupportUnknown, Source: SourceRuntimeProfile},
	}
	modelCaps := CapabilitySet{
		CapabilityEmbeddings: {Capability: CapabilityEmbeddings, Level: SupportSupported, Source: SourceModelMetadata},
	}

	result := Intersect(runtimeCaps, modelCaps)
	if got := result.Resolve(CapabilityEmbeddings).Level; got != SupportSupported {
		t.Fatalf("got %s, want %s (model evidence used when runtime is unknown)", got, SupportSupported)
	}
}
