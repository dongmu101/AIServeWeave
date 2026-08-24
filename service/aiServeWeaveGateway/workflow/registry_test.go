package workflow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// writeTemplate writes one template manifest into dir and returns its path.
//
// writeTemplate 往 dir 里写一份模板清单并返回其路径。
func writeTemplate(t *testing.T, dir, name, id string) string {
	t.Helper()
	body, err := json.Marshal(workflow.Template{
		ID:     id,
		Inputs: []workflow.Input{{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true}},
		Graph:  json.RawMessage(testGraph),
	})
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return path
}

func TestLoadReadsADirectoryOfTemplates(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "a.json", "alpha")
	writeTemplate(t, dir, "b.json", "beta")
	// A non-JSON file in the same directory is ignored rather than fatal: an
	// operator's notes next to their templates are not a config error.
	//
	// 同一目录下的非 JSON 文件被忽略而不是致命错误：运维在模板旁边放一份笔记不算
	// 配置错误。
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}

	reg, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if got, want := reg.IDs(), []string{"alpha", "beta"}; !equalStrings(got, want) {
		t.Errorf("IDs() = %v, want %v", got, want)
	}
	if _, ok := reg.Lookup("alpha"); !ok {
		t.Error(`Lookup("alpha") = _, false, want it to be found`)
	}
	if _, ok := reg.Lookup("missing"); ok {
		t.Error(`Lookup("missing") = _, true, want false`)
	}
}

func TestLoadReadsSingleFiles(t *testing.T) {
	dir := t.TempDir()
	path := writeTemplate(t, dir, "a.json", "alpha")

	reg, err := workflow.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if reg.Len() != 1 {
		t.Errorf("Len() = %d, want 1", reg.Len())
	}
}

func TestLoadRejectsDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "a.json", "alpha")
	writeTemplate(t, dir, "b.json", "alpha")

	_, err := workflow.Load(dir)
	if err == nil {
		t.Fatal("Load() error = nil, want a duplicate-id error")
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("Load() error = %q, want it to name the duplicated id", err)
	}
}

func TestLoadRejectsAnInvalidTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"),
		[]byte(`{"id": "bad", "graph": {}, "inputs": [{"name":"p","node":"6","field":"text","type":"string"}]}`), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if _, err := workflow.Load(dir); err == nil {
		t.Fatal("Load() error = nil, want the template's own validation error")
	}
}

func TestLoadWithNoPathsReturnsAnEmptyRegistry(t *testing.T) {
	reg, err := workflow.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if reg.Len() != 0 {
		t.Errorf("Len() = %d, want 0", reg.Len())
	}
	if _, ok := reg.Lookup("anything"); ok {
		t.Error("Lookup on an empty registry found something")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
