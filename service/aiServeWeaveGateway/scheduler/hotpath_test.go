package scheduler

import (
	"context"
	"math/rand"
	"sort"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// TestModelServesMatchesIntersectAndRequire holds modelServes to the
// definition it replaces. Every combination of "absent / unsupported / unknown
// / supported" on the runtime and on the model is tried, for the asked
// capability and for an unrelated one that must not leak into the answer.
//
// TestModelServesMatchesIntersectAndRequire 要求 modelServes 与它所取代的定义一致。运行时
// 与模型上「缺失 / 不支持 / 未知 / 支持」的每种组合都被尝试，既针对被问的能力，也针对一个
// 不相干、不得影响答案的能力。
func TestModelServesMatchesIntersectAndRequire(t *testing.T) {
	levels := []runtime.SupportLevel{"", runtime.SupportUnsupported, runtime.SupportUnknown, runtime.SupportSupported}
	set := func(chat, other runtime.SupportLevel) runtime.CapabilitySet {
		out := runtime.CapabilitySet{}
		if chat != "" {
			out[runtime.CapabilityChat] = runtime.CapabilityEvidence{Capability: runtime.CapabilityChat, Level: chat}
		}
		if other != "" {
			out[runtime.CapabilityEmbeddings] = runtime.CapabilityEvidence{Capability: runtime.CapabilityEmbeddings, Level: other}
		}
		return out
	}
	for _, rtChat := range levels {
		for _, rtOther := range levels {
			for _, mChat := range levels {
				for _, mOther := range levels {
					rt, m := set(rtChat, rtOther), set(mChat, mOther)
					for _, capability := range []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityEmbeddings, runtime.CapabilityRerank} {
						want := runtime.Intersect(rt, m).Require(capability) == nil
						if got := modelServes(rt, m, capability); got != want {
							t.Errorf("modelServes(runtime %v, model %v, %s) = %v, Intersect+Require says %v", rt, m, capability, got, want)
						}
					}
				}
			}
		}
	}
	if modelServes(nil, nil, runtime.CapabilityChat) {
		t.Error("modelServes with no evidence at all = true, want false")
	}
}

// TestStableSortMatchesSliceStable checks the index sort produces exactly
// sort.SliceStable's order, including among the many ties a fleet of
// identically loaded nodes produces.
//
// TestStableSortMatchesSliceStable 检查索引排序给出的顺序与 sort.SliceStable 完全相同，
// 包括负载相同的机群所产生的大量平局之间。
func TestStableSortMatchesSliceStable(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 2, 3, 7, 64, 801} {
		for _, spread := range []int{1, 2, 5, 1000} {
			found := make([]scored, n)
			for i := range found {
				found[i] = scored{
					Candidate: Candidate{NodeID: "n", RuntimeID: string(rune('a' + i%26)), Model: string(rune('A' + i/26))},
					idle:      rng.Intn(spread),
					queued:    rng.Intn(spread),
					inflight:  rng.Intn(spread),
				}
			}
			want := append([]scored(nil), found...)
			sort.SliceStable(want, func(i, j int) bool { return lessLoaded(want[i], want[j]) })

			stableSort(found, func(a, b *scored) bool { return lessLoaded(*a, *b) })
			for i := range want {
				if found[i] != want[i] {
					t.Fatalf("n=%d spread=%d: position %d = %+v, sort.SliceStable gives %+v", n, spread, i, found[i], want[i])
				}
			}
		}
	}
}

// TestWeightedCandidatesDeduplicatesOnlyWhenModelsRepeat covers both branches
// of the de-duplication: two targets with different models need none, and two
// targets resolving to the same runtime model must still not list a
// (node, runtime) twice.
//
// TestWeightedCandidatesDeduplicatesOnlyWhenModelsRepeat 覆盖去重的两个分支：两个 target
// 模型不同时无需去重；两个 target 解析到同一运行时模型时，仍然不得把同一个 (node, runtime)
// 列出两次。
func TestWeightedCandidatesDeduplicatesOnlyWhenModelsRepeat(t *testing.T) {
	c := func(node, model string) Candidate { return Candidate{NodeID: node, RuntimeID: "rt", Model: model} }
	first := func() float64 { return 0 }

	distinct := weightedCandidates([]targetCandidates{
		{priority: 0, weight: 1, candidates: []Candidate{c("a", "m1"), c("b", "m1")}},
		{priority: 1, weight: 1, candidates: []Candidate{c("a", "m2")}},
	}, first)
	if len(distinct) != 3 || distinct[0] != c("a", "m1") || distinct[1] != c("b", "m1") || distinct[2] != c("a", "m2") {
		t.Errorf("distinct models = %+v, want [a/m1 b/m1 a/m2] in priority order", distinct)
	}

	same := weightedCandidates([]targetCandidates{
		{priority: 0, weight: 1, candidates: []Candidate{c("a", "m1"), c("b", "m1")}},
		{priority: 1, weight: 1, candidates: []Candidate{c("b", "m1"), c("c", "m1")}},
	}, first)
	if len(same) != 3 || same[0] != c("a", "m1") || same[1] != c("b", "m1") || same[2] != c("c", "m1") {
		t.Errorf("repeated model = %+v, want [a/m1 b/m1 c/m1] with b listed once", same)
	}

	if got := weightedCandidates(nil, first); got != nil {
		t.Errorf("no groups = %+v, want nil", got)
	}
}

// TestHasCandidateAgreesWithCandidatesAndSharedViewMatchesFreshView checks
// that Models' shortcut (hasCandidate on one held view) says exactly what
// the full candidate computation on fresh views does, across a fleet where
// models, capabilities, breakers, draining and node selectors all matter.
//
// TestHasCandidateAgreesWithCandidatesAndSharedViewMatchesFreshView 检查 Models 的捷径
// （在持有的同一份视图上调用 hasCandidate）与在新视图上做完整候选计算给出的答案完全一致，
// 场景是一个模型、能力、熔断、draining 与节点选择器都起作用的机群。
func TestHasCandidateAgreesWithCandidatesAndSharedViewMatchesFreshView(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	snap := func(rt string, state runtime.State, models ...string) runtime.Snapshot {
		s := runtime.Snapshot{Descriptor: runtime.Descriptor{ID: rt, Kind: runtime.KindOllama}, State: state}
		for _, m := range models {
			s.Discovery.Models = append(s.Discovery.Models, runtime.Model{ID: m, Capabilities: runtime.CapabilitySet{
				runtime.CapabilityChat: {Level: runtime.SupportSupported},
			}})
		}
		return s
	}
	h.ConnectWithLabels(t, "n-chat", "rt", map[string]string{"zone": "a"}, snap("rt", runtime.StateHealthy, "chat-model"))
	h.ConnectWithLabels(t, "n-sick", "rt", map[string]string{"zone": "b"}, snap("rt", runtime.StateUnhealthy, "sick-model"))
	h.ConnectWithLabels(t, "n-tripped", "rt", nil, snap("rt", runtime.StateHealthy, "tripped-model"))

	table, err := routing.New([]routing.Route{
		{Model: "to-chat", Targets: []routing.Target{{RuntimeModel: "chat-model"}}},
		{Model: "to-zone-b", Targets: []routing.Target{{RuntimeModel: "chat-model", NodeSelector: map[string]string{"zone": "b"}}}},
		{Model: "to-zone-a", Targets: []routing.Target{{RuntimeModel: "chat-model", NodeSelector: map[string]string{"zone": "a"}}}},
		{Model: "to-sick", Targets: []routing.Target{{RuntimeModel: "sick-model"}}},
		{Model: "to-tripped", Targets: []routing.Target{{RuntimeModel: "tripped-model"}}},
		{Model: "fallback", Targets: []routing.Target{{RuntimeModel: "sick-model", Priority: 0}, {RuntimeModel: "chat-model", Priority: 1}}},
		{Model: "to-nothing", Targets: []routing.Target{{RuntimeModel: "no-such-model"}}},
	})
	if err != nil {
		t.Fatalf("routing.New: %v", err)
	}
	s := New(h.Srv, Config{Clock: h.Clock, Routes: table, FailureThreshold: 1})
	// Trip the breaker on n-tripped so a candidate exists but is excluded.
	s.breakers.record(Candidate{NodeID: "n-tripped", RuntimeID: "rt", Kind: runtime.KindOllama, Model: "tripped-model"},
		&runtime.RuntimeError{Code: runtime.ErrorConnection, Retryable: true}, h.Clock.Now())

	ctx := context.Background()
	view := s.acquireView()
	defer releaseView(view)
	for _, alias := range table.Models() {
		for _, capability := range []runtime.Capability{runtime.CapabilityChat, runtime.CapabilityMessages, runtime.CapabilityImageGeneration} {
			want := len(s.candidatesWith(ctx, table, alias, capability)) > 0
			if got := s.hasCandidate(ctx, table, alias, capability, view); got != want {
				t.Errorf("hasCandidate(%s, %s) = %v, candidates says %v", alias, capability, got, want)
			}
			if got := len(s.candidatesOn(ctx, table, alias, capability, affinity{}, rand.Float64, view)) > 0; got != want {
				t.Errorf("candidatesOn on a shared view (%s, %s) = %v, fresh view says %v", alias, capability, got, want)
			}
		}
	}

	got := map[string]bool{}
	for _, m := range s.Models(ctx) {
		got[m.ID] = true
	}
	for alias, want := range map[string]bool{
		"to-chat": true, "to-zone-a": true, "fallback": true,
		"to-zone-b": false, "to-sick": false, "to-tripped": false, "to-nothing": false,
	} {
		if got[alias] != want {
			t.Errorf("Models lists %q = %v, want %v", alias, got[alias], want)
		}
	}
}
