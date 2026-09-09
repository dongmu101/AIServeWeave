package workflow_test

import (
	"encoding/json"
	"strings"
	"testing"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

func TestFromBundleBuildsARegistryFromControlPlaneSnapshots(t *testing.T) {
	snapshots := []workflowtemplate.Snapshot{
		{
			RevisionInfo: workflowtemplate.RevisionInfo{TemplateID: "alpha", Revision: 3},
			Content: workflowtemplate.Content{
				Inputs: []workflowtemplate.Input{{Name: "prompt", Node: "6", Field: "text", Type: workflowtemplate.InputString}},
				Graph:  json.RawMessage(testGraph),
			},
			VisibleTenantIDs: []string{"tenant-a"},
		},
	}
	reg, err := workflow.FromBundle(snapshots)
	if err != nil {
		t.Fatalf("FromBundle() error = %v, want nil", err)
	}
	tpl, ok := reg.Lookup("alpha")
	if !ok {
		t.Fatal(`Lookup("alpha") = _, false, want it to be found`)
	}
	if tpl.Version != "3" {
		t.Errorf("Version = %q, want %q", tpl.Version, "3")
	}
	if !tpl.VisibleTo("tenant-a") {
		t.Error("VisibleTo(tenant-a) = false, want true")
	}
	if tpl.VisibleTo("tenant-b") {
		t.Error("VisibleTo(tenant-b) = true, want false")
	}
}

func TestFromBundleRejectsDuplicateIDs(t *testing.T) {
	content := workflowtemplate.Content{Graph: json.RawMessage(testGraph)}
	snapshots := []workflowtemplate.Snapshot{
		{RevisionInfo: workflowtemplate.RevisionInfo{TemplateID: "alpha", Revision: 1}, Content: content},
		{RevisionInfo: workflowtemplate.RevisionInfo{TemplateID: "alpha", Revision: 2}, Content: content},
	}
	if _, err := workflow.FromBundle(snapshots); err == nil || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("FromBundle() error = %v, want an error naming the duplicated id", err)
	}
}

func TestFromBundleRejectsAnInvalidTemplate(t *testing.T) {
	snapshots := []workflowtemplate.Snapshot{
		{RevisionInfo: workflowtemplate.RevisionInfo{TemplateID: "bad", Revision: 1}, Content: workflowtemplate.Content{Graph: json.RawMessage(`{}`),
			Inputs: []workflowtemplate.Input{{Name: "p", Node: "missing", Field: "text", Type: workflowtemplate.InputString}}}},
	}
	if _, err := workflow.FromBundle(snapshots); err == nil {
		t.Fatal("FromBundle() error = nil, want the template's own validation error")
	}
}

func TestFromBundleWithNoSnapshotsReturnsAnEmptyRegistry(t *testing.T) {
	reg, err := workflow.FromBundle(nil)
	if err != nil {
		t.Fatalf("FromBundle() error = %v, want nil", err)
	}
	if reg.Len() != 0 {
		t.Errorf("Len() = %d, want 0", reg.Len())
	}
}
