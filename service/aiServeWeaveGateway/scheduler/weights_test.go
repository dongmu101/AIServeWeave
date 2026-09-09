package scheduler

import (
	"reflect"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

func TestWeightedTargetOrdering(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name   string
		groups []targetCandidates
		draw   float64
		want   []string
	}{
		{"larger share chosen first", []targetCandidates{{weight: 1, candidates: []Candidate{{Model: "a"}}}, {weight: 9, candidates: []Candidate{{Model: "b"}}}}, .5, []string{"b", "a"}},
		{"zero is one share", []targetCandidates{{weight: 0, candidates: []Candidate{{Model: "a"}}}, {weight: 1, candidates: []Candidate{{Model: "b"}}}}, .5, []string{"b", "a"}},
		{"priority dominates weight", []targetCandidates{{priority: 1, weight: 1, candidates: []Candidate{{Model: "a"}}}, {priority: 2, weight: 100, candidates: []Candidate{{Model: "b"}}}}, .99, []string{"a", "b"}},
		{"unavailable targets consume no share", []targetCandidates{{weight: 1000}, {weight: 1, candidates: []Candidate{{Model: "a"}}}, {weight: 9, candidates: []Candidate{{Model: "b"}}}}, .5, []string{"b", "a"}},
		{"preserve target load order", []targetCandidates{{weight: 1, candidates: []Candidate{{Model: "a1"}, {Model: "a2"}}}, {weight: 9, candidates: []Candidate{{Model: "b1"}, {Model: "b2"}}}}, .5, []string{"b1", "b2", "a1", "a2"}},
		{"sum cannot overflow", []targetCandidates{{weight: maxInt, candidates: []Candidate{{Model: "a"}}}, {weight: maxInt, candidates: []Candidate{{Model: "b"}}}}, .75, []string{"b", "a"}},
		{"deduplicate overlapping targets", []targetCandidates{{weight: 1, candidates: []Candidate{{Model: "a"}}}, {weight: 9, candidates: []Candidate{{Model: "a"}, {Model: "b"}}}}, .5, []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := weightedCandidates(tc.groups, func() float64 { return tc.draw })
			models := []string{}
			for _, c := range got {
				models = append(models, c.Model)
			}
			if !reflect.DeepEqual(models, tc.want) {
				t.Fatalf("want order %v, got %v", tc.want, models)
			}
		})
	}
}

func TestHotWeightUpdateChangesSelectionWithoutChangingCapturedSnapshot(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	for _, model := range []string{"a", "b"} {
		h.ConnectWithLabels(t, model, "backend", nil, runtime.Snapshot{Descriptor: runtime.Descriptor{ID: "backend", Kind: runtime.KindOllama}, State: runtime.StateHealthy, Discovery: runtime.Discovery{Models: []runtime.Model{{ID: model, Capabilities: runtime.CapabilitySet{runtime.CapabilityChat: {Level: runtime.SupportSupported}}}}}})
	}
	before, err := routing.New([]routing.Route{{Model: "alias", Targets: []routing.Target{{RuntimeModel: "a", Weight: 1}, {RuntimeModel: "b", Weight: 9}}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := routing.New([]routing.Route{{Model: "alias", Targets: []routing.Target{{RuntimeModel: "a", Weight: 9}, {RuntimeModel: "b", Weight: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	s := New(h.Srv, Config{Clock: h.Clock, Routes: before})
	captured := s.routes.Load()
	assertFirst := func(table *routing.Table, want string) {
		t.Helper()
		got := s.candidatesWithRandom(table, "alias", runtime.CapabilityChat, func() float64 { return .5 })
		if len(got) != 2 || got[0].Model != want {
			t.Fatalf("want first model %s and two fallback candidates, got %+v", want, got)
		}
	}
	assertFirst(s.routes.Load(), "b")
	s.SetRoutes(after)
	assertFirst(s.routes.Load(), "a")
	assertFirst(captured, "b")
}
