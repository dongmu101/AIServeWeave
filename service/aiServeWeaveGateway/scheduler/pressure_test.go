package scheduler

import "testing"

func pressureCandidates() []scored {
	return []scored{
		{Candidate: Candidate{NodeID: "busy-pressured"}, idle: 9, pressure: true},
		{Candidate: Candidate{NodeID: "a"}, idle: 2},
		{Candidate: Candidate{NodeID: "pressured-2"}, idle: 5, pressure: true},
		{Candidate: Candidate{NodeID: "b"}, idle: 1},
	}
}

func ids(found []scored) []string {
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.NodeID
	}
	return out
}

func TestEveryPolicyTriesPressuredCandidatesLast(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
		key    affinity
	}{
		{"least load", PolicyLeastLoad, affinity{}},
		{"power of two", PolicyPowerOfTwo, affinity{}},
		{"prefix hash", PolicyPrefixHash, affinity{hash: 42, ok: true}},
		{"session key", PolicyLeastLoad, affinity{hash: 7, ok: true, session: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := pressureCandidates()
			orderCandidates(found, tc.policy, tc.key, func(n int) int { return n - 1 })
			got := ids(found)
			for i, f := range found {
				if f.pressure && i < 2 {
					t.Fatalf("order %v: a pressured candidate is ahead of an unpressured one", got)
				}
			}
			if len(found) != 4 {
				t.Fatalf("candidates lost: %v", got)
			}
		})
	}
}

func TestLeastLoadKeepsLoadOrderWithinPressureGroups(t *testing.T) {
	found := pressureCandidates()
	orderCandidates(found, PolicyLeastLoad, affinity{}, func(int) int { return 0 })
	want := []string{"a", "b", "busy-pressured", "pressured-2"}
	got := ids(found)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestPressureLastWithoutPressureIsANoOp(t *testing.T) {
	found := []scored{{Candidate: Candidate{NodeID: "x"}}, {Candidate: Candidate{NodeID: "y"}}}
	pressureLast(found)
	if found[0].NodeID != "x" || found[1].NodeID != "y" {
		t.Fatalf("order changed: %v", ids(found))
	}
	if allocs := testing.AllocsPerRun(100, func() { pressureLast(found) }); allocs != 0 {
		t.Fatalf("pressureLast allocated %v times with no pressured candidate", allocs)
	}
}
