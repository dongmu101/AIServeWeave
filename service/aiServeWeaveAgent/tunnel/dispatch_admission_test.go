package tunnel_test

import (
	"context"
	"sync"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// fakeAdmitter records how the dispatcher consulted node-wide admission.
type fakeAdmitter struct {
	mu         sync.Mutex
	pressure   bool
	refuse     bool
	gates      []string
	acquires   []string
	releases   int
	acquireErr error
}

func (a *fakeAdmitter) CheckPressure(class string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gates = append(a.gates, class)
	if a.pressure {
		return &runtime.RuntimeError{Code: runtime.ErrorBackpressure, Operation: "admission", Retryable: true, Message: "pressure"}
	}
	return nil
}

func (a *fakeAdmitter) Acquire(_ context.Context, class string) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acquires = append(a.acquires, class)
	if a.refuse {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorBackpressure, Operation: "admission", Retryable: true, Message: "full"}
	}
	return func() {
		a.mu.Lock()
		a.releases++
		a.mu.Unlock()
	}, nil
}

func (a *fakeAdmitter) snapshot() (gates, acquires []string, releases int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.gates...), append([]string(nil), a.acquires...), a.releases
}

func admissionFixture(t *testing.T, adm *fakeAdmitter) *dispatchFixture {
	return newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) { cfg.Admission = adm })
}

func installKind(f *dispatchFixture, kind runtime.Kind, maxConcurrent int) *tunneltest.InferenceRuntime {
	rt := f.inference(maxConcurrent)
	rt.Desc.Kind = kind
	return rt
}

func chatPayload(t *testing.T) []byte {
	return mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "m"})
}

func TestAdmissionClassesByRuntimeKind(t *testing.T) {
	tests := []struct {
		kind      runtime.Kind
		wantGate  bool
		wantClass string
	}{
		{runtime.KindOllama, true, "general"},
		{runtime.KindCodex, true, "cli"},
		{runtime.KindClaude, true, "cli"},
		{runtime.KindOpenAI, false, ""},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			adm := &fakeAdmitter{}
			f := admissionFixture(t, adm)
			installKind(f, tc.kind, 4)
			if _, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, chatPayload(t), nil); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			gates, acquires, releases := adm.snapshot()
			if (len(gates) == 1) != tc.wantGate {
				t.Fatalf("gates = %v, want gate=%v", gates, tc.wantGate)
			}
			if tc.wantClass == "" {
				if len(acquires) != 0 {
					t.Fatalf("hosted runtime must take no slot, got %v", acquires)
				}
				return
			}
			if len(acquires) != 1 || acquires[0] != tc.wantClass || releases != 1 {
				t.Fatalf("acquires = %v releases = %d, want one %q slot released", acquires, releases, tc.wantClass)
			}
		})
	}
}

func TestAdmissionPressureRefusesBeforeTheBackend(t *testing.T) {
	adm := &fakeAdmitter{pressure: true}
	f := admissionFixture(t, adm)
	called := false
	installKind(f, runtime.KindOllama, 1).ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
		called = true
		return runtime.ChatResponse{}, nil
	}
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, chatPayload(t), nil)
	re := wantCode(t, err, runtime.ErrorBackpressure)
	if !re.Retryable || re.RuntimeID != testRuntimeID {
		t.Fatalf("refusal = %+v, want retryable with runtime id", re)
	}
	if called {
		t.Fatal("the backend must not be called under pressure")
	}
	if _, acquires, _ := adm.snapshot(); len(acquires) != 0 {
		t.Fatalf("no slot may be taken after a pressure refusal, got %v", acquires)
	}

	// The runtime limiter (max 1) was never taken, so the next request runs.
	adm.mu.Lock()
	adm.pressure = false
	adm.mu.Unlock()
	if _, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, chatPayload(t), nil); err != nil {
		t.Fatalf("dispatch after pressure cleared: %v", err)
	}
}

func TestAdmissionSlotRefusalReleasesTheRuntimeLimiter(t *testing.T) {
	adm := &fakeAdmitter{refuse: true}
	f := admissionFixture(t, adm)
	installKind(f, runtime.KindOllama, 1)
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, chatPayload(t), nil)
	wantCode(t, err, runtime.ErrorBackpressure)

	adm.mu.Lock()
	adm.refuse = false
	adm.mu.Unlock()
	if _, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, chatPayload(t), nil); err != nil {
		t.Fatalf("the runtime limiter leaked its only permit: %v", err)
	}
}

func TestAdmissionExemptsStatusAndListModels(t *testing.T) {
	adm := &fakeAdmitter{pressure: true, refuse: true}
	f := admissionFixture(t, adm)
	installKind(f, runtime.KindOllama, 4)
	if _, err := f.dispatch(tunnelv1.Operation_OPERATION_LIST_MODELS, nil, nil); err != nil {
		t.Fatalf("LIST_MODELS must be exempt: %v", err)
	}

	f.workflow(4).StatusFunc = func(context.Context, string) (runtime.WorkflowStatus, error) {
		return runtime.WorkflowStatus{State: runtime.WorkflowRunning}, nil
	}
	payload := mustMarshal(t, tunnelwire.MarshalRunRef, testRunID)
	if _, err := f.dispatch(tunnelv1.Operation_OPERATION_WORKFLOW_STATUS, payload, nil); err != nil {
		t.Fatalf("WORKFLOW_STATUS must be exempt: %v", err)
	}
	gates, acquires, _ := adm.snapshot()
	if len(gates) != 0 || len(acquires) != 0 {
		t.Fatalf("exempt operations consulted admission: gates %v acquires %v", gates, acquires)
	}
}

func TestAdmissionWorkflowSubmitIsGateOnly(t *testing.T) {
	adm := &fakeAdmitter{pressure: true}
	f := admissionFixture(t, adm)
	f.workflow(4)
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT, nil, nil)
	wantCode(t, err, runtime.ErrorBackpressure)
	gates, acquires, _ := adm.snapshot()
	if len(gates) != 1 || gates[0] != "workflow" || len(acquires) != 0 {
		t.Fatalf("submit must pass the gate only: gates %v acquires %v", gates, acquires)
	}
}
