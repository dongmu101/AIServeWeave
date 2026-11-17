package types

import "time"

// PriceCatalogPrice is one official unit price, in micro-credits per million
// units of its meter (1 credit = 1 USD). ValidFrom is when it took effect.
//
// PriceCatalogPrice 是一条官方单价，单位为「micro 点 / 该计量项的百万单位」（1 点 = 1 美元）。
// ValidFrom 是它生效的时间。
type PriceCatalogPrice struct {
	Meter            string     `json:"meter"`
	Variant          string     `json:"variant,omitempty"`
	AboveInputTokens int64      `json:"above_input_tokens,omitempty"`
	PriceMicroPerM   int64      `json:"price_micro_per_m"`
	Derived          bool       `json:"derived,omitempty"`
	ValidFrom        *time.Time `json:"valid_from,omitempty"`
}

// PriceCatalogEntry is one price catalog entry with its prices in effect.
// Pending is the kind of review waiting on it ("change" or "remove").
//
// PriceCatalogEntry 是一个价目表条目及其生效中的单价。Pending 是等待中的审核类型（"change"
// 或 "remove"）。
type PriceCatalogEntry struct {
	Model     string              `json:"model"`
	Provider  string              `json:"provider"`
	Mode      string              `json:"mode"`
	Status    string              `json:"status"`
	Pending   string              `json:"pending,omitempty"`
	Prices    []PriceCatalogPrice `json:"prices"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// PriceCatalogSyncState is the catalog's sync state as the operator sees it.
//
// PriceCatalogSyncState 是运维看到的价目表同步状态。
type PriceCatalogSyncState struct {
	Source      string     `json:"source"`
	EntryCount  int64      `json:"entry_count"`
	Version     int64      `json:"version"`
	SyncedAt    *time.Time `json:"synced_at,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
}

// PriceCatalogListResponse is a page of entries and the sync state.
//
// PriceCatalogListResponse 是一页条目与同步状态。
type PriceCatalogListResponse struct {
	Items      []PriceCatalogEntry   `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
	Sync       PriceCatalogSyncState `json:"sync"`
}

// PriceCatalogPendingItem is one change waiting for review. MaxChangeBP is the
// largest relative change in basis points, null when unbounded (a price
// rising from zero or with nothing to compare with) or for a removal.
// Proposed is empty for a removal.
//
// PriceCatalogPendingItem 是一个等待审核的变动。MaxChangeBP 是最大的相对变动（基点）；无界
// （单价从 0 上涨或没有可比较的原价）或删除时为 null。删除时 Proposed 为空。
type PriceCatalogPendingItem struct {
	Model       string              `json:"model"`
	Kind        string              `json:"kind"`
	Status      string              `json:"status"`
	MaxChangeBP *int64              `json:"max_change_bp"`
	DetectedAt  time.Time           `json:"detected_at"`
	Current     []PriceCatalogPrice `json:"current"`
	Proposed    []PriceCatalogPrice `json:"proposed,omitempty"`
}

// PriceCatalogPendingListResponse is a page of waiting changes.
//
// PriceCatalogPendingListResponse 是一页等待中的变动。
type PriceCatalogPendingListResponse struct {
	Items      []PriceCatalogPendingItem `json:"items"`
	NextCursor string                    `json:"next_cursor,omitempty"`
}

// PriceCatalogChange names an entry and its largest relative change in basis
// points (null when unbounded).
//
// PriceCatalogChange 指出一个条目及其最大的相对变动（基点；无界时为 null）。
type PriceCatalogChange struct {
	Model       string `json:"model"`
	MaxChangeBP *int64 `json:"max_change_bp"`
}

// PriceCatalogSyncResponse is what a manual sync did. Skipped means the upstream
// content was already applied.
//
// PriceCatalogSyncResponse 是一次手动同步所做的事。Skipped 表示上游内容已经采用过。
type PriceCatalogSyncResponse struct {
	Skipped    bool                 `json:"skipped"`
	Added      []string             `json:"added"`
	Applied    []PriceCatalogChange `json:"applied"`
	Reviewed   []PriceCatalogChange `json:"reviewed"`
	Removed    []string             `json:"removed"`
	Suppressed int                  `json:"suppressed"`
	Version    int64                `json:"version"`
}

// PriceCatalogDecisionRequest names the entry an approval or rejection is for.
// The name travels in the body because upstream names contain "/".
//
// PriceCatalogDecisionRequest 指出一次批准或拒绝针对的条目。名称放在请求体里，因为上游名称
// 含有 "/"。
type PriceCatalogDecisionRequest struct {
	Model string `json:"model"`
}

// PriceCatalogDecisionResponse is what an approval or rejection did.
//
// PriceCatalogDecisionResponse 是一次批准或拒绝所做的事。
type PriceCatalogDecisionResponse struct {
	Model   string              `json:"model"`
	Kind    string              `json:"kind"`
	Version int64               `json:"version"`
	Before  []PriceCatalogPrice `json:"before"`
	After   []PriceCatalogPrice `json:"after"`
}
