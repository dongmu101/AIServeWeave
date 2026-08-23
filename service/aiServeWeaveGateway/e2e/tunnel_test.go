package e2e

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

func TestEveryReplicaServesTheNodeIndependently(t *testing.T) {
	// The first completion criterion for the tunnel: a request may run
	// through any replica, and none of them forwards to another. The proof is
	// that each replica answers from its own node table and its own slots,
	// and that the backend saw exactly one call per replica.
	f := newFleet(t, 3)
	f.awaitReady(f.replicas...)

	before := backend.chatCalls.Load()
	for _, r := range f.replicas {
		rt := r.server.Runtime("mac-mini-01", "backend-1")
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		resp, err := rt.Chat(ctx, runtime.ChatRequest{
			Model:    "e2e-model",
			Messages: []runtime.ChatMessage{{Role: "user", Content: r.id}},
		})
		cancel()
		if err != nil {
			t.Fatalf("chat through %s: %v", r.id, err)
		}
		if want := "echo:" + r.id; resp.Message.Content != want {
			t.Errorf("chat through %s returned %q, want %q", r.id, resp.Message.Content, want)
		}
	}

	if got := backend.chatCalls.Load() - before; got != int64(len(f.replicas)) {
		t.Errorf("backend served %d completions, want %d: a request was forwarded or replayed", got, len(f.replicas))
	}

	// Every replica announces its own identity, which is what makes a node's
	// logs and metrics attributable to one link rather than to "the Gateway".
	seen := make(map[string]bool)
	for _, r := range f.replicas {
		if seen[r.server.ReplicaID()] {
			t.Errorf("replica id %q is used twice", r.server.ReplicaID())
		}
		seen[r.server.ReplicaID()] = true
	}
}

func TestStreamingArrivesEventByEventOverRealTLS(t *testing.T) {
	backend.tokens.Store(6)
	backend.firstTokenDelay.Store(0)
	f := newFleet(t, 1)
	f.awaitReady(f.replicas...)

	rt := f.replicas[0].server.Runtime("mac-mini-01", "backend-1")
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	stream, err := rt.ChatStream(ctx, runtime.ChatRequest{
		Model:    "e2e-model",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	var got []string
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got = append(got, ev.Delta.Content)
	}
	if want := "t0t1t2t3t4t5"; strings.Join(got, "") != want {
		t.Errorf("streamed content = %q, want %q", strings.Join(got, ""), want)
	}
	if len(got) != 6 {
		t.Errorf("received %d events, want 6: events were merged somewhere on the path", len(got))
	}
}

func TestKillingOneReplicaLeavesTheOthersServing(t *testing.T) {
	// Fault injection, single replica down. The Agent's other tunnels are
	// separate TCP connections to separate processes, so nothing about them
	// should change.
	f := newFleet(t, 3)
	f.awaitReady(f.replicas...)

	dead := f.replicas[0]
	dead.stop()

	survivors := f.awaitReadyExcept(dead)
	for _, r := range survivors {
		rt := r.server.Runtime("mac-mini-01", "backend-1")
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		resp, err := rt.Chat(ctx, runtime.ChatRequest{
			Model:    "e2e-model",
			Messages: []runtime.ChatMessage{{Role: "user", Content: "after the kill"}},
		})
		cancel()
		if err != nil {
			t.Fatalf("chat through surviving replica %s: %v", r.id, err)
		}
		if resp.Message.Content != "echo:after the kill" {
			t.Errorf("replica %s returned %q", r.id, resp.Message.Content)
		}
	}

	// The dead replica's own view is gone with the process; asking it is not
	// a route to the node any more.
	if _, ok := dead.server.Node("mac-mini-01"); ok {
		if _, err := dead.server.Runtime("mac-mini-01", "backend-1").Chat(context.Background(), runtime.ChatRequest{
			Model:    "e2e-model",
			Messages: []runtime.ChatMessage{{Role: "user", Content: "x"}},
		}); err == nil {
			t.Error("the killed replica still served a request")
		}
	}
}

func TestAgentReconnectsToARestartedReplica(t *testing.T) {
	// Rolling upgrade in miniature: one replica goes away and a new process
	// comes back on the same address. The Agent must find it again on its own,
	// and must keep serving through the others the whole time.
	f := newFleet(t, 2)
	f.awaitReady(f.replicas...)

	dead := f.replicas[0]
	addr := dead.addr
	dead.stop()

	survivors := f.awaitReadyExcept(dead)
	if len(survivors) == 0 {
		t.Fatal("no replica survived")
	}

	replacement := f.restartReplica(dead, addr)
	f.awaitReady(replacement)

	rt := replacement.server.Runtime("mac-mini-01", "backend-1")
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	resp, err := rt.Chat(ctx, runtime.ChatRequest{
		Model:    "e2e-model",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "restarted"}},
	})
	if err != nil {
		t.Fatalf("chat through the restarted replica: %v", err)
	}
	if resp.Message.Content != "echo:restarted" {
		t.Errorf("restarted replica returned %q", resp.Message.Content)
	}
}

func TestSaturatedNodeReportsBackpressureRatherThanQueueing(t *testing.T) {
	// The slot pool has a ceiling, so a replica can run out of parked slots.
	// When it does the answer is immediate backpressure, marked retryable so
	// a scheduler places the request on another node instead of waiting.
	backend.tokens.Store(1)
	backend.firstTokenDelay.Store(2 * int64(time.Second))
	f := newFleet(t, 1)
	f.awaitReady(f.replicas...)

	r := f.replicas[0]
	var held []*tunnelserver.Response
	defer func() {
		for _, resp := range held {
			resp.Close()
		}
	}()

	// Occupy every parked slot with a stream whose first token has not
	// arrived yet, then ask for one more.
	var last error
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		resp, err := r.server.Dispatch(context.Background(), tunnelserver.Request{
			NodeID:    "mac-mini-01",
			RuntimeID: "backend-1",
			Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
			Payload:   mustChatPayload(t),
		})
		if err != nil {
			last = err
			break
		}
		held = append(held, resp)
	}

	if last == nil {
		t.Fatal("the node never ran out of slots; the pool has no ceiling")
	}
	if !errors.Is(last, tunnelserver.ErrNoIdleSlot) {
		t.Fatalf("saturation error = %v, want one wrapping ErrNoIdleSlot", last)
	}
	var rtErr *runtime.RuntimeError
	if !errors.As(last, &rtErr) {
		t.Fatalf("saturation error = %v (%T), want a *runtime.RuntimeError", last, last)
	}
	if rtErr.Code != runtime.ErrorBackpressure {
		t.Errorf("Code = %s, want %s", rtErr.Code, runtime.ErrorBackpressure)
	}
	if !rtErr.Retryable {
		t.Error("Retryable = false; a request that never reached the backend must be placeable on another node")
	}
}

func TestTunnelSegmentLatency(t *testing.T) {
	// The tunnel's own share of time to first token, measured against the same
	// backend called in-process. The absolute numbers belong to this machine
	// and this loopback link, so they are logged rather than asserted; what is
	// asserted is the shape: one hop over loopback TLS must not cost a
	// noticeable fraction of a second, because a regression there is the
	// difference between a responsive stream and a stalled one.
	const samples = 20
	backend.tokens.Store(8)
	backend.firstTokenDelay.Store(0)

	f := newFleet(t, 1)
	f.awaitReady(f.replicas...)
	rt := f.replicas[0].server.Runtime("mac-mini-01", "backend-1")

	direct, ok := f.manager.Get("backend-1")
	if !ok {
		t.Fatal("the backend instance is not registered")
	}
	local := direct.(runtime.InferenceRuntime)

	req := runtime.ChatRequest{
		Model:    "e2e-model",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "latency"}},
	}

	measure := func(open func(context.Context) (runtime.Stream[runtime.ChatEvent], error)) time.Duration {
		t.Helper()
		var total time.Duration
		for range samples {
			ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
			start := time.Now()
			stream, err := open(ctx)
			if err != nil {
				cancel()
				t.Fatalf("opening the stream: %v", err)
			}
			if _, err := stream.Recv(); err != nil {
				stream.Close()
				cancel()
				t.Fatalf("first event: %v", err)
			}
			total += time.Since(start)
			// Drain so the slot is released for the next sample rather than
			// being cancelled, which is the steady-state path.
			for {
				if _, err := stream.Recv(); err != nil {
					break
				}
			}
			stream.Close()
			cancel()
		}
		return total / samples
	}

	localTTFT := measure(func(ctx context.Context) (runtime.Stream[runtime.ChatEvent], error) {
		return local.ChatStream(ctx, req)
	})
	tunnelTTFT := measure(func(ctx context.Context) (runtime.Stream[runtime.ChatEvent], error) {
		return rt.ChatStream(ctx, req)
	})

	overhead := tunnelTTFT - localTTFT
	t.Logf("TTFT over %d samples: in-process %v, through the tunnel %v, tunnel overhead %v",
		samples, localTTFT, tunnelTTFT, overhead)

	if overhead > 100*time.Millisecond {
		t.Errorf("tunnel added %v to time to first token over loopback; a single mTLS hop must cost far less", overhead)
	}
}

// mustChatPayload marshals a minimal streaming chat request.
func mustChatPayload(t *testing.T) []byte {
	t.Helper()
	payload, err := chatRequestPayload(runtime.ChatRequest{
		Model:    "e2e-model",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "saturate"}},
	})
	if err != nil {
		t.Fatalf("marshalling a chat request: %v", err)
	}
	return payload
}
