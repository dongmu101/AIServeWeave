package admission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/nodeload"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/hostresources"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

type scriptedSampler struct {
	mu    sync.Mutex
	usage hostresources.Usage
	err   error
}

func (s *scriptedSampler) set(cpuPct float64, availPct float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cpu := cpuPct / 100
	total := int64(1000)
	avail := int64(availPct * 10)
	s.usage = hostresources.Usage{CPUUtilization: &cpu, MemoryAvailableBytes: &avail, MemoryTotalBytes: &total}
	s.err = nil
}

func (s *scriptedSampler) fail() {
	s.mu.Lock()
	s.err = errors.New("probe broke")
	s.mu.Unlock()
}

func (s *scriptedSampler) Read() (hostresources.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage, s.err
}

func isBusy(err error) bool {
	var rtErr *runtime.RuntimeError
	return errors.As(err, &rtErr) && rtErr.Code == runtime.ErrorBackpressure && rtErr.Retryable
}

func TestZeroConfigOnlySamples(t *testing.T) {
	s := &scriptedSampler{}
	s.set(99, 1)
	c := New(Config{Sampler: s, Clock: newFakeClock()})
	c.Sample()
	c.Sample()
	if err := c.CheckPressure(ClassCLI); err != nil {
		t.Fatalf("disabled thresholds must never refuse: %v", err)
	}
	release, err := c.Acquire(context.Background(), ClassCLI)
	if err != nil {
		t.Fatalf("unlimited class must admit: %v", err)
	}
	release()
	report, ok := c.Report()
	if !ok || report.Pressure || report.CPUUtilization == nil {
		t.Fatalf("report = %+v ok=%v", report, ok)
	}
}

func TestReportAbsentBeforeFirstSample(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{err: errors.New("x")}, Clock: newFakeClock()})
	if _, ok := c.Report(); ok {
		t.Fatal("no report before a successful sample")
	}
}

func TestCPUPressureHysteresis(t *testing.T) {
	clock := newFakeClock()
	s := &scriptedSampler{}
	c := New(Config{Sampler: s, Clock: clock, CPUHighPercent: 80, HysteresisPercent: 10})

	s.set(90, 50)
	c.Sample()
	if c.CheckPressure(ClassCLI) != nil {
		t.Fatal("one high sample must not enter CPU pressure")
	}
	c.Sample()
	if err := c.CheckPressure(ClassCLI); !isBusy(err) {
		t.Fatalf("two high samples must enter pressure, got %v", err)
	}

	// Inside the hysteresis band: stays in pressure.
	s.set(75, 50)
	c.Sample()
	c.Sample()
	if c.CheckPressure(ClassCLI) == nil {
		t.Fatal("a value inside the band must not clear pressure")
	}

	// Below the exit mark but before the minimum hold: stays.
	s.set(50, 50)
	c.Sample()
	c.Sample()
	if c.CheckPressure(ClassCLI) == nil {
		t.Fatal("pressure must hold for the minimum duration")
	}
	clock.Advance(minPressureHold)
	c.Sample()
	if err := c.CheckPressure(ClassCLI); err != nil {
		t.Fatalf("pressure should clear after hold + exit samples: %v", err)
	}
}

func TestMemoryPressureEntersOnOneSample(t *testing.T) {
	clock := newFakeClock()
	s := &scriptedSampler{}
	c := New(Config{Sampler: s, Clock: clock, MemoryLowPercent: 10})
	s.set(10, 5)
	c.Sample()
	err := c.CheckPressure(ClassGeneral)
	if !isBusy(err) {
		t.Fatalf("low memory must enter pressure at once, got %v", err)
	}
	report, _ := c.Report()
	if !report.Pressure || len(report.PressureReasons) != 1 || report.PressureReasons[0] != nodeload.ReasonMemory {
		t.Fatalf("report reasons = %+v", report)
	}
	clock.Advance(minPressureHold)
	s.set(10, 30)
	c.Sample()
	c.Sample()
	if c.CheckPressure(ClassGeneral) != nil {
		t.Fatal("memory pressure should clear")
	}
}

func TestUnknownMeasurementContributesNothing(t *testing.T) {
	s := &scriptedSampler{}
	c := New(Config{Sampler: s, Clock: newFakeClock(), CPUHighPercent: 50, MemoryLowPercent: 10})
	c.Sample() // zero Usage: every field nil
	c.Sample()
	if c.CheckPressure(ClassCLI) != nil {
		t.Fatal("unmeasured values must not cause pressure")
	}
}

func TestSamplerFailureFailsOpen(t *testing.T) {
	s := &scriptedSampler{}
	c := New(Config{Sampler: s, Clock: newFakeClock(), MemoryLowPercent: 10})
	s.set(0, 1)
	c.Sample()
	if c.CheckPressure(ClassCLI) == nil {
		t.Fatal("setup: expected pressure")
	}
	s.fail()
	for i := 0; i < failOpenErrors; i++ {
		c.Sample()
	}
	if err := c.CheckPressure(ClassCLI); err != nil {
		t.Fatalf("a broken probe must not refuse work forever: %v", err)
	}
}

func TestAcquireFailFastWhenNoWait(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{}, Clock: newFakeClock(), CLIMax: 1})
	release, err := c.Acquire(context.Background(), ClassCLI)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), ClassCLI); !isBusy(err) {
		t.Fatalf("full class with no wait must refuse at once, got %v", err)
	}
	// The other class is independent.
	other, err := c.Acquire(context.Background(), ClassGeneral)
	if err != nil {
		t.Fatalf("general class must not share the cli semaphore: %v", err)
	}
	other()
	release()
	release() // extra release is a no-op
	again, err := c.Acquire(context.Background(), ClassCLI)
	if err != nil {
		t.Fatalf("slot must be free after release: %v", err)
	}
	again()
}

func TestAcquireWaitsThenAdmits(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{}, Clock: newFakeClock(), CLIMax: 1, QueueMaxWait: 5 * time.Second})
	release, _ := c.Acquire(context.Background(), ClassCLI)
	done := make(chan error, 1)
	go func() {
		r, err := c.Acquire(context.Background(), ClassCLI)
		if r != nil {
			r()
		}
		done <- err
	}()
	waitFor(t, func() bool { return c.Waiting() == 1 })
	release()
	if err := <-done; err != nil {
		t.Fatalf("waiter should be admitted after release: %v", err)
	}
}

func TestAcquireWaitTimesOut(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{}, Clock: newFakeClock(), GeneralMax: 1, QueueMaxWait: 30 * time.Millisecond})
	release, _ := c.Acquire(context.Background(), ClassGeneral)
	defer release()
	if _, err := c.Acquire(context.Background(), ClassGeneral); !isBusy(err) {
		t.Fatalf("wait timeout must be backpressure, got %v", err)
	}
	if c.Waiting() != 0 {
		t.Fatalf("waiter count leaked: %d", c.Waiting())
	}
}

func TestAcquireCanceledReturnsContextError(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{}, Clock: newFakeClock(), CLIMax: 1, QueueMaxWait: 5 * time.Second})
	release, _ := c.Acquire(context.Background(), ClassCLI)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Acquire(ctx, ClassCLI)
		done <- err
	}()
	waitFor(t, func() bool { return c.Waiting() == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait = %v, want context.Canceled", err)
	}
}

func TestAcquireRejectsBeyondMaxWaiters(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{}, Clock: newFakeClock(), CLIMax: 1, QueueMaxWait: 5 * time.Second, QueueMaxWaiters: 1})
	release, _ := c.Acquire(context.Background(), ClassCLI)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = c.Acquire(ctx, ClassCLI) }()
	waitFor(t, func() bool { return c.Waiting() == 1 })
	if _, err := c.Acquire(context.Background(), ClassCLI); !isBusy(err) {
		t.Fatalf("beyond max waiters must refuse at once, got %v", err)
	}
	cancel()
	waitFor(t, func() bool { return c.Waiting() == 0 })
	release()
}

func TestAcquireRechecksPressureAfterWait(t *testing.T) {
	s := &scriptedSampler{}
	c := New(Config{Sampler: s, Clock: newFakeClock(), CLIMax: 1, QueueMaxWait: 5 * time.Second, MemoryLowPercent: 10})
	s.set(0, 50)
	c.Sample()
	release, _ := c.Acquire(context.Background(), ClassCLI)
	done := make(chan error, 1)
	go func() {
		r, err := c.Acquire(context.Background(), ClassCLI)
		if r != nil {
			r()
		}
		done <- err
	}()
	waitFor(t, func() bool { return c.Waiting() == 1 })
	s.set(0, 1)
	c.Sample()
	release()
	if err := <-done; !isBusy(err) {
		t.Fatalf("pressure that arrived during the wait must refuse, got %v", err)
	}
	// The slot the refused waiter briefly held was returned.
	r, err := c.Acquire(context.Background(), ClassCLI)
	if err != nil {
		t.Fatalf("slot leaked: %v", err)
	}
	r()
}

func TestUnknownClassAdmits(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{}, Clock: newFakeClock(), CLIMax: 1, GeneralMax: 1})
	r, err := c.Acquire(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	r()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestExitMarkStaysReachableForExtremeThresholds(t *testing.T) {
	clock := newFakeClock()
	s := &scriptedSampler{}
	// memory_low 96 with hysteresis 5 would put the exit at 101%.
	c := New(Config{Sampler: s, Clock: clock, MemoryLowPercent: 96, CPUHighPercent: 4})
	s.set(10, 50)
	c.Sample()
	c.Sample()
	if c.CheckPressure(ClassCLI) == nil {
		t.Fatal("setup: expected pressure")
	}
	clock.Advance(minPressureHold)
	s.set(1, 100)
	c.Sample()
	c.Sample()
	if err := c.CheckPressure(ClassCLI); err != nil {
		t.Fatalf("a fully recovered machine must leave pressure: %v", err)
	}
}

func TestPersistentlyMissingMetricFailsOpen(t *testing.T) {
	s := &scriptedSampler{}
	c := New(Config{Sampler: s, Clock: newFakeClock(), MemoryLowPercent: 10})
	s.set(10, 1)
	c.Sample()
	if c.CheckPressure(ClassCLI) == nil {
		t.Fatal("setup: expected memory pressure")
	}
	// Memory stops being measurable while samples keep succeeding.
	s.mu.Lock()
	s.usage.MemoryAvailableBytes = nil
	s.mu.Unlock()
	for i := 0; i < failOpenErrors; i++ {
		c.Sample()
	}
	if err := c.CheckPressure(ClassCLI); err != nil {
		t.Fatalf("a metric that stays missing must not pin pressure: %v", err)
	}
}

func TestRequestDeadlineWhileWaitingIsNotBackpressure(t *testing.T) {
	c := New(Config{Sampler: &scriptedSampler{}, Clock: newFakeClock(), CLIMax: 1, QueueMaxWait: 5 * time.Second})
	release, _ := c.Acquire(context.Background(), ClassCLI)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Acquire(ctx, ClassCLI)
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorTimeout || rtErr.Retryable {
		t.Fatalf("expired request = %v, want a non-retryable timeout", err)
	}
}

func TestReportGoesStaleAfterRepeatedSampleFailures(t *testing.T) {
	s := &scriptedSampler{}
	s.set(10, 50)
	c := New(Config{Sampler: s, Clock: newFakeClock()})
	c.Sample()
	if _, ok := c.Report(); !ok {
		t.Fatal("setup: expected a report")
	}
	s.fail()
	for i := 0; i < failOpenErrors; i++ {
		c.Sample()
	}
	if _, ok := c.Report(); ok {
		t.Fatal("a stale sample must not be reported as live")
	}
}

func TestBothReasonsLabelMemory(t *testing.T) {
	s := &scriptedSampler{}
	c := New(Config{Sampler: s, Clock: newFakeClock(), CPUHighPercent: 50, MemoryLowPercent: 10})
	s.set(90, 1)
	c.Sample()
	c.Sample()
	report, _ := c.Report()
	if len(report.PressureReasons) != 2 {
		t.Fatalf("reasons = %v, want both", report.PressureReasons)
	}
	if err := c.CheckPressure(ClassCLI); err == nil || !strings.Contains(err.Error(), ReasonPressureMemory) {
		t.Fatalf("refusal = %v, want the memory label", err)
	}
}
