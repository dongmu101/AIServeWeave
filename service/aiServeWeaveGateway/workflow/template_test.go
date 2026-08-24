package workflow_test

import (
	"encoding/json"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// TestValidateRejects covers what a template must not be allowed to declare.
// Every case here is caught at load time rather than at submit time: a
// template that binds into a node it does not have is an operator mistake,
// and the caller is not the one who should discover it.
//
// TestValidateRejects 覆盖模板不允许声明的东西。这里每一项都在加载时而不是提交时
// 被拦下：模板绑向一个并不存在的节点是运维的失误，而发现它的不该是调用方。
func TestValidateRejects(t *testing.T) {
	valid := func() *workflow.Template {
		return &workflow.Template{
			ID:     "t",
			Inputs: []workflow.Input{{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true}},
			Graph:  json.RawMessage(testGraph),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*workflow.Template)
		wantErr string
	}{
		{
			name:    "empty id",
			mutate:  func(tpl *workflow.Template) { tpl.ID = "" },
			wantErr: "id",
		},
		{
			name:    "graph is not an object",
			mutate:  func(tpl *workflow.Template) { tpl.Graph = json.RawMessage(`[1,2]`) },
			wantErr: "graph",
		},
		{
			name:    "node has no inputs object",
			mutate:  func(tpl *workflow.Template) { tpl.Graph = json.RawMessage(`{"6": {"class_type": "CLIPTextEncode"}}`) },
			wantErr: "inputs",
		},
		{
			name:    "input binds to a missing node",
			mutate:  func(tpl *workflow.Template) { tpl.Inputs[0].Node = "42" },
			wantErr: `node "42"`,
		},
		{
			name:    "input binds to a missing field",
			mutate:  func(tpl *workflow.Template) { tpl.Inputs[0].Field = "nope" },
			wantErr: "nope",
		},
		{
			name:    "unknown input type",
			mutate:  func(tpl *workflow.Template) { tpl.Inputs[0].Type = "blob" },
			wantErr: "blob",
		},
		{
			name: "duplicate input name",
			mutate: func(tpl *workflow.Template) {
				tpl.Inputs = append(tpl.Inputs, workflow.Input{Name: "prompt", Node: "5", Field: "width", Type: workflow.InputInteger})
			},
			wantErr: "prompt",
		},
		{
			name: "default does not match the declared type",
			mutate: func(tpl *workflow.Template) {
				tpl.Inputs[0].Default = json.RawMessage(`5`)
			},
			wantErr: "default",
		},
		{
			name:    "empty input name",
			mutate:  func(tpl *workflow.Template) { tpl.Inputs[0].Name = "" },
			wantErr: "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := valid()
			tt.mutate(tpl)
			err := tpl.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want an error mentioning %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateAcceptsAWellFormedTemplate(t *testing.T) {
	tpl := testTemplate(t)
	if err := tpl.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
