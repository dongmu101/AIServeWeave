// Package modelroute defines the shared model-routing publication contract.
// modelroute 包定义共享的模型路由发布契约。
package modelroute

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// MaxRoutesBytes bounds the canonical routes array. / MaxRoutesBytes 限制规范化路由数组大小。
const MaxRoutesBytes = 1 << 20

// MaxDocumentBytes allows bounded metadata around a route array. / MaxDocumentBytes 为路由数组之外的元数据提供有界空间。
const MaxDocumentBytes = MaxRoutesBytes + 64*1024

// MaxRoutes caps aliases in a publication. / MaxRoutes 限制一次发布的别名数。
const MaxRoutes = 1000

// MaxTargets caps targets per alias. / MaxTargets 限制每个别名的目标数。
const MaxTargets = 100

// MaxRevisions caps retained immutable publications. / MaxRevisions 限制保留的不可变发布数量。
const MaxRevisions = 1000

// Target selects a real model and matching node labels. / Target 选择真实模型和匹配的节点标签。
type Target struct {
	RuntimeModel string            `json:"runtime_model"`
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	Priority     int               `json:"priority,omitempty"`
	Weight       int               `json:"weight,omitempty"`
}

// MatchesNode requires every selected label to exist and match. / MatchesNode 要求每个选择器标签均存在并匹配。
func (t Target) MatchesNode(labels map[string]string) bool {
	for key, want := range t.NodeSelector {
		got, exists := labels[key]
		if !exists || got != want {
			return false
		}
	}
	return true
}

// Route maps a logical model onto ordered targets. / Route 将逻辑模型映射到有序目标。
type Route struct {
	Model   string   `json:"model"`
	Targets []Target `json:"targets"`
}

// Validate checks one route without echoing configuration content. / Validate 检查单条路由，不回显配置内容。
func (r Route) Validate() error {
	if strings.TrimSpace(r.Model) == "" {
		return errors.New("modelroute: empty model")
	}
	if len(r.Targets) == 0 || len(r.Targets) > MaxTargets {
		return errors.New("modelroute: invalid target count")
	}
	for _, t := range r.Targets {
		if strings.TrimSpace(t.RuntimeModel) == "" || t.Weight < 0 || int64(t.Weight) > 9007199254740991 || int64(t.Priority) > 9007199254740991 || int64(t.Priority) < -9007199254740991 {
			return errors.New("modelroute: invalid target")
		}
		for k := range t.NodeSelector {
			if strings.TrimSpace(k) == "" {
				return errors.New("modelroute: empty selector label")
			}
		}
	}
	return nil
}

// Validate checks unique aliases, route structure and serialized size. / Validate 检查别名唯一性、结构及序列化大小。
func Validate(routes []Route) error {
	if len(routes) > MaxRoutes {
		return errors.New("modelroute: too many aliases")
	}
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		if err := r.Validate(); err != nil {
			return err
		}
		if seen[r.Model] {
			return errors.New("modelroute: duplicate alias")
		}
		seen[r.Model] = true
	}
	body, err := json.Marshal(routes)
	if err != nil || len(body) > MaxRoutesBytes {
		return errors.New("modelroute: route array exceeds limit")
	}
	return nil
}

// Canonical returns deterministic JSON, preserving target order within an alias. / Canonical 返回确定性 JSON，保留别名内目标顺序。
func Canonical(routes []Route) ([]byte, error) {
	if err := Validate(routes); err != nil {
		return nil, err
	}
	sorted := make([]Route, len(routes))
	copy(sorted, routes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Model < sorted[j].Model })
	return json.Marshal(sorted)
}

// Digest identifies route content independently of alias and map iteration order. / Digest 标识路由内容，不受别名及映射遍历顺序影响。
func Digest(routes []Route) (string, error) {
	body, err := Canonical(routes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// RevisionInfo is immutable publication metadata. / RevisionInfo 是不可变的发布元数据。
type RevisionInfo struct {
	Revision   int64     `json:"revision"`
	Digest     string    `json:"digest"`
	CreatedAt  time.Time `json:"created_at"`
	ActorID    string    `json:"actor_id"`
	RollbackOf int64     `json:"rollback_of,omitempty"`
}

// Snapshot carries one complete published routing table. / Snapshot 携带一份完整已发布路由表。
type Snapshot struct {
	RevisionInfo
	Routes []Route `json:"routes"`
}

// Applied reports the local effective version, never an inferred fleet acknowledgement. / Applied 报告本地生效版本，不推断机群确认。
type Applied struct {
	Mode      string    `json:"mode"`
	Revision  int64     `json:"revision"`
	Digest    string    `json:"digest"`
	AppliedAt time.Time `json:"applied_at"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

// ReplicaStatus is one Gateway's current routing observation. / ReplicaStatus 是单个 Gateway 当前的路由观测。
type ReplicaStatus struct {
	Applied
	ReplicaID   string    `json:"replica_id"`
	GeneratedAt time.Time `json:"generated_at"`
}
