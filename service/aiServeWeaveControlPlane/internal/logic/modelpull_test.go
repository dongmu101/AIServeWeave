package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/modelpullrouter"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

// fakeModelPullRouter is a logic.ModelPullRouter substitute, so these tests
// exercise TriggerModelPull without a real Gateway replica — the same
// reasoning fakeRegistryClient exists for the node-ops methods.
type fakeModelPullRouter struct {
	lastNodeID string
	lastNames  []string
	result     modelpullrouter.Result
	err        error
}

func (f *fakeModelPullRouter) Trigger(_ context.Context, nodeID string, names []string) (modelpullrouter.Result, error) {
	f.lastNodeID = nodeID
	f.lastNames = names
	return f.result, f.err
}

var _ logic.ModelPullRouter = (*fakeModelPullRouter)(nil)

func platformActor() logic.Actor {
	return logic.Actor{
		UserID:   model.NewID(model.PrefixPlatformOperator),
		TenantID: model.PlatformScope,
		Role:     model.RolePlatformOperator,
		IP:       "10.0.0.1",
	}
}

func TestTriggerModelPullForwardsAndAuditsWhenConnected(t *testing.T) {
	st := memstore.New()
	router := &fakeModelPullRouter{result: modelpullrouter.Result{Connected: true, Replicas: []modelpullrouter.ReplicaStatus{{Endpoint: "http://gateway-1:8092", Connected: true}}}}
	svc := logic.New(st, newFakeClock(), logic.WithModelPullRouter(router))
	actor := platformActor()

	result, err := svc.TriggerModelPull(context.Background(), actor, "node-1", []string{"qwen3-coder:30b"})
	if err != nil {
		t.Fatalf("TriggerModelPull: %v", err)
	}
	if !result.Connected {
		t.Errorf("result.Connected = false, want true")
	}
	if router.lastNodeID != "node-1" || len(router.lastNames) != 1 || router.lastNames[0] != "qwen3-coder:30b" {
		t.Errorf("router was called with (%q, %v), want (\"node-1\", [\"qwen3-coder:30b\"])", router.lastNodeID, router.lastNames)
	}

	page, err := st.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, entry := range page.Items {
		if entry.Action == model.ActionModelPullTrigger && entry.Target == "node-1" {
			found = true
		}
	}
	if !found {
		t.Error("no ActionModelPullTrigger audit entry was recorded")
	}
}

// TestTriggerModelPullReturnsNotFoundWhenNoReplicaHasTheNode asserts a call
// that reached the router but found nowhere to forward to is reported as
// ErrNotFound, and leaves no audit trail — the same "nothing to act on
// leaves no audit" rule TestNodeOpsDoNotAuditOnFailure covers for the
// Registry-backed node-ops methods.
func TestTriggerModelPullReturnsNotFoundWhenNoReplicaHasTheNode(t *testing.T) {
	st := memstore.New()
	router := &fakeModelPullRouter{result: modelpullrouter.Result{Connected: false}}
	svc := logic.New(st, newFakeClock(), logic.WithModelPullRouter(router))

	if _, err := svc.TriggerModelPull(context.Background(), platformActor(), "node-1", []string{"m"}); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("TriggerModelPull with no connected replica = %v, want ErrNotFound", err)
	}

	page, err := st.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("an audit entry was recorded for a trigger that found nothing to act on: %+v", page.Items)
	}
}

func TestTriggerModelPullRejectsATenantActor(t *testing.T) {
	st := memstore.New()
	router := &fakeModelPullRouter{result: modelpullrouter.Result{Connected: true}}
	svc := logic.New(st, newFakeClock(), logic.WithModelPullRouter(router))
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if _, err := svc.TriggerModelPull(context.Background(), tenantActor, "node-1", []string{"m"}); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("TriggerModelPull with a tenant actor = %v, want ErrForbidden", err)
	}
	if router.lastNodeID != "" {
		t.Errorf("the router was called (%q) for a request that should have been refused first", router.lastNodeID)
	}
}

func TestTriggerModelPullRequiresAModelPullRouter(t *testing.T) {
	st := memstore.New()
	svc := logic.New(st, newFakeClock()) // no WithModelPullRouter

	if _, err := svc.TriggerModelPull(context.Background(), platformActor(), "node-1", []string{"m"}); !errors.Is(err, logic.ErrModelPullRouterUnconfigured) {
		t.Errorf("TriggerModelPull with no router = %v, want ErrModelPullRouterUnconfigured", err)
	}
}

func TestTriggerModelPullRejectsEmptyInput(t *testing.T) {
	st := memstore.New()
	router := &fakeModelPullRouter{result: modelpullrouter.Result{Connected: true}}
	svc := logic.New(st, newFakeClock(), logic.WithModelPullRouter(router))
	actor := platformActor()

	if _, err := svc.TriggerModelPull(context.Background(), actor, "", []string{"m"}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("TriggerModelPull with an empty node id = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.TriggerModelPull(context.Background(), actor, "node-1", nil); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("TriggerModelPull with no names = %v, want ErrInvalidInput", err)
	}
}
