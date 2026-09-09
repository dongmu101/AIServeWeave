package logic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

const wtGraph = `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":"a cat"}}}`

func wtContent(description string) workflowtemplate.Content {
	return workflowtemplate.Content{
		Description: description,
		Inputs:      []workflowtemplate.Input{{Name: "prompt", Node: "6", Field: "text", Type: workflowtemplate.InputString}},
		Graph:       json.RawMessage(wtGraph),
	}
}

func TestWorkflowTemplateConcurrentCreateAndImmutableRollback(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := logic.New(st, nil)
	actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
	content := wtContent("old")
	var wg sync.WaitGroup
	var winners atomic.Int32
	for range 20 {
		wg.Go(func() {
			_, err := s.PublishWorkflowTemplate(ctx, actor, "alpha", 0, content, nil)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, logic.ErrConflict) {
				t.Errorf("publish error=%v want conflict", err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d want 1", winners.Load())
	}

	content.Description = "mutated after publish"
	original, err := s.WorkflowTemplateRevision(ctx, "alpha", 1)
	if err != nil || original.Description != "old" {
		t.Fatalf("snapshot=%+v err=%v want immutable old", original, err)
	}

	got, err := s.RollbackWorkflowTemplate(ctx, actor, "alpha", 1, 1)
	if err != nil || got.Revision != 2 || got.RollbackOf != 1 || got.Description != "old" {
		t.Fatalf("rollback=%+v err=%v want immutable revision 2", got, err)
	}

	audit, err := st.ListAudit(ctx, model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil || len(audit.Items) != 2 {
		t.Fatalf("audit=%+v err=%v want 2", audit, err)
	}

	if _, err := s.PublishWorkflowTemplate(ctx, logic.Actor{TenantID: "tenant", Role: model.RoleOwner}, "alpha", 2, content, nil); !errors.Is(err, logic.ErrForbidden) {
		t.Fatalf("tenant error=%v want forbidden", err)
	}
	invalid := workflowtemplate.Content{Inputs: []workflowtemplate.Input{}, Graph: json.RawMessage(`"nope"`)}
	if _, err := s.PublishWorkflowTemplate(ctx, actor, "alpha", 2, invalid, nil); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("invalid error=%v want invalid input", err)
	}
}

func TestWorkflowTemplatesAreIndependentlyVersioned(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := logic.New(st, nil)
	actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}

	if _, err := s.PublishWorkflowTemplate(ctx, actor, "alpha", 0, wtContent("a1"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishWorkflowTemplate(ctx, actor, "beta", 0, wtContent("b1"), []string{"tenant-x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishWorkflowTemplate(ctx, actor, "alpha", 1, wtContent("a2"), nil); err != nil {
		t.Fatal(err)
	}

	alpha, err := s.WorkflowTemplate(ctx, "alpha")
	if err != nil || alpha.Revision != 2 || alpha.Description != "a2" {
		t.Fatalf("alpha=%+v err=%v want revision 2 a2", alpha, err)
	}
	beta, err := s.WorkflowTemplate(ctx, "beta")
	if err != nil || beta.Revision != 1 || len(beta.VisibleTenantIDs) != 1 || beta.VisibleTenantIDs[0] != "tenant-x" {
		t.Fatalf("beta=%+v err=%v want revision 1 with tenant-x visible", beta, err)
	}

	items, err := s.ListWorkflowTemplates(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("list=%+v err=%v want 2 templates", items, err)
	}

	bundle, err := s.CurrentWorkflowTemplatesBundle(ctx)
	if err != nil || len(bundle) != 2 {
		t.Fatalf("bundle=%+v err=%v want 2 snapshots", bundle, err)
	}

	if _, err := s.WorkflowTemplate(ctx, "never-published"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("never-published err=%v want not found", err)
	}
}

func TestWorkflowTemplateRejectCorruptStoredSnapshotsBeforeRollback(t *testing.T) {
	for _, tc := range []struct{ name, inputs, digest string }{
		{"invalid inputs json", "not json", "bad"},
		{"digest mismatch", "[]", "bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := memstore.New()
			row := model.WorkflowTemplateRevision{InputsJSON: tc.inputs, OutputsJSON: "[]", DependenciesJSON: "{}", VisibleTenantsJSON: "[]", GraphJSON: wtGraph, Digest: tc.digest}
			audit := model.AuditLog{}
			if err := st.PublishWorkflowTemplateRevision(ctx, "alpha", 0, &row, &audit); err != nil {
				t.Fatal(err)
			}
			s := logic.New(st, nil)
			if _, err := s.WorkflowTemplate(ctx, "alpha"); err == nil {
				t.Fatal("corrupt current read succeeded; want error")
			}
			actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
			if _, err := s.RollbackWorkflowTemplate(ctx, actor, "alpha", 1, 1); err == nil {
				t.Fatal("corrupt rollback succeeded; want error")
			}
			current, err := st.CurrentWorkflowTemplateRevision(ctx, "alpha")
			if err != nil || current.Revision != 1 {
				t.Fatalf("current revision=%d err=%v want unchanged 1", current.Revision, err)
			}
		})
	}
}

func TestWorkflowTemplateHistoryCapacityPreservesCurrent(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := logic.New(st, nil)
	actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
	for i := int64(0); i < workflowtemplate.MaxRevisionsPerTemplate; i++ {
		if _, err := s.PublishWorkflowTemplate(ctx, actor, "alpha", i, wtContent("d"), nil); err != nil {
			t.Fatalf("publish %d err=%v want nil", i, err)
		}
	}
	if _, err := s.PublishWorkflowTemplate(ctx, actor, "alpha", workflowtemplate.MaxRevisionsPerTemplate, wtContent("d"), nil); !errors.Is(err, logic.ErrWorkflowTemplateCapacity) {
		t.Fatalf("full history err=%v want capacity", err)
	}
	current, err := s.WorkflowTemplate(ctx, "alpha")
	if err != nil || current.Revision != workflowtemplate.MaxRevisionsPerTemplate {
		t.Fatalf("current=%+v err=%v want capacity revision", current, err)
	}
	page, err := s.WorkflowTemplateHistoryPage(ctx, "alpha", 0, 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].Revision != workflowtemplate.MaxRevisionsPerTemplate || page.NextBefore != workflowtemplate.MaxRevisionsPerTemplate-1 {
		t.Fatalf("history=%+v err=%v want two newest with cursor", page, err)
	}
}

func TestWorkflowTemplateLimitOnlyAppliesToNewTemplates(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := logic.New(st, nil)
	actor := logic.Actor{UserID: "operator", TenantID: model.PlatformScope, Role: model.RolePlatformOperator}
	for i := 0; i < workflowtemplate.MaxTemplates; i++ {
		id := fmt.Sprintf("tpl-%03d", i)
		if _, err := s.PublishWorkflowTemplate(ctx, actor, id, 0, wtContent("d"), nil); err != nil {
			t.Fatalf("publish %s err=%v want nil", id, err)
		}
	}
	if _, err := s.PublishWorkflowTemplate(ctx, actor, "one-too-many", 0, wtContent("d"), nil); !errors.Is(err, logic.ErrWorkflowTemplateLimit) {
		t.Fatalf("new template at limit err=%v want ErrWorkflowTemplateLimit", err)
	}
	// Republishing an existing template is unaffected by the limit: only a
	// new distinct id is checked against it.
	//
	// 重新发布一个已存在的模板不受该上限影响：只有新增的、不同的 id 才会被检查。
	if _, err := s.PublishWorkflowTemplate(ctx, actor, "tpl-000", 1, wtContent("d2"), nil); err != nil {
		t.Fatalf("republish existing template at limit err=%v want nil", err)
	}
}
