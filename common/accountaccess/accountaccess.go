// Package accountaccess defines access to individually sold Agent runtimes.
// accountaccess 定义按账号售卖的 Agent 运行时访问规则，不包含登录凭据。
package accountaccess

import "time"

// MaxAccounts bounds inventory and every cached verification document.
const MaxAccounts = 1000

// Lease identifies a managed account. A zero expiry closes it to this caller.
type Lease struct {
	NodeID    string    `json:"node_id"`
	RuntimeID string    `json:"runtime_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Policy closes every managed account except the caller's unexpired leases.
// Only limits an account-monthly tenant to those leases, including on retries.
type Policy struct {
	Only    bool    `json:"only,omitempty"`
	Managed []Lease `json:"managed,omitempty"`
}

func (p Policy) Unrestricted() bool { return !p.Only && len(p.Managed) == 0 }

func (p Policy) Allows(nodeID, runtimeID string, now time.Time) bool {
	for _, lease := range p.Managed {
		if lease.NodeID == nodeID && lease.RuntimeID == runtimeID {
			return now.Before(lease.ExpiresAt)
		}
	}
	return !p.Only
}

func (p Policy) Active(now time.Time) bool {
	for _, lease := range p.Managed {
		if now.Before(lease.ExpiresAt) {
			return true
		}
	}
	return false
}

// NextMonth adds one calendar month in UTC, clamping to its final day.
func NextMonth(at time.Time) time.Time {
	at = at.UTC()
	y, m, d := at.Date()
	last := time.Date(y, m+2, 0, 0, 0, 0, 0, time.UTC).Day()
	return time.Date(y, m+1, min(d, last), at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), time.UTC)
}
