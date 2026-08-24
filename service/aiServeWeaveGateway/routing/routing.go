// Package routing maps the logical model a client asks for onto the real
// deployments that can serve it.
//
// It is README's 模型与部署抽象 made executable: a client says "qwen-coder" and
// this package answers with an ordered list of (real model name, node
// selector) pairs. That indirection is what lets an operator swap a
// quantization, move a model to a different node, or take a GPU server down,
// without any client changing what it asks for.
//
// The table is loaded from files rather than from the control plane. That is a
// deliberate first step, matching how -workflow-templates works: the feature
// becomes usable without first building three tables and a CRUD surface for
// them. When the Console needs to edit routes, this package keeps its shape
// and Load is replaced by a fetch — Table is already the only thing the
// scheduler depends on.
//
// routing 包把客户端所请求的逻辑模型，映射到真正能服务它的那些部署上。
//
// 它是 README「模型与部署抽象」的可执行版本：客户端说 "qwen-coder"，本包答以一列有序
// 的（真实模型名、节点选择器）对。正是这层间接，让运维可以更换量化版本、把模型挪到
// 另一个节点、或者关掉一台 GPU 服务器，而无需任何客户端改变它所请求的东西。
//
// 路由表从文件加载而不是从控制面。这是一个有意为之的第一步，与 -workflow-templates
// 的做法一致：功能可用，无需先建三张表并为它们配一套 CRUD。等 Console 需要编辑路由
// 时，本包保持形状，Load 换成一次拉取即可——调度器依赖的本来就只有 Table 一个东西。
package routing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxRouteFileBytes bounds one routing file. A route table is a handful of
// aliases; anything larger is a mistake worth failing on at startup.
//
// MaxRouteFileBytes 限制单个路由文件的大小。一张路由表不过是若干别名；更大的是错误，
// 值得在启动时失败。
const MaxRouteFileBytes = 1 << 20

// Target is one place a logical model can be served from.
//
// Target 是一个逻辑模型可以被服务的一处去向。
type Target struct {
	// RuntimeModel is the model id a node actually advertises. It is what the
	// request is rewritten to before it goes down the tunnel.
	//
	// RuntimeModel 是节点实际声明的模型 id。请求在下隧道之前会被改写成它。
	RuntimeModel string `json:"runtime_model"`
	// NodeSelector narrows this target to nodes carrying every label in it. An
	// empty selector matches every node.
	//
	// NodeSelector 把本 target 收窄到带有其中每一个标签的节点上。选择器为空则匹配
	// 所有节点。
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	// Priority orders targets, lowest first. It is the operator's stated
	// preference — "the local Mac before the rented GPU" — and it is applied
	// before any load-based ordering, so a healthy first choice is used until
	// it stops being available.
	//
	// Priority 为 target 排序，数值小的在前。它是运维声明的偏好——「先用本地那台 Mac，
	// 再用租来的 GPU」——并且先于任何基于负载的排序生效，因此健康的首选会一直被使用，
	// 直到它不再可用。
	Priority int `json:"priority,omitempty"`
	// Weight splits traffic between targets of equal priority. Zero means one
	// share, so a table that never mentions weight spreads evenly.
	//
	// Weight 在同优先级的 target 之间分配流量。为零表示一份，因此从不提及 weight 的
	// 表会均匀铺开。
	Weight int `json:"weight,omitempty"`
}

// MatchesNode reports whether a node carrying labels satisfies this target's
// selector. The selector is a conjunction: every declared label must match. A
// rule meaning "any of these" could not express "the local 4090" at all, and
// that is the rule operators actually write.
//
// MatchesNode 报告带有 labels 的节点是否满足本 target 的选择器。选择器是「与」：每个
// 声明的标签都必须匹配。一条意为「其中任意一个」的规则根本无法表达「本地那台 4090」，
// 而那正是运维实际会写的规则。
func (t Target) MatchesNode(labels map[string]string) bool {
	for key, want := range t.NodeSelector {
		if labels[key] != want {
			return false
		}
	}
	return true
}

// Route is one logical model and everywhere it can be served from.
//
// Route 是一个逻辑模型，以及它可以被服务的全部去向。
type Route struct {
	Model   string   `json:"model"`
	Targets []Target `json:"targets"`
}

// Validate rejects a route that cannot be acted on.
//
// Validate 拒绝一条无法执行的路由。
func (r Route) Validate() error {
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("routing: a route has an empty model")
	}
	if len(r.Targets) == 0 {
		return fmt.Errorf("routing: route %q has no target", r.Model)
	}
	for i, t := range r.Targets {
		if strings.TrimSpace(t.RuntimeModel) == "" {
			return fmt.Errorf("routing: route %q target %d has an empty runtime_model", r.Model, i)
		}
		if t.Weight < 0 {
			return fmt.Errorf("routing: route %q target %d has a negative weight", r.Model, i)
		}
	}
	return nil
}

// Table is the loaded routing table. It is built once at startup and read-only
// afterwards, so it needs no lock.
//
// Table 是已加载的路由表。它在启动时构建一次，此后只读，因此无需加锁。
type Table struct {
	byModel map[string][]Target
	models  []string
}

// Load reads every routing file named by paths. A path may be a file or a
// directory, in which case its *.json entries are read and anything else is
// ignored. Each file holds an array of routes.
//
// Load 读取 paths 指定的所有路由文件。path 可以是文件或目录——目录下的 *.json 会被
// 读取，其余一律忽略。每个文件存放一个路由数组。
func Load(paths ...string) (*Table, error) {
	table := &Table{byModel: make(map[string][]Target)}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("routing: reading %s: %w", path, err)
		}
		if !info.IsDir() {
			if err := table.loadFile(path); err != nil {
				return nil, err
			}
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("routing: reading %s: %w", path, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				continue
			}
			if err := table.loadFile(filepath.Join(path, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(table.models)
	return table, nil
}

func (t *Table) loadFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("routing: reading %s: %w", path, err)
	}
	if info.Size() > MaxRouteFileBytes {
		return fmt.Errorf("routing: %s is %d bytes, over the %d limit", path, info.Size(), MaxRouteFileBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("routing: reading %s: %w", path, err)
	}
	var routes []Route
	if err := json.Unmarshal(body, &routes); err != nil {
		return fmt.Errorf("routing: %s is not a valid route array: %w", path, err)
	}
	for _, route := range routes {
		if err := route.Validate(); err != nil {
			return fmt.Errorf("routing: %s: %w", path, err)
		}
		if _, dup := t.byModel[route.Model]; dup {
			return fmt.Errorf("routing: %s declares alias %q, which another route already claimed", path, route.Model)
		}
		targets := append([]Target(nil), route.Targets...)
		// Sorted once at load so Resolve does no work per request. Stable, so
		// two targets of equal priority keep the order the operator wrote —
		// which is the only order they could have meant.
		//
		// 在加载时排一次序，好让 Resolve 在每个请求上不做任何工作。稳定排序，因此同
		// 优先级的两个 target 保持运维书写的顺序——那是他们唯一可能意指的顺序。
		sort.SliceStable(targets, func(i, j int) bool { return targets[i].Priority < targets[j].Priority })
		t.byModel[route.Model] = targets
		t.models = append(t.models, route.Model)
	}
	return nil
}

// Resolve returns the targets for a logical model, best first.
//
// Resolve 返回某个逻辑模型的 target，最优的在前。
func (t *Table) Resolve(model string) ([]Target, bool) {
	if t == nil {
		return nil, false
	}
	targets, ok := t.byModel[model]
	return targets, ok
}

// Models returns every alias this table defines, sorted.
//
// Models 返回本表定义的所有别名，已排序。
func (t *Table) Models() []string {
	if t == nil {
		return nil
	}
	out := make([]string, len(t.models))
	copy(out, t.models)
	return out
}

// Len is how many aliases are defined.
//
// Len 是已定义的别名数量。
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.byModel)
}
