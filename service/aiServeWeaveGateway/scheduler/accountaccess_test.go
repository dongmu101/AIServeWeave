package scheduler_test

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

type accountPolicyClock struct {
	runtime.Clock
	now time.Time
}

func (c *accountPolicyClock) Now() time.Time { return c.now }

func TestMonthlyAccountIsolatesRuntimesAndExpiresCachedPolicy(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	control := h.Connect("agent", "mine", "other", "shared")
	control.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full: true, ReportedAt: timestamppb.New(h.Clock.Now()), Snapshots: tunnelwire.SnapshotsToProto([]runtime.Snapshot{
			chatCapableSnapshot("mine", "my-model"), chatCapableSnapshot("other", "other-model"), chatCapableSnapshot("shared", "shared-model"),
		}),
	}}})
	gatewaytest.WaitFor(t, "all three runtime reports", func() bool { node, _ := h.Srv.Node("agent"); return len(node.Runtimes) == 3 })
	clock := &accountPolicyClock{Clock: h.Clock, now: h.Clock.Now()}
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: clock})
	policy := accountaccess.Policy{Only: true, Managed: []accountaccess.Lease{
		{NodeID: "agent", RuntimeID: "mine", ExpiresAt: h.Clock.Now().Add(time.Hour)},
		{NodeID: "agent", RuntimeID: "other"},
	}}
	ctx := scheduler.WithAccountAccess(context.Background(), policy)
	models := sched.Models(ctx)
	if len(models) != 1 || models[0].ID != "my-model" {
		t.Fatalf("catalog leaks another runtime: %+v", models)
	}
	for _, model := range []string{"other-model", "shared-model"} {
		if _, _, err := sched.Chat(ctx, runtime.ChatRequest{Model: model}); !errors.Is(err, scheduler.ErrNoCapableNode) {
			t.Fatalf("%s admitted: %v", model, err)
		}
	}
	clock.now = clock.now.Add(time.Hour)
	if models := sched.Models(ctx); len(models) != 0 {
		t.Fatalf("cached policy survives expiry: %+v", models)
	}
	if _, _, err := sched.Chat(ctx, runtime.ChatRequest{Model: "my-model"}); !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Fatal(err)
	}
	// Pinned native tool continuations must reject the same expired lease.
	if _, err := sched.Messages(ctx, scheduler.Candidate{NodeID: "agent", RuntimeID: "mine", Model: "my-model"}, runtime.MessagesRequest{Model: "my-model"}); !errors.Is(err, scheduler.ErrNodeNotAllowed) {
		t.Fatal("pinned continuation escaped expiry", err)
	}
	// A credit-billed tenant retains shared runtimes but cannot use inventory
	// merely because its lease has expired or the offer is unpurchased.
	policy.Only = false
	models = sched.Models(scheduler.WithAccountAccess(context.Background(), policy))
	if len(models) != 1 || models[0].ID != "shared-model" {
		t.Fatal(models)
	}
}

func TestMonthlyAccountRetryDoesNotSpillToSharedAccount(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "owned", "mine", chatCapableSnapshot("mine", "model"), failingHandler(nil))
	connectNode(t, h, "shared", "free", chatCapableSnapshot("free", "model"), chatHandler("shared", nil))
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	ctx := scheduler.WithAccountAccess(context.Background(), accountaccess.Policy{Only: true, Managed: []accountaccess.Lease{{NodeID: "owned", RuntimeID: "mine", ExpiresAt: h.Clock.Now().Add(time.Hour)}}})
	if _, candidate, err := sched.Chat(ctx, runtime.ChatRequest{Model: "model"}); err == nil || candidate.NodeID == "shared" {
		t.Fatal("retry spilled to an unpurchased account", candidate, err)
	}
}
