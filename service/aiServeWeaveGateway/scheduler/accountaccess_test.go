package scheduler_test

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
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

func TestClaudeMessagesAndCodexChatUseSeparateExclusiveAccountsOnOneAgent(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	control := h.Connect("agent-one", "claude-a", "codex-a")
	claude := runtime.Snapshot{
		Descriptor: runtime.Descriptor{ID: "claude-a", Kind: runtime.KindClaude}, State: runtime.StateHealthy,
		Discovery: runtime.Discovery{Models: []runtime.Model{{ID: "claude-model", Capabilities: runtime.CapabilitySet{
			runtime.CapabilityMessages: {Level: runtime.SupportSupported}, runtime.CapabilityMessagesNative: {Level: runtime.SupportSupported},
		}}}},
	}
	codex := chatCapableSnapshot("codex-a", "codex-model")
	codex.Descriptor.Kind = runtime.KindCodex
	control.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full: true, ReportedAt: timestamppb.New(h.Clock.Now()), Snapshots: tunnelwire.SnapshotsToProto([]runtime.Snapshot{claude, codex}),
	}}})
	gatewaytest.WaitFor(t, "Claude Code and Codex on the same Agent", func() bool { node, _ := h.Srv.Node("agent-one"); return len(node.Runtimes) == 2 })
	handler := func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		switch req.GetRuntimeId() {
		case "claude-a":
			if req.GetOperation() != tunnelv1.Operation_OPERATION_MESSAGES {
				return errors.New("wrong Claude Code operation")
			}
			payload, err := tunnelwire.MarshalMessagesEvent(runtime.MessagesEvent{JSON: []byte(`{"type":"message_stop"}`)})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case "codex-a":
			return chatHandler("Codex account A", nil)(req, body, reply)
		default:
			return errors.New("unexpected account")
		}
	}
	h.OpenSlot("agent-one", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-one", handler)
	h.OpenSlot("agent-one", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-two", handler)
	gatewaytest.WaitFor(t, "account slots", func() bool { return gatewaytest.IdleCount(h, "agent-one") == 2 })
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	contextFor := func(mine string) context.Context {
		policy := accountaccess.Policy{Only: true}
		for _, id := range []string{"claude-a", "codex-a"} {
			lease := accountaccess.Lease{NodeID: "agent-one", RuntimeID: id}
			if id == mine {
				lease.ExpiresAt = h.Clock.Now().Add(time.Hour)
			}
			policy.Managed = append(policy.Managed, lease)
		}
		return scheduler.WithAccountAccess(scheduler.WithNodeAccess(context.Background(), "buyer", nodeaccess.Policy{DefaultDeny: true}), policy)
	}
	claudeContext, codexContext := contextFor("claude-a"), contextFor("codex-a")
	stream, selected, err := sched.MessagesStart(claudeContext, runtime.MessagesRequest{Model: "claude-model", TenantID: "claude-buyer", KeyID: "claude-key", Native: true, JSON: []byte(`{"model":"claude-model","messages":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Kind != runtime.KindClaude || selected.RuntimeID != "claude-a" {
		t.Fatal(selected)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	stream.Close()
	response, selected, err := sched.Chat(codexContext, runtime.ChatRequest{Model: "codex-model"})
	if err != nil || selected.Kind != runtime.KindCodex || selected.RuntimeID != "codex-a" || response.Message.Content != "served by Codex account A" {
		t.Fatal(response, selected, err)
	}
	if _, _, err := sched.Chat(claudeContext, runtime.ChatRequest{Model: "codex-model"}); !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Fatal("Claude buyer used Codex", err)
	}
	if _, _, err := sched.MessagesStart(codexContext, runtime.MessagesRequest{Model: "claude-model", Native: true}); !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Fatal("Codex buyer used Claude Code", err)
	}
	for _, test := range []struct {
		ctx   context.Context
		model string
	}{{claudeContext, "claude-model"}, {codexContext, "codex-model"}} {
		models := sched.Models(test.ctx)
		if len(models) != 1 || models[0].ID != test.model {
			t.Fatal("another account in model catalog", models)
		}
	}
}

func TestAgentDefaultPrivateSharedAndPinnedDispatch(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "agent", "cli", chatCapableSnapshot("cli", "model"), chatHandler("private", nil))
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	ctx := scheduler.WithNodeAccess(context.Background(), "tenant", nodeaccess.Policy{DefaultDeny: true})
	if len(sched.Models(ctx)) != 0 {
		t.Fatal("private Agent leaked through model discovery")
	}
	if _, _, err := sched.Chat(ctx, runtime.ChatRequest{Model: "model"}); !errors.Is(err, scheduler.ErrNoCapableNode) {
		t.Fatal("private Agent received ordinary tenant traffic", err)
	}
	if _, err := sched.Messages(ctx, scheduler.Candidate{NodeID: "agent", RuntimeID: "cli", Model: "model"}, runtime.MessagesRequest{Model: "model"}); !errors.Is(err, scheduler.ErrNodeNotAllowed) {
		t.Fatal("pinned request bypassed private Agent", err)
	}
	shared := scheduler.WithNodeAccess(context.Background(), "tenant", nodeaccess.Policy{DefaultDeny: true, Allowed: []string{"agent"}})
	if len(sched.Models(shared)) != 1 {
		t.Fatal("explicit sharing did not publish model")
	}
	response, _, err := sched.Chat(shared, runtime.ChatRequest{Model: "model"})
	if err != nil || response.Message.Content != "served by private" {
		t.Fatal("explicitly shared Agent did not serve tenant", response, err)
	}
}
