// Package routing maps the logical model a client asks for onto the real
// deployments that can serve it.
//
// It is README's 模型与部署抽象 made executable: a client says "qwen-coder" and
// this package answers with an ordered list of (real model name, node
// selector) pairs. That indirection is what lets an operator swap a
// quantization, move a model to a different node, or take a GPU server down,
// without any client changing what it asks for.
//
// routing 包把客户端所请求的逻辑模型，映射到真正能服务它的那些部署上。
//
// 它是 README「模型与部署抽象」的可执行版本：客户端说 "qwen-coder"，本包答以一列有序
// 的（真实模型名、节点选择器）对。正是这层间接，让运维可以更换量化版本、把模型挪到
// 另一个节点、或者关掉一台 GPU 服务器，而无需任何客户端改变它所请求的东西。
package routing

import (
	"AIServeWeave/common/modelroute"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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

// Target is a shared deployment selector. / Target 是共享的部署选择器。
type Target = modelroute.Target

// Route is a shared logical model route. / Route 是共享的逻辑模型路由。
type Route = modelroute.Route

// Table is immutable after construction and safe for concurrent readers.
// Table 在构造后不可变，可以安全地并发读取。
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
	if err := modelroute.Validate(table.Routes()); err != nil {
		return nil, err
	}
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
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, MaxRouteFileBytes+1))
	if len(body) > MaxRouteFileBytes {
		return fmt.Errorf("routing: file exceeds limit")
	}
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
	return modelroute.Validate(t.Routes())
}

// Resolve returns the targets for a logical model, best first.
//
// Resolve 返回某个逻辑模型的 target，最优的在前。
func (t *Table) Resolve(model string) ([]Target, bool) {
	if t == nil {
		return nil, false
	}
	targets, ok := t.byModel[model]
	return cloneTargets(targets), ok
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

// New validates and owns a deep copy of routes. / New 校验并持有路由的深拷贝。
func New(routes []Route) (*Table, error) {
	if err := modelroute.Validate(routes); err != nil {
		return nil, err
	}
	t := &Table{byModel: make(map[string][]Target)}
	for _, r := range routes {
		targets := cloneTargets(r.Targets)
		sort.SliceStable(targets, func(i, j int) bool { return targets[i].Priority < targets[j].Priority })
		t.byModel[r.Model] = targets
		t.models = append(t.models, r.Model)
	}
	sort.Strings(t.models)
	return t, nil
}

// Routes returns a detached copy of the effective table. / Routes 返回生效路由表的独立拷贝。
func (t *Table) Routes() []Route {
	out := []Route{}
	if t != nil {
		for _, m := range t.models {
			out = append(out, Route{Model: m, Targets: cloneTargets(t.byModel[m])})
		}
	}
	return out
}

func cloneTargets(in []Target) []Target {
	out := append([]Target(nil), in...)
	for i := range out {
		out[i].NodeSelector = maps.Clone(out[i].NodeSelector)
	}
	return out
}
