package tunnel_test

import (
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeload"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// fakeLoad is a tunnel.LoadSource test double.
type fakeLoad struct {
	mu     sync.Mutex
	report nodeload.Report
	ok     bool
}

func (f *fakeLoad) Report() (nodeload.Report, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.report, f.ok
}

func (f *fakeLoad) set(r nodeload.Report) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.report, f.ok = r, true
}

func TestControlHeartbeatCarriesLoad(t *testing.T) {
	cpu := 0.75
	load := &fakeLoad{}
	load.set(nodeload.Report{SampledAt: tunnelNow, CPUUtilization: &cpu, Pressure: true, PressureReasons: []nodeload.Reason{nodeload.ReasonCPU}})
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		isolateHeartbeat(cfg)
		cfg.Load = load
	})
	f.start()
	sess := f.connect()

	f.advance(15*time.Second, 1)
	hb := f.recv(sess).GetHeartbeat()
	if hb == nil || hb.GetLoad() == nil {
		t.Fatalf("heartbeat = %v, want one carrying load", hb)
	}
	if hb.GetLoad().GetCpuUtilization() != 0.75 || !hb.GetLoad().GetPressure() ||
		len(hb.GetLoad().GetPressureReasons()) != 1 ||
		hb.GetLoad().GetPressureReasons()[0] != tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_CPU {
		t.Fatalf("load = %v", hb.GetLoad())
	}
}

func TestControlHeartbeatWithoutLoadSourceCarriesNone(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  tunnel.LoadSource
	}{
		{"nil source", nil},
		{"no sample yet", &fakeLoad{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
				isolateHeartbeat(cfg)
				cfg.Load = tc.src
			})
			f.start()
			sess := f.connect()
			f.advance(15*time.Second, 1)
			hb := f.recv(sess).GetHeartbeat()
			if hb == nil || hb.GetLoad() != nil {
				t.Fatalf("heartbeat = %v, want one without load", hb)
			}
		})
	}
}

func TestControlPressureFlipSendsAnExtraHeartbeat(t *testing.T) {
	load := &fakeLoad{}
	load.set(nodeload.Report{SampledAt: tunnelNow})
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		isolateStatus(cfg)
		cfg.Load = load
	})
	f.start()
	sess := f.connect()

	// The first poll reports the verdict the replica has not seen yet.
	f.advance(2*time.Second, 1)
	if hb := f.recv(sess).GetHeartbeat(); hb == nil || hb.GetLoad() == nil || hb.GetLoad().GetPressure() {
		t.Fatalf("first poll = %v, want a heartbeat without pressure", hb)
	}

	// Unchanged verdict: quiet.
	f.advance(2*time.Second, 1)
	expectNoStatus(f, sess, 1)

	// Flip: the next poll carries it without waiting for the heartbeat timer.
	load.set(nodeload.Report{SampledAt: tunnelNow, Pressure: true, PressureReasons: []nodeload.Reason{nodeload.ReasonMemory}})
	f.advance(2*time.Second, 1)
	if hb := f.recv(sess).GetHeartbeat(); hb == nil || !hb.GetLoad().GetPressure() {
		t.Fatalf("after flip = %v, want a heartbeat with pressure", hb)
	}
	f.advance(2*time.Second, 1)
	expectNoStatus(f, sess, 2)
}
