package workflow_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// testGraph is a trimmed API-format ComfyUI workflow: a sampler, a latent
// image, a positive prompt and a save node.
//
// testGraph 是一份精简的 API Format ComfyUI 工作流：采样器、空 latent、正向提示词
// 与保存节点各一个。
const testGraph = `{
  "3": {"class_type": "KSampler", "inputs": {"seed": 0, "steps": 20, "cfg": 8.0, "model": ["4", 0]}},
  "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512, "batch_size": 1}},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["4", 1]}, "_meta": {"title": "Positive"}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0]}}
}`

func testTemplate(t *testing.T) *workflow.Template {
	t.Helper()
	tpl := &workflow.Template{
		ID: "flux-text-to-image",
		Inputs: []workflow.Input{
			{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true, MaxLength: 16},
			{Name: "width", Node: "5", Field: "width", Type: workflow.InputInteger, Default: json.RawMessage(`768`), Min: f(64), Max: f(2048)},
			{Name: "cfg", Node: "3", Field: "cfg", Type: workflow.InputNumber, Min: f(0), Max: f(20)},
		},
		Graph: json.RawMessage(testGraph),
	}
	if err := tpl.Validate(); err != nil {
		t.Fatalf("Validate() on the fixture template: %v", err)
	}
	return tpl
}

func f(v float64) *float64 { return &v }

// nodeField reads one field out of a bound graph.
//
// nodeField 从一份已绑定的图里读出某个字段。
func nodeField(t *testing.T, bound json.RawMessage, node, field string) any {
	t.Helper()
	var g map[string]struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(bound, &g); err != nil {
		t.Fatalf("unmarshal bound graph: %v", err)
	}
	n, ok := g[node]
	if !ok {
		t.Fatalf("node %q missing from bound graph", node)
	}
	return n.Inputs[field]
}

func TestBindAppliesDeclaredInputs(t *testing.T) {
	tpl := testTemplate(t)
	bound, err := tpl.Bind(map[string]json.RawMessage{
		"prompt": json.RawMessage(`"a cat"`),
		"width":  json.RawMessage(`1024`),
	})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}

	if got, want := nodeField(t, bound, "6", "text"), "a cat"; got != want {
		t.Errorf("node 6 text = %v, want %v", got, want)
	}
	if got, want := nodeField(t, bound, "5", "width"), float64(1024); got != want {
		t.Errorf("node 5 width = %v, want %v", got, want)
	}
	// An input the caller omitted and that has no default keeps the graph's
	// own value.
	//
	// 调用方未给、也没有默认值的输入，保持图里原本的取值。
	if got, want := nodeField(t, bound, "3", "cfg"), float64(8); got != want {
		t.Errorf("node 3 cfg = %v, want the template's own %v", got, want)
	}
	// Fields the template never declared are untouched.
	//
	// 模板未声明的字段原样不动。
	if got, want := nodeField(t, bound, "3", "steps"), float64(20); got != want {
		t.Errorf("node 3 steps = %v, want the template's own %v", got, want)
	}
}

func TestBindAppliesDefaultForOmittedInput(t *testing.T) {
	tpl := testTemplate(t)
	bound, err := tpl.Bind(map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`)})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	if got, want := nodeField(t, bound, "5", "width"), float64(768); got != want {
		t.Errorf("node 5 width = %v, want the declared default %v", got, want)
	}
}

func TestBindLeavesTemplateGraphUnchanged(t *testing.T) {
	tpl := testTemplate(t)
	before := string(tpl.Graph)
	if _, err := tpl.Bind(map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`)}); err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	if got := string(tpl.Graph); got != before {
		t.Errorf("Bind() mutated the stored graph:\n got: %s\nwant: %s", got, before)
	}
}

func TestBindRejects(t *testing.T) {
	tests := []struct {
		name       string
		values     map[string]json.RawMessage
		wantInput  string
		wantReason string
	}{
		{
			name:       "unknown input",
			values:     map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`), "checkpoint": json.RawMessage(`"evil.safetensors"`)},
			wantInput:  "checkpoint",
			wantReason: "not declared",
		},
		{
			name:       "required input missing",
			values:     map[string]json.RawMessage{"width": json.RawMessage(`1024`)},
			wantInput:  "prompt",
			wantReason: "required",
		},
		{
			name:       "string given a number",
			values:     map[string]json.RawMessage{"prompt": json.RawMessage(`7`)},
			wantInput:  "prompt",
			wantReason: "string",
		},
		{
			name:       "string over max length",
			values:     map[string]json.RawMessage{"prompt": json.RawMessage(`"aaaaaaaaaaaaaaaaaaaaaaaa"`)},
			wantInput:  "prompt",
			wantReason: "at most 16",
		},
		{
			name:       "integer given a fraction",
			values:     map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`), "width": json.RawMessage(`1024.5`)},
			wantInput:  "width",
			wantReason: "integer",
		},
		{
			name:       "integer below min",
			values:     map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`), "width": json.RawMessage(`8`)},
			wantInput:  "width",
			wantReason: "at least 64",
		},
		{
			name:       "integer above max",
			values:     map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`), "width": json.RawMessage(`4096`)},
			wantInput:  "width",
			wantReason: "at most 2048",
		},
		{
			name:       "number given an object",
			values:     map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`), "cfg": json.RawMessage(`{"a":1}`)},
			wantInput:  "cfg",
			wantReason: "number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := testTemplate(t)
			bound, err := tpl.Bind(tt.values)
			if err == nil {
				t.Fatalf("Bind() error = nil, want an error mentioning %q; bound = %s", tt.wantInput, bound)
			}
			var inputErr *workflow.InputError
			if !errors.As(err, &inputErr) {
				t.Fatalf("Bind() error = %v (%T), want *workflow.InputError", err, err)
			}
			if inputErr.Name != tt.wantInput {
				t.Errorf("InputError.Name = %q, want %q", inputErr.Name, tt.wantInput)
			}
			if !strings.Contains(inputErr.Reason, tt.wantReason) {
				t.Errorf("InputError.Reason = %q, want it to contain %q", inputErr.Reason, tt.wantReason)
			}
		})
	}
}

// TestBindTruncatesUnknownInputName keeps an oversized caller-supplied key
// from travelling back out in an error message at its original length.
//
// TestBindTruncatesUnknownInputName 确保调用方给出的超长键不会以原长度出现在错误
// 信息里回到对端。
func TestBindTruncatesUnknownInputName(t *testing.T) {
	tpl := testTemplate(t)
	long := strings.Repeat("x", 4096)
	_, err := tpl.Bind(map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`), long: json.RawMessage(`1`)})
	var inputErr *workflow.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("Bind() error = %v, want *workflow.InputError", err)
	}
	if len(inputErr.Name) > workflow.MaxInputNameInError {
		t.Errorf("InputError.Name is %d bytes, want at most %d", len(inputErr.Name), workflow.MaxInputNameInError)
	}
}
