// Package nodeaccess is the contract between the control plane, which stores
// which tenants a dedicated node belongs to, and the Gateway, which keeps every
// other tenant's work off that node. Like common/modelaccess it holds the data
// and its meaning and nothing that enforces anything.
//
// A node with no tenants bound to it is shared, which is what every node is
// before anyone configures this feature: a deployment that upgrades into it
// must not start refusing yesterday's traffic. A node bound to one or more
// tenants is dedicated: those tenants may use it, and no other tenant may. The
// bound tenants keep every shared node as well; a binding only closes a node
// to the rest.
//
// Bindings name a node by its node_id, the identity the Registry certified,
// never by a label: labels are what an Agent says about itself, and an Agent
// that could claim a label could claim another tenant's hardware.
//
// A tenant-owned node, one a tenant enrolled itself, is a third kind: its
// node_id names its owner (TenantNodePrefix), and no binding can open it to
// anyone else.
//
// nodeaccess 包是控制面（存储一台专属节点属于哪些租户）与 Gateway（让其他租户的
// 工作远离这台节点）之间的契约。与 common/modelaccess 一样，它只保存数据及其含义，
// 不保存任何执行逻辑。
//
// 没有绑定任何租户的节点是共享节点，这也是本功能被配置之前每台节点的状态：升级到
// 本功能的部署，绝不能开始拒绝它昨天还接受的流量。绑定了一个或多个租户的节点是
// 专属节点：这些租户可以使用它，其他租户都不可以。被绑定的租户仍然保留所有共享节点；
// 绑定只是对其余租户关闭这台节点。
//
// 绑定按节点的 node_id（Registry 认证过的身份）指名，绝不按标签：标签是 Agent 对
// 自己的描述，一个能声称某个标签的 Agent，也就能声称另一个租户的硬件。
//
// 商户自有节点（商户自己接入的节点）是第三种：它的 node_id 写明了所有者
// （TenantNodePrefix），任何绑定都不能把它开放给别人。
package nodeaccess

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDedicatedNodes caps how many nodes may be dedicated at once. Every key
// verification carries the nodes closed to its tenant, so the bound keeps that
// payload, and the rows read to compute it, small.
//
// MaxDedicatedNodes 限制同时存在的专属节点数。每次 Key 校验都携带对其租户关闭的
// 节点，因此这个上限让该载荷以及计算它所读取的行数保持很小。
const MaxDedicatedNodes = 1000

// MaxTenantsPerNode caps the tenants one node may be bound to. A node shared by
// more tenants than this is a shared node in all but name.
//
// MaxTenantsPerNode 限制一台节点可绑定的租户数。被比这还多的租户共用的节点，除了
// 名字以外就是一台共享节点。
const MaxTenantsPerNode = 64

// MaxIDBytes bounds one node or tenant id. Real ids are far shorter; the bound
// exists so a binding cannot be used to park arbitrary text in the database and
// in every cached key verification.
//
// MaxIDBytes 限制单个节点或租户 id 的长度。真实 id 远短于它；设置这个上限，是为了
// 不让一条绑定被用来往数据库和每一条缓存的 Key 校验结果里塞任意文本。
const MaxIDBytes = 128

// MaxDocumentBytes bounds a request body carrying one node's tenant list:
// MaxTenantsPerNode quoted ids of MaxIDBytes with separators, plus room for
// the enclosing object, rounded up.
//
// MaxDocumentBytes 限制携带一台节点租户列表的请求体大小：MaxTenantsPerNode 个带
// 引号与分隔符、长度为 MaxIDBytes 的 id，加上外层对象的空间，再向上取整。
const MaxDocumentBytes = 16 << 10

// Policy is what applies to one tenant: the dedicated nodes bound to other
// tenants and not to it. The Gateway needs only this to decide, and a policy
// with nothing excluded is exactly today's behavior.
//
// Policy 是作用于某个租户的授权：绑定给其他租户、而不包括它的那些专属节点。Gateway
// 做判断只需要这一份，而一个什么都不排除的 policy 恰好就是现在的行为。
type Policy struct {
	Excluded []string `json:"excluded,omitempty"`
}

// Unrestricted reports whether no node is closed to the tenant, which lets a
// caller skip filtering entirely.
//
// Unrestricted 报告是否没有任何节点对该租户关闭，调用方据此可以整个跳过过滤。
func (p Policy) Unrestricted() bool {
	return len(p.Excluded) == 0
}

// Allows reports whether the tenant may use nodeID under p.
//
// Allows 报告在 p 之下该租户是否可以使用 nodeID。
func (p Policy) Allows(nodeID string) bool {
	return !slices.Contains(p.Excluded, nodeID)
}

// Binding is one dedicated node and the tenants it belongs to.
//
// Binding 是一台专属节点及其所属的租户。
type Binding struct {
	NodeID  string
	Tenants []string
}

// PolicyFor derives tenantID's policy from every stored binding. The result is
// sorted so two derivations from the same bindings cache and compare equal.
//
// PolicyFor 由全部已存储的绑定推导 tenantID 的 policy。结果已排序，使同一组绑定的两次
// 推导在缓存与比较中完全一致。
func PolicyFor(bindings []Binding, tenantID string) Policy {
	var excluded []string
	for _, binding := range bindings {
		if len(binding.Tenants) == 0 || slices.Contains(binding.Tenants, tenantID) {
			continue
		}
		excluded = append(excluded, binding.NodeID)
	}
	sort.Strings(excluded)
	return Policy{Excluded: excluded}
}

// ValidNodeID reports whether id is acceptable as a node id in a binding: non
// empty, at most MaxIDBytes, valid UTF-8, and free of whitespace and control
// characters, which no Registry-issued id contains and which would make an id
// read differently in a URL path, a log line and a database row.
//
// ValidNodeID 报告 id 是否可以作为绑定中的节点 id：非空、不超过 MaxIDBytes、是合法
// UTF-8，且不含空白与控制字符——Registry 签发的 id 从不包含它们，而它们会让同一个
// id 在 URL 路径、日志行与数据库行里读起来各不相同。
func ValidNodeID(id string) bool {
	return validID(id)
}

// NormalizeTenants validates a tenant list as an operator submitted it and
// returns its canonical form: trimmed, deduplicated and sorted, so two
// submissions naming the same set store and audit identically. An empty or nil
// input returns an empty, non-nil list, which means "make this node shared".
//
// NormalizeTenants 校验运维提交的一份租户列表并返回其规范形式：去除首尾空白、去重
// 并排序，使两次指定同一集合的提交在存储与审计中完全一致。空输入或 nil 输入返回一个
// 空的非 nil 列表，表示「让这台节点恢复共享」。
func NormalizeTenants(tenants []string) ([]string, error) {
	out := make([]string, 0, len(tenants))
	seen := make(map[string]bool, len(tenants))
	for _, raw := range tenants {
		tenant := strings.TrimSpace(raw)
		if !validID(tenant) {
			return nil, errors.New("nodeaccess: invalid tenant id")
		}
		if seen[tenant] {
			continue
		}
		seen[tenant] = true
		out = append(out, tenant)
	}
	if len(out) > MaxTenantsPerNode {
		return nil, errors.New("nodeaccess: too many tenants for one node")
	}
	sort.Strings(out)
	return out, nil
}

// TenantNodePrefix starts the node_id of every tenant-owned node: a machine a
// tenant enrolled itself, which only that tenant may use. The full form is
// "tnode.<tenant_id>.<32 lowercase hex digits>". Ownership is read from the id
// alone, which the Registry certifies, so deciding it needs no list that grows
// with the number of tenants and no data that could be stale when a new node
// first connects.
//
// TenantNodePrefix 是每台商户自有节点 node_id 的开头：商户自己接入、只有该商户能用的
// 机器。完整形式为 "tnode.<tenant_id>.<32 位小写十六进制>"。归属只从 Registry 认证过的
// id 读出，因此判断它既不需要一份随租户数增长的名单，也不依赖新节点首次连上时可能尚未
// 同步的数据。
const TenantNodePrefix = "tnode."

// tenantNodeSuffixLen is the length of a tenant-owned id's random part.
//
// tenantNodeSuffixLen 是商户自有节点 id 随机部分的长度。
const tenantNodeSuffixLen = 32

// NewTenantNodeID mints a fresh node_id owned by tenantID. It fails when
// tenantID could not be read back out of the id: invalid as an id, containing
// a '.', or too long for the result to fit MaxIDBytes.
//
// NewTenantNodeID 生成一个归 tenantID 所有的新 node_id。tenantID 无法从结果中原样读回
// 时失败：它不是合法 id、含有 '.'，或长到结果超出 MaxIDBytes。
func NewTenantNodeID(tenantID string) (string, error) {
	if !validID(tenantID) || strings.Contains(tenantID, ".") ||
		len(TenantNodePrefix)+len(tenantID)+1+tenantNodeSuffixLen > MaxIDBytes {
		return "", errors.New("nodeaccess: tenant id cannot own a node")
	}
	var random [tenantNodeSuffixLen / 2]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("nodeaccess: random source failed")
	}
	return TenantNodePrefix + tenantID + "." + hex.EncodeToString(random[:]), nil
}

// OwnerOf reports whether nodeID is tenant-owned and, if so, which tenant owns
// it. An id that carries TenantNodePrefix but is otherwise malformed is owned
// with an empty owner, so no tenant may use it: a reserved prefix must never
// fall back to meaning "shared".
//
// OwnerOf 报告 nodeID 是否为商户自有节点，是则返回所属商户。带有 TenantNodePrefix 但
// 其余部分不合格式的 id 视为自有、所属商户为空，因而没有任何商户能用：保留前缀绝不能
// 退化成「共享」。
func OwnerOf(nodeID string) (tenantID string, owned bool) {
	rest, ok := strings.CutPrefix(nodeID, TenantNodePrefix)
	if !ok {
		return "", false
	}
	if len(nodeID) > MaxIDBytes {
		return "", true
	}
	dot := strings.LastIndexByte(rest, '.')
	if dot <= 0 {
		return "", true
	}
	tenant, suffix := rest[:dot], rest[dot+1:]
	if strings.Contains(tenant, ".") || !validID(tenant) || !lowerHex(suffix, tenantNodeSuffixLen) {
		return "", true
	}
	return tenant, true
}

// Permits is the whole node rule for one tenant: a tenant-owned node admits
// only its owner, and any other node is judged by the tenant's dedicated-node
// policy.
//
// Permits 是对一个商户的完整节点规则：商户自有节点只允许其所有者使用，其他节点按该
// 商户的专属节点 policy 判断。
func Permits(tenantID string, policy Policy, nodeID string) bool {
	if owner, owned := OwnerOf(nodeID); owned {
		return owner != "" && owner == tenantID
	}
	return policy.Allows(nodeID)
}

func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validID(id string) bool {
	if id == "" || len(id) > MaxIDBytes || !utf8.ValidString(id) {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
