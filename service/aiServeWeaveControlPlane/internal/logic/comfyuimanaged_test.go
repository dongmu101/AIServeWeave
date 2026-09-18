package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/comfyuimanagedrouter"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

// fakeComfyUIManagedRouter is a logic.ComfyUIManagedRouter substitute, so
// these tests exercise TriggerComfyUIManagedAction without a real Gateway
// replica — the same reasoning fakeModelPullRouter exists for
// TriggerModelPull.
type fakeComfyUIManagedRouter struct {
	lastNodeID string
	lastAction comfyuimanagedstatus.Action
	result     comfyuimanagedrouter.Result
	err        error
}

func (f *fakeComfyUIManagedRouter) Trigger(_ context.Context, nodeID string, action comfyuimanagedstatus.Action) (comfyuimanagedrouter.Result, error) {
	f.lastNodeID = nodeID
	f.lastAction = action
	return f.result, f.err
}

var _ logic.ComfyUIManagedRouter = (*fakeComfyUIManagedRouter)(nil)

func TestTriggerComfyUIManagedActionForwardsAndAuditsWhenConnected(t *testing.T) {
	st := memstore.New()
	router := &fakeComfyUIManagedRouter{result: comfyuimanagedrouter.Result{Connected: true, Replicas: []comfyuimanagedrouter.ReplicaStatus{{Endpoint: "http://gateway-1:8093", Connected: true}}}}
	svc := logic.New(st, newFakeClock(), logic.WithComfyUIManagedRouter(router))
	actor := platformActor()

	result, err := svc.TriggerComfyUIManagedAction(context.Background(), actor, "node-1", comfyuimanagedstatus.ActionStart)
	if err != nil {
		t.Fatalf("TriggerComfyUIManagedAction: %v", err)
	}
	if !result.Connected {
		t.Errorf("result.Connected = false, want true")
	}
	if router.lastNodeID != "node-1" || router.lastAction != comfyuimanagedstatus.ActionStart {
		t.Errorf("router was called with (%q, %v), want (\"node-1\", ActionStart)", router.lastNodeID, router.lastAction)
	}

	page, err := st.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, entry := range page.Items {
		if entry.Action == model.ActionComfyUIManagedTrigger && entry.Target == "node-1" {
			found = true
		}
	}
	if !found {
		t.Error("no ActionComfyUIManagedTrigger audit entry was recorded")
	}
}

// TestTriggerComfyUIManagedActionReturnsNotFoundWhenNoReplicaHasTheNode
// asserts a call that reached the router but found nowhere to forward to is
// reported as ErrNotFound, and leaves no audit trail — the same rule
// TestTriggerModelPullReturnsNotFoundWhenNoReplicaHasTheNode covers.
func TestTriggerComfyUIManagedActionReturnsNotFoundWhenNoReplicaHasTheNode(t *testing.T) {
	st := memstore.New()
	router := &fakeComfyUIManagedRouter{result: comfyuimanagedrouter.Result{Connected: false}}
	svc := logic.New(st, newFakeClock(), logic.WithComfyUIManagedRouter(router))

	if _, err := svc.TriggerComfyUIManagedAction(context.Background(), platformActor(), "node-1", comfyuimanagedstatus.ActionStart); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("TriggerComfyUIManagedAction with no connected replica = %v, want ErrNotFound", err)
	}

	page, err := st.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{}, store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("an audit entry was recorded for a trigger that found nothing to act on: %+v", page.Items)
	}
}

func TestTriggerComfyUIManagedActionRejectsATenantActor(t *testing.T) {
	st := memstore.New()
	router := &fakeComfyUIManagedRouter{result: comfyuimanagedrouter.Result{Connected: true}}
	svc := logic.New(st, newFakeClock(), logic.WithComfyUIManagedRouter(router))
	tenantActor := logic.Actor{UserID: model.NewID(model.PrefixUser), TenantID: model.NewID(model.PrefixTenant), Role: model.RoleOwner}

	if _, err := svc.TriggerComfyUIManagedAction(context.Background(), tenantActor, "node-1", comfyuimanagedstatus.ActionStart); !errors.Is(err, logic.ErrForbidden) {
		t.Errorf("TriggerComfyUIManagedAction with a tenant actor = %v, want ErrForbidden", err)
	}
	if router.lastNodeID != "" {
		t.Errorf("the router was called (%q) for a request that should have been refused first", router.lastNodeID)
	}
}

func TestTriggerComfyUIManagedActionRequiresARouter(t *testing.T) {
	st := memstore.New()
	svc := logic.New(st, newFakeClock()) // no WithComfyUIManagedRouter

	if _, err := svc.TriggerComfyUIManagedAction(context.Background(), platformActor(), "node-1", comfyuimanagedstatus.ActionStart); !errors.Is(err, logic.ErrComfyUIManagedRouterUnconfigured) {
		t.Errorf("TriggerComfyUIManagedAction with no router = %v, want ErrComfyUIManagedRouterUnconfigured", err)
	}
}

func TestTriggerComfyUIManagedActionRejectsInvalidInput(t *testing.T) {
	st := memstore.New()
	router := &fakeComfyUIManagedRouter{result: comfyuimanagedrouter.Result{Connected: true}}
	svc := logic.New(st, newFakeClock(), logic.WithComfyUIManagedRouter(router))
	actor := platformActor()

	if _, err := svc.TriggerComfyUIManagedAction(context.Background(), actor, "", comfyuimanagedstatus.ActionStart); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("TriggerComfyUIManagedAction with an empty node id = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.TriggerComfyUIManagedAction(context.Background(), actor, "node-1", comfyuimanagedstatus.ActionUnspecified); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("TriggerComfyUIManagedAction with an unspecified action = %v, want ErrInvalidInput", err)
	}
}
