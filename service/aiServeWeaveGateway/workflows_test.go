package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

const testImagesGraph = `{
  "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512}},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": ""}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0]}}
}`

// loadTemplates writes a one-template catalogue and loads it, the same
// on-disk shape -workflow-templates uses at startup.
func loadTemplates(t *testing.T, tpl workflow.Template) *workflow.Handle {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(tpl)
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t.json"), body, 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	reg, err := workflow.Load(dir)
	if err != nil {
		t.Fatalf("workflow.Load: %v", err)
	}
	return workflow.NewHandle(reg)
}

func TestValidateImagesWorkflow(t *testing.T) {
	wellFormed := workflow.Template{
		ID: "text-to-image",
		Inputs: []workflow.Input{
			{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true},
		},
		Outputs: []workflow.Output{{Name: "image", Node: "9", Type: "image"}},
		Graph:   json.RawMessage(testImagesGraph),
	}

	tests := []struct {
		name             string
		imagesWorkflowID string
		tpl              workflow.Template
		wantErrContains  string
	}{
		{name: "empty id is not checked", imagesWorkflowID: "", tpl: wellFormed},
		{name: "a well-formed template passes", imagesWorkflowID: "text-to-image", tpl: wellFormed},
		{
			name: "missing prompt input fails", imagesWorkflowID: "text-to-image",
			tpl: workflow.Template{
				ID:      "text-to-image",
				Outputs: []workflow.Output{{Name: "image", Node: "9", Type: "image"}},
				Graph:   json.RawMessage(testImagesGraph),
			},
			wantErrContains: "prompt",
		},
		{
			name: "an optional (not required) prompt fails", imagesWorkflowID: "text-to-image",
			tpl: workflow.Template{
				ID: "text-to-image",
				Inputs: []workflow.Input{
					{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: false},
				},
				Outputs: []workflow.Output{{Name: "image", Node: "9", Type: "image"}},
				Graph:   json.RawMessage(testImagesGraph),
			},
			wantErrContains: "prompt",
		},
		{
			name: "missing an image-typed output fails", imagesWorkflowID: "text-to-image",
			tpl: workflow.Template{
				ID: "text-to-image",
				Inputs: []workflow.Input{
					{Name: "prompt", Node: "6", Field: "text", Type: workflow.InputString, Required: true},
				},
				Graph: json.RawMessage(testImagesGraph),
			},
			wantErrContains: "image",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handle := loadTemplates(t, tt.tpl)
			err := validateImagesWorkflow(handle, tt.imagesWorkflowID)
			if tt.wantErrContains == "" {
				if err != nil {
					t.Fatalf("validateImagesWorkflow() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("validateImagesWorkflow() error = %v, want it to mention %q", err, tt.wantErrContains)
			}
		})
	}
}

func TestValidateImagesWorkflowUnregisteredID(t *testing.T) {
	handle := loadTemplates(t, workflow.Template{
		ID:      "some-other-template",
		Outputs: []workflow.Output{{Name: "image", Node: "9", Type: "image"}},
		Graph:   json.RawMessage(testImagesGraph),
	})
	err := validateImagesWorkflow(handle, "text-to-image")
	if err == nil || !strings.Contains(err.Error(), "not a registered workflow template") {
		t.Fatalf("validateImagesWorkflow() error = %v, want it to say the id is not registered", err)
	}
}
