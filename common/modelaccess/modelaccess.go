// Package modelaccess is the contract between the control plane, which stores
// which model aliases a tenant may see and call, and the Gateway, which
// enforces it. Like common/quota it holds the data and its meaning and nothing
// that enforces anything: two services must read "an empty list" the same way,
// or one of them will hide what the other one serves.
//
// Access is granted at two levels, and both must allow a model:
//
//   - Platform, set once for the whole deployment. It is the ceiling: an alias
//     left off it is unavailable to every tenant, which is what "take this
//     model off the platform" needs to mean without editing each tenant.
//   - Tenant, set per tenant. It narrows the platform list for one tenant and
//     can never widen it.
//
// At either level an empty list means "no restriction at this level", which is
// also what every tenant gets before anyone configures this feature: a
// deployment that upgrades into it must not start refusing yesterday's traffic.
// The names are modelroute aliases, the stable public names callers send; a
// deployment without a routing table uses the node-reported model IDs as its
// aliases, and the same lists apply to those.
//
// modelaccess 包是控制面（存储一个租户可以看到并调用哪些模型别名）与 Gateway
// （执行它）之间的契约。与 common/quota 一样，它只保存数据及其含义，不保存任何
// 执行逻辑：两个服务必须以同一种方式理解「空列表」，否则一边会把另一边正在提供的
// 东西藏起来。
//
// 授权分两级，模型必须同时被两级允许：
//
//   - 平台级，整个部署一份。它是上限：不在其中的别名对所有租户都不可用——「把这个
//     模型从平台下架」就该是这个意思，而不必逐个租户去改。
//   - 租户级，每个租户一份。它在平台列表的基础上为某个租户再收窄，永远不能放宽。
//
// 任一级的空列表都表示「这一级不限制」，这也是本功能被配置之前每个租户的状态：
// 升级到本功能的部署，绝不能开始拒绝它昨天还接受的流量。列表里的名字是 modelroute
// 的别名，即调用方发送的、稳定的对外名称；没有路由表的部署把节点上报的模型 ID 当作
// 别名，同样的列表对它们一样生效。
package modelaccess

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"AIServeWeave/common/modelroute"
)

// MaxModels caps one list at the number of aliases a routing publication may
// hold: a list longer than every alias that can exist only names models that
// cannot exist.
//
// MaxModels 把一份列表的长度限制在一次路由发布最多能容纳的别名数：比所有可能存在
// 的别名还长的列表，多出来的只能是不存在的模型。
const MaxModels = modelroute.MaxRoutes

// MaxModelBytes bounds one alias. Model IDs in the wild are well under it; the
// bound exists so a list cannot be used to park arbitrary text in the database
// and in every cached key verification.
//
// MaxModelBytes 限制单个别名的长度。现实中的模型 ID 远短于它；设置这个上限，是为了
// 不让一份列表被用来往数据库和每一条缓存的 Key 校验结果里塞任意文本。
const MaxModelBytes = 256

// MaxDocumentBytes bounds a request body carrying one list. It is the routing
// document's bound: a list names at most every alias a publication holds, so
// it never needs more room than that publication.
//
// MaxDocumentBytes 限制携带一份列表的请求体大小。它沿用路由文档的上限：一份列表
// 最多点名一次发布中的全部别名，因此用不着比那次发布更大的空间。
const MaxDocumentBytes = modelroute.MaxDocumentBytes

// Policy is the access that applies to one tenant: the platform list and that
// tenant's own list. It travels whole, rather than as one pre-intersected
// list, because an intersection cannot tell "nothing in common" from "no
// restriction" once both are written as an empty list.
//
// Policy 是作用于某个租户的授权：平台列表和该租户自己的列表。它整体传递，而不是
// 预先求好交集的一份列表，因为一旦两者都写成空列表，交集就区分不了「没有共同项」
// 与「不限制」。
type Policy struct {
	Platform []string `json:"platform,omitempty"`
	Tenant   []string `json:"tenant,omitempty"`
}

// Unrestricted reports whether neither level restricts anything, which lets a
// caller skip filtering entirely.
//
// Unrestricted 报告两级是否都不做任何限制，调用方据此可以整个跳过过滤。
func (p Policy) Unrestricted() bool {
	return len(p.Platform) == 0 && len(p.Tenant) == 0
}

// Allows reports whether model may be seen and called under p.
//
// Allows 报告在 p 之下 model 是否可以被看到和调用。
func (p Policy) Allows(model string) bool {
	return listAllows(p.Platform, model) && listAllows(p.Tenant, model)
}

func listAllows(list []string, model string) bool {
	return len(list) == 0 || slices.Contains(list, model)
}

// Normalize validates a list as an operator submitted it and returns its
// canonical form: trimmed, deduplicated and sorted, so two submissions naming
// the same set store and audit identically. An empty or nil input returns an
// empty, non-nil list, which means "no restriction".
//
// Normalize 校验运维提交的一份列表并返回其规范形式：去除首尾空白、去重并排序，
// 使两次指定同一集合的提交在存储与审计中完全一致。空输入或 nil 输入返回一个空的
// 非 nil 列表，表示「不限制」。
func Normalize(models []string) ([]string, error) {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" {
			return nil, errors.New("modelaccess: empty model name")
		}
		if len(model) > MaxModelBytes || !utf8.ValidString(model) {
			return nil, errors.New("modelaccess: invalid model name")
		}
		if seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	if len(out) > MaxModels {
		return nil, errors.New("modelaccess: too many models")
	}
	sort.Strings(out)
	return out, nil
}
