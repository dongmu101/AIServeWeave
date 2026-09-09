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
  "7": {"class_type": "LoadImage", "inputs": {"image": "example.png"}},
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
			{Name: "image", Node: "7", Field: "image", Type: workflow.InputFile},
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
	bound, _, err := tpl.Bind(map[string]json.RawMessage{
		"prompt": json.RawMessage(`"a cat"`),
		"width":  json.RawMessage(`1024`),
	}, nil)
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
	bound, _, err := tpl.Bind(map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`)}, nil)
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
	if _, _, err := tpl.Bind(map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`)}, nil); err != nil {
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
			bound, _, err := tpl.Bind(tt.values, nil)
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
	_, _, err := tpl.Bind(map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`), long: json.RawMessage(`1`)}, nil)
	var inputErr *workflow.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("Bind() error = %v, want *workflow.InputError", err)
	}
	if len(inputErr.Name) > workflow.MaxInputNameInError {
		t.Errorf("InputError.Name is %d bytes, want at most %d", len(inputErr.Name), workflow.MaxInputNameInError)
	}
}

func requiredValues() map[string]json.RawMessage {
	return map[string]json.RawMessage{"prompt": json.RawMessage(`"a cat"`)}
}

// TestBindReturnsAPendingFileForASuppliedFileInput covers the split Bind
// makes for a file-typed input: the graph field it targets is left
// untouched (there is no value to substitute yet), and the input is
// reported back as a PendingFile carrying everything a later
// scheduler.UploadInput + workflow.SetGraphField step needs.
//
// TestBindReturnsAPendingFileForASuppliedFileInput 覆盖 Bind 对一个文件类型
// 输入所做的拆分：它所指向的图字段保持不动（此刻还没有可替换的取值），
// 而该输入会作为一个 PendingFile 被回报，携带之后一步
// scheduler.UploadInput + workflow.SetGraphField 所需的一切。
func TestBindReturnsAPendingFileForASuppliedFileInput(t *testing.T) {
	tpl := testTemplate(t)
	bound, pending, err := tpl.Bind(requiredValues(), map[string]workflow.FileHeader{
		"image": {Filename: "cat.png", Size: 1234},
	})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	p := pending[0]
	if p.Name != "image" || p.Node != "7" || p.Field != "image" || p.Filename != "cat.png" || p.Size != 1234 {
		t.Errorf("pending[0] = %+v, want the declared input's Node/Field plus the given filename and size", p)
	}
	// The graph field is untouched: it still holds whatever the template
	// author's own graph had there, not the caller's filename.
	//
	// 图字段保持原样：那里仍是模板作者自己的图本来就有的东西，而不是调用方
	// 给出的文件名。
	if got, want := nodeField(t, bound, "7", "image"), "example.png"; got != want {
		t.Errorf("node 7 image = %v, want the template's own untouched %q", got, want)
	}
}

func TestBindSkipsAnOptionalFileInputThatWasNotSupplied(t *testing.T) {
	tpl := testTemplate(t)
	_, pending, err := tpl.Bind(requiredValues(), nil)
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %+v, want none: the file input is optional and was not supplied", pending)
	}
}

func TestBindRejectsAScalarValueForAFileInput(t *testing.T) {
	tpl := testTemplate(t)
	values := requiredValues()
	values["image"] = json.RawMessage(`"cat.png"`)
	_, _, err := tpl.Bind(values, nil)
	var inputErr *workflow.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("Bind() error = %v, want *workflow.InputError", err)
	}
	if inputErr.Name != "image" || !strings.Contains(inputErr.Reason, "file") {
		t.Errorf("InputError = %+v, want it to name image and explain it must be a file", inputErr)
	}
}

func TestBindRejectsAFilePartForANonFileInput(t *testing.T) {
	tpl := testTemplate(t)
	_, _, err := tpl.Bind(requiredValues(), map[string]workflow.FileHeader{"prompt": {Filename: "x"}})
	var inputErr *workflow.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("Bind() error = %v, want *workflow.InputError", err)
	}
	if inputErr.Name != "prompt" || !strings.Contains(inputErr.Reason, "not a file") {
		t.Errorf("InputError = %+v, want it to name prompt and explain it is not a file input", inputErr)
	}
}

func TestBindRejectsAnUnknownFileName(t *testing.T) {
	tpl := testTemplate(t)
	_, _, err := tpl.Bind(requiredValues(), map[string]workflow.FileHeader{"checkpoint": {Filename: "evil.safetensors"}})
	var inputErr *workflow.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("Bind() error = %v, want *workflow.InputError", err)
	}
	if inputErr.Name != "checkpoint" || !strings.Contains(inputErr.Reason, "not declared") {
		t.Errorf("InputError = %+v, want it to name checkpoint and say it is not declared", inputErr)
	}
}

func TestBindRequiresARequiredFileInput(t *testing.T) {
	tpl := testTemplate(t)
	for i := range tpl.Inputs {
		if tpl.Inputs[i].Name == "image" {
			tpl.Inputs[i].Required = true
		}
	}
	_, _, err := tpl.Bind(requiredValues(), nil)
	var inputErr *workflow.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("Bind() error = %v, want *workflow.InputError", err)
	}
	if inputErr.Name != "image" || !strings.Contains(inputErr.Reason, "required") {
		t.Errorf("InputError = %+v, want it to name image and say it is required", inputErr)
	}
}

func TestSetGraphFieldWritesTheValueAndLeavesEverythingElseAlone(t *testing.T) {
	tpl := testTemplate(t)
	bound, pending, err := tpl.Bind(requiredValues(), map[string]workflow.FileHeader{"image": {Filename: "cat.png", Size: 3}})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	p := pending[0]

	patched, err := workflow.SetGraphField(bound, p.Node, p.Field, "cat (1).png")
	if err != nil {
		t.Fatalf("SetGraphField() error = %v, want nil", err)
	}
	if got, want := nodeField(t, patched, "7", "image"), "cat (1).png"; got != want {
		t.Errorf("node 7 image = %v, want the uploaded InputRef %q", got, want)
	}
	// Everything Bind already wrote survives the patch untouched.
	//
	// Bind 已经写下的一切在这次修补之后原样保留。
	if got, want := nodeField(t, patched, "6", "text"), "a cat"; got != want {
		t.Errorf("node 6 text = %v, want the value Bind already substituted, %q", got, want)
	}
}

func TestSetGraphFieldRejectsAnUnknownNode(t *testing.T) {
	tpl := testTemplate(t)
	if _, err := workflow.SetGraphField(tpl.Graph, "no-such-node", "image", "cat.png"); err == nil {
		t.Fatal("SetGraphField() on an unknown node: want an error, got nil")
	}
}
