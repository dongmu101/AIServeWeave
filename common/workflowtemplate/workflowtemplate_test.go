package workflowtemplate

import (
	"encoding/json"
	"testing"
)

func graphJSON(t *testing.T) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"6": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "a cat"}},
		"9": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"images": []any{"8", 0}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestValidate(t *testing.T) {
	g := graphJSON(t)
	for _, tt := range []struct {
		name    string
		id      string
		content Content
		valid   bool
	}{
		{"minimal valid", "tpl", Content{Graph: g}, true},
		{"empty id", "", Content{Graph: g}, false},
		{"not a graph", "tpl", Content{Graph: json.RawMessage(`"nope"`)}, false},
		{"node missing inputs", "tpl", Content{Graph: json.RawMessage(`{"1":{"class_type":"X"}}`)}, false},
		{"valid input", "tpl", Content{Graph: g, Inputs: []Input{{Name: "prompt", Node: "6", Field: "text", Type: InputString}}}, true},
		{"input unknown node", "tpl", Content{Graph: g, Inputs: []Input{{Name: "prompt", Node: "99", Field: "text", Type: InputString}}}, false},
		{"input unknown field", "tpl", Content{Graph: g, Inputs: []Input{{Name: "prompt", Node: "6", Field: "nope", Type: InputString}}}, false},
		{"input unknown type", "tpl", Content{Graph: g, Inputs: []Input{{Name: "prompt", Node: "6", Field: "text", Type: "weird"}}}, false},
		{"input empty name", "tpl", Content{Graph: g, Inputs: []Input{{Node: "6", Field: "text", Type: InputString}}}, false},
		{"duplicate input name", "tpl", Content{Graph: g, Inputs: []Input{
			{Name: "p", Node: "6", Field: "text", Type: InputString},
			{Name: "p", Node: "6", Field: "text", Type: InputString},
		}}, false},
		{"input bad default", "tpl", Content{Graph: g, Inputs: []Input{{Name: "p", Node: "6", Field: "text", Type: InputInteger, Default: json.RawMessage(`"nope"`)}}}, false},
		{"valid file input", "tpl", Content{Graph: g, Inputs: []Input{{Name: "image", Node: "6", Field: "text", Type: InputFile}}}, true},
		{"file input with a default is rejected", "tpl", Content{Graph: g, Inputs: []Input{{Name: "image", Node: "6", Field: "text", Type: InputFile, Default: json.RawMessage(`"placeholder.png"`)}}}, false},
		{"valid output", "tpl", Content{Graph: g, Outputs: []Output{{Name: "image", Node: "9", Type: "image"}}}, true},
		{"output unknown node", "tpl", Content{Graph: g, Outputs: []Output{{Name: "image", Node: "99", Type: "image"}}}, false},
		{"output empty type", "tpl", Content{Graph: g, Outputs: []Output{{Name: "image", Node: "9"}}}, false},
		{"duplicate output name", "tpl", Content{Graph: g, Outputs: []Output{
			{Name: "o", Node: "9", Type: "image"},
			{Name: "o", Node: "9", Type: "image"},
		}}, false},
		{"valid dependencies", "tpl", Content{Graph: g, Dependencies: Dependencies{
			CustomNodes: []NodeDependency{{Name: "ComfyUI-Impact-Pack", Version: "1.0"}},
			Models:      []ModelDependency{{Name: "sdxl-base", Version: "1.0"}},
		}}, true},
		{"empty custom node name", "tpl", Content{Graph: g, Dependencies: Dependencies{CustomNodes: []NodeDependency{{Version: "1.0"}}}}, false},
		{"duplicate custom node", "tpl", Content{Graph: g, Dependencies: Dependencies{CustomNodes: []NodeDependency{{Name: "a"}, {Name: "a"}}}}, false},
		{"duplicate model dep", "tpl", Content{Graph: g, Dependencies: Dependencies{Models: []ModelDependency{{Name: "a"}, {Name: "a"}}}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.id, tt.content); (err == nil) != tt.valid {
				t.Fatalf("Validate() error = %v, want valid=%v", err, tt.valid)
			}
		})
	}
}

func TestDigestIsOrderIndependent(t *testing.T) {
	g := graphJSON(t)
	content := Content{Graph: g, Inputs: []Input{{Name: "p", Node: "6", Field: "text", Type: InputString}}}
	da, err := Digest("tpl", content, []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest("tpl", content, []string{"a", "b"})
	if err != nil || da != db {
		t.Fatalf("Digest with reordered tenants = %s, %v, want %s", db, err, da)
	}
	dc, err := Digest("tpl", content, []string{"a"})
	if err != nil || dc == da {
		t.Fatalf("Digest with different tenant list should differ from %s, got %s", da, dc)
	}
}

func TestBundleDigestIsOrderIndependent(t *testing.T) {
	a := []RevisionInfo{{TemplateID: "b", Revision: 1, Digest: "d2"}, {TemplateID: "a", Revision: 1, Digest: "d1"}}
	b := []RevisionInfo{a[1], a[0]}
	da, err := BundleDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := BundleDigest(b)
	if err != nil || da != db {
		t.Fatalf("BundleDigest reordered = %s, %v, want %s", db, err, da)
	}
	b[0].Digest = "changed"
	dc, _ := BundleDigest(b)
	if dc == da {
		t.Fatal("changed revision retained bundle digest")
	}
}

func TestValidateBoundsCounts(t *testing.T) {
	g := graphJSON(t)
	inputs := make([]Input, MaxInputs+1)
	for i := range inputs {
		inputs[i] = Input{Name: string(rune('a'+i%26)) + string(rune(i)), Node: "6", Field: "text", Type: InputString}
	}
	if err := Validate("tpl", Content{Graph: g, Inputs: inputs}); err == nil {
		t.Fatal("expected error for too many inputs")
	}
}
