package routing_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/routing"
)

func writeRoutes(t *testing.T, dir, name string, routes ...routing.Route) string {
	t.Helper()
	body, err := json.Marshal(routes)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestResolveReturnsTargetsInPriorityOrder(t *testing.T) {
	dir := t.TempDir()
	writeRoutes(t, dir, "r.json", routing.Route{
		Model: "qwen-coder",
		Targets: []routing.Target{
			{RuntimeModel: "qwen3-coder:30b", Priority: 10, NodeSelector: map[string]string{"region": "local"}},
			{RuntimeModel: "Qwen/Qwen3-Coder", Priority: 1},
			{RuntimeModel: "Qwen/Qwen3-Coder-FP8", Priority: 5},
		},
	})
	table, err := routing.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	targets, ok := table.Resolve("qwen-coder")
	if !ok {
		t.Fatal("Resolve(\"qwen-coder\") = _, false, want the route to be found")
	}
	// Lower Priority is tried first, matching every other "priority" in this
	// repository and in Kubernetes: 1 is the first choice, not the last.
	//
	// Priority 越小越先尝试，与本仓库以及 Kubernetes 中其他每一处「优先级」一致：
	// 1 是首选而不是末选。
	want := []string{"Qwen/Qwen3-Coder", "Qwen/Qwen3-Coder-FP8", "qwen3-coder:30b"}
	if len(targets) != len(want) {
		t.Fatalf("targets = %d, want %d", len(targets), len(want))
	}
	for i, w := range want {
		if targets[i].RuntimeModel != w {
			t.Errorf("target %d = %q, want %q", i, targets[i].RuntimeModel, w)
		}
	}
}

func TestResolveUnknownModel(t *testing.T) {
	table, err := routing.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := table.Resolve("nothing"); ok {
		t.Error("Resolve on an empty table found something")
	}
}

// TestMatchesNode is the node-selector half. A selector is a conjunction: every
// declared label must match, because a rule that meant "any of these" could not
// express "the local 4090" at all.
//
// TestMatchesNode 是节点选择器那一半。选择器是「与」：每个声明的标签都必须匹配，因为
// 一条意为「其中任意一个」的规则根本无法表达「本地那台 4090」。
func TestMatchesNode(t *testing.T) {
	tests := []struct {
		name     string
		selector map[string]string
		labels   map[string]string
		want     bool
	}{
		{
			name:     "an empty selector matches every node",
			selector: nil,
			labels:   map[string]string{"region": "local"},
			want:     true,
		},
		{
			name:     "an empty selector matches an unlabelled node",
			selector: nil,
			labels:   nil,
			want:     true,
		},
		{
			name:     "one label matches",
			selector: map[string]string{"region": "local"},
			labels:   map[string]string{"region": "local", "gpu": "4090"},
			want:     true,
		},
		{
			name:     "every label must match",
			selector: map[string]string{"region": "local", "gpu": "4090"},
			labels:   map[string]string{"region": "local", "gpu": "3090"},
			want:     false,
		},
		{
			name:     "a missing label does not match",
			selector: map[string]string{"region": "local"},
			labels:   map[string]string{"gpu": "4090"},
			want:     false,
		},
		{
			name:     "an unlabelled node does not match a selector",
			selector: map[string]string{"region": "local"},
			labels:   nil,
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := routing.Target{NodeSelector: tt.selector}
			if got := target.MatchesNode(tt.labels); got != tt.want {
				t.Errorf("MatchesNode(%v) with selector %v = %v, want %v", tt.labels, tt.selector, got, tt.want)
			}
		})
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name    string
		route   routing.Route
		wantErr string
	}{
		{
			name:    "empty model",
			route:   routing.Route{Targets: []routing.Target{{RuntimeModel: "m"}}},
			wantErr: "model",
		},
		{
			name:    "no targets",
			route:   routing.Route{Model: "alias"},
			wantErr: "target",
		},
		{
			name:    "a target with no runtime model",
			route:   routing.Route{Model: "alias", Targets: []routing.Target{{Priority: 1}}},
			wantErr: "runtime_model",
		},
		{
			name:    "a negative weight",
			route:   routing.Route{Model: "alias", Targets: []routing.Target{{RuntimeModel: "m", Weight: -1}}},
			wantErr: "weight",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRoutes(t, dir, "r.json", tt.route)
			_, err := routing.Load(dir)
			if err == nil {
				t.Fatalf("Load() error = nil, want one mentioning %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestLoadRejectsADuplicateAlias stops two files from each claiming one alias,
// where which one wins would depend on directory order.
//
// TestLoadRejectsADuplicateAlias 阻止两个文件各自声称同一个别名——那样谁生效将取决于
// 目录顺序。
func TestLoadRejectsADuplicateAlias(t *testing.T) {
	dir := t.TempDir()
	writeRoutes(t, dir, "a.json", routing.Route{Model: "alias", Targets: []routing.Target{{RuntimeModel: "m1"}}})
	writeRoutes(t, dir, "b.json", routing.Route{Model: "alias", Targets: []routing.Target{{RuntimeModel: "m2"}}})

	if _, err := routing.Load(dir); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Errorf("Load() error = %v, want it to name the duplicated alias", err)
	}
}

func TestModelsListsAliases(t *testing.T) {
	dir := t.TempDir()
	writeRoutes(t, dir, "r.json",
		routing.Route{Model: "zeta", Targets: []routing.Target{{RuntimeModel: "m"}}},
		routing.Route{Model: "alpha", Targets: []routing.Target{{RuntimeModel: "m"}}},
	)
	table, err := routing.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := table.Models()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("Models() = %v, want [alpha zeta] sorted", got)
	}
}

// TestNilTableIsUsable keeps a deployment with no routing file from needing a
// nil check at every call site.
//
// TestNilTableIsUsable 让没有路由文件的部署无需在每个调用点做空值判断。
func TestNilTableIsUsable(t *testing.T) {
	var table *routing.Table
	if _, ok := table.Resolve("anything"); ok {
		t.Error("Resolve on a nil table found something")
	}
	if got := table.Models(); len(got) != 0 {
		t.Errorf("Models() = %v, want empty", got)
	}
}
