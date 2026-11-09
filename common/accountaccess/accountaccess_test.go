package accountaccess_test

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/common/nodeaccess"
	"testing"
	"time"
)

func TestMonthlyExpiryAndIsolation(t *testing.T) {
	for _, test := range []struct{ start, end string }{
		{"2026-01-31T12:30:00Z", "2026-02-28T12:30:00Z"},
		{"2028-01-31T12:30:00Z", "2028-02-29T12:30:00Z"},
		{"2026-12-15T12:30:00Z", "2027-01-15T12:30:00Z"},
	} {
		at, _ := time.Parse(time.RFC3339, test.start)
		if got := accountaccess.NextMonth(at).Format(time.RFC3339); got != test.end {
			t.Fatalf("%s => %s, want %s", test.start, got, test.end)
		}
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := accountaccess.Policy{Managed: []accountaccess.Lease{{NodeID: "agent", RuntimeID: "mine", ExpiresAt: now.Add(time.Hour)}, {NodeID: "agent", RuntimeID: "other"}}}
	if !p.Allows("agent", "mine", now) || p.Allows("agent", "other", now) || !p.Allows("agent", "shared", now) {
		t.Fatal("runtime isolation failed")
	}
	p.Only = true
	if p.Allows("agent", "shared", now) || p.Allows("other-agent", "mine", now) {
		t.Fatal("monthly mode escaped purchased accounts")
	}
	if p.Allows("agent", "mine", now.Add(time.Hour)) || p.Active(now.Add(time.Hour)) {
		t.Fatal("cached policy survived expiry")
	}
}

func TestPrivateAgentAllowsOnlyAnExactPaidAccount(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	nodes := nodeaccess.Policy{DefaultDeny: true}
	p := accountaccess.Policy{Managed: []accountaccess.Lease{{NodeID: "private-agent", RuntimeID: "codex", ExpiresAt: now.Add(time.Hour)}}}
	if !p.Permits("buyer", nodes, "private-agent", "codex", now) {
		t.Fatal("paid account was denied")
	}
	if p.Permits("buyer", nodes, "private-agent", "claude", now) || p.Permits("buyer", nodes, "new-agent", "codex", now) {
		t.Fatal("lease opened unpurchased resources")
	}
	if p.Permits("buyer", nodes, "private-agent", "codex", now.Add(time.Hour)) {
		t.Fatal("expired lease opened a private account")
	}
	nodes.Excluded = []string{"private-agent"}
	if p.Permits("buyer", nodes, "private-agent", "codex", now) {
		t.Fatal("lease bypassed another tenant's dedicated node")
	}
}
