// Package admission keeps an Agent from overloading its own machine. It
// samples live CPU and memory (hostresources.UsageReader), turns them into a
// pressure verdict with hysteresis, and holds two node-wide semaphores — one
// for local CLI runtimes, which spawn a process per request, and one for
// other local backends — on top of each runtime's own concurrency limiter.
//
// Every refusal is a retryable ErrorBackpressure, the answer the Gateway
// already treats as "this node is busy, try another": the request never
// reached a backend, so it may safely run elsewhere. The semaphores may wait
// a bounded time first (QueueMaxWait, QueueMaxWaiters); the pressure gate
// never waits, since a loaded machine does not recover in seconds.
//
// The verdict and the latest sample are also reported on the heartbeat
// (Report), so the Gateway can rank a node under pressure last before it
// ever has to be refused.
//
// admission 防止 Agent 把自己所在的机器压垮。它采集实时 CPU 与内存
// （hostresources.UsageReader），经迟滞转换为资源紧张判断，并在每个运行时自身的并发
// 限流器之上再加两个整机信号量——一个给按请求启动进程的本地 CLI 运行时，一个给其他
// 本地后端。
//
// 每一次拒绝都是可重试的 ErrorBackpressure，也就是 Gateway 早已视为「这个节点忙，
// 换一个」的回答：请求从未到达后端，因此可以安全地在别处执行。信号量可以先有界等待
// （QueueMaxWait、QueueMaxWaiters）；资源闸门从不等待，因为负载过高的机器不会在几秒内
// 恢复。
//
// 判断结果与最近一次采样也经心跳上报（Report），让 Gateway 在需要拒绝之前就把资源紧张
// 的节点排到最后。
package admission

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"AIServeWeave/common/nodeload"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/hostresources"
)

// Classes of admitted work. They are plain strings so the tunnel package can
// name them without importing this one.
//
// 准入工作的分类。它们是普通字符串，让 tunnel 包无需 import 本包即可引用。
const (
	ClassCLI     = "cli"
	ClassGeneral = "general"
)

// Defaults applied by New.
const (
	DefaultSampleInterval    = 5 * time.Second
	DefaultHysteresisPercent = 5
	DefaultQueueMaxWaiters   = 16

	// minPressureHold keeps a node in pressure at least this long once it
	// entered, so the Gateway's view does not flap between heartbeats.
	// minPressureHold 让节点一旦进入资源紧张状态至少保持这么久，避免 Gateway 的视图在
	// 两次心跳之间来回跳动。
	minPressureHold = 15 * time.Second
	// cpuEnterSamples / exitSamples are the consecutive samples needed to
	// change state. Memory enters on a single sample: running out of memory
	// is abrupt and its cost (the OOM killer) is far higher than a refusal.
	// cpuEnterSamples / exitSamples 是改变状态所需的连续样本数。内存只需一个样本就进入：
	// 内存耗尽来得突然，其代价（OOM killer）远高于一次拒绝。
	cpuEnterSamples = 2
	exitSamples     = 2
	// failOpenErrors clears pressure after this many consecutive failed
	// samples: a broken probe must not leave a node refusing work forever.
	// failOpenErrors 在连续这么多次采样失败后解除资源紧张：坏掉的探测不能让节点永远拒绝工作。
	failOpenErrors = 3
)

// Rejection reasons, the reason label of MetricRejectionsTotal.
const (
	ReasonPressureCPU    = "pressure_cpu"
	ReasonPressureMemory = "pressure_memory"
	ReasonQueueFull      = "queue_full"
	ReasonWaitTimeout    = "wait_timeout"
)

// Sampler takes one live usage sample. hostresources.UsageReader is the
// production implementation.
// Sampler 采集一次实时用量样本。生产实现是 hostresources.UsageReader。
type Sampler interface {
	Read() (hostresources.Usage, error)
}

// Config configures a Controller. Zero thresholds disable the pressure gate
// and zero maxima leave a class unlimited, so the zero Config only samples
// and reports.
//
// Config 配置一个 Controller。阈值为零即关闭资源闸门，上限为零即该分类不限，因此零值
// Config 只采样和上报。
type Config struct {
	// SampleInterval is the time between samples. Default 5s.
	SampleInterval time.Duration
	// CPUHighPercent enters CPU pressure at or above this utilization. 0 disables.
	CPUHighPercent float64
	// MemoryLowPercent enters memory pressure at or below this share of
	// available memory. 0 disables.
	MemoryLowPercent float64
	// HysteresisPercent is how far a value must move back past its threshold
	// before pressure clears. Default 5.
	HysteresisPercent float64
	// CLIMax and GeneralMax bound concurrent admitted requests per class. 0 is unlimited.
	CLIMax, GeneralMax int
	// QueueMaxWait is how long a request may wait for a full class
	// semaphore before it is refused. 0 refuses immediately.
	QueueMaxWait time.Duration
	// QueueMaxWaiters bounds how many requests may wait per class. Default 16.
	QueueMaxWaiters int
	Sampler         Sampler
	Clock           runtime.Clock
	Metrics         runtime.Metrics
	Logger          *slog.Logger
}

// Controller is the node-wide admission gate. It is safe for concurrent use.
// Controller 是整机准入闸门，可并发使用。
type Controller struct {
	cfg     Config
	clock   runtime.Clock
	logger  *slog.Logger
	metrics recorder
	classes map[string]*class

	mu          sync.Mutex
	usage       hostresources.Usage
	sampledAt   time.Time
	cpuPressure bool
	memPressure bool
	cpuEntered  time.Time
	memEntered  time.Time
	cpuHigh     int // consecutive samples at or above the CPU threshold
	cpuLow      int // consecutive samples below the CPU exit mark
	memLow      int // consecutive samples above the memory exit mark
	sampleErrs  int

	// pressure mirrors cpuPressure || memPressure for the lock-free hot path.
	pressure atomic.Bool
	reasons  atomic.Value // []nodeload.Reason
}

type class struct {
	name    string
	sem     *semaphore.Weighted
	waiting atomic.Int64
	active  atomic.Int64
}

// New returns a Controller. Call Run to start sampling.
// New 返回一个 Controller。调用 Run 开始采样。
func New(cfg Config) *Controller {
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = DefaultSampleInterval
	}
	if cfg.HysteresisPercent <= 0 {
		cfg.HysteresisPercent = DefaultHysteresisPercent
	}
	if cfg.QueueMaxWaiters <= 0 {
		cfg.QueueMaxWaiters = DefaultQueueMaxWaiters
	}
	if cfg.Sampler == nil {
		cfg.Sampler = hostresources.NewUsageReader()
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	c := &Controller{
		cfg:     cfg,
		clock:   cfg.Clock,
		logger:  cfg.Logger,
		metrics: newRecorder(cfg.Metrics),
		classes: map[string]*class{
			ClassCLI:     newClass(ClassCLI, cfg.CLIMax),
			ClassGeneral: newClass(ClassGeneral, cfg.GeneralMax),
		},
	}
	c.reasons.Store([]nodeload.Reason(nil))
	c.metrics.pressure(false)
	return c
}

func newClass(name string, maxActive int) *class {
	cl := &class{name: name}
	if maxActive > 0 {
		cl.sem = semaphore.NewWeighted(int64(maxActive))
	}
	return cl
}

// Run samples immediately and then every SampleInterval until ctx ends.
// Run 立即采样一次，之后每隔 SampleInterval 采样，直到 ctx 结束。
func (c *Controller) Run(ctx context.Context) {
	for {
		c.Sample()
		timer, stop := c.clock.NewTimer(c.cfg.SampleInterval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-timer:
		}
	}
}

// Sample takes one sample and updates the verdict. Run calls it; tests may
// call it directly.
// Sample 采集一次样本并更新判断。由 Run 调用；测试可直接调用。
func (c *Controller) Sample() {
	usage, err := c.cfg.Sampler.Read()
	now := c.clock.Now()

	c.mu.Lock()
	before := c.cpuPressure || c.memPressure
	if err != nil {
		c.sampleErrs++
		c.metrics.sampleError()
		if c.sampleErrs >= failOpenErrors && before {
			c.cpuPressure, c.memPressure = false, false
			c.cpuHigh, c.cpuLow, c.memLow = 0, 0, 0
		}
	} else {
		c.sampleErrs = 0
		c.usage, c.sampledAt = usage, now
		c.updateCPU(usage, now)
		c.updateMemory(usage, now)
		c.metrics.usage(usage)
	}
	after := c.cpuPressure || c.memPressure
	var reasons []nodeload.Reason
	if c.cpuPressure {
		reasons = append(reasons, nodeload.ReasonCPU)
	}
	if c.memPressure {
		reasons = append(reasons, nodeload.ReasonMemory)
	}
	c.mu.Unlock()

	if err != nil && c.sampleErrs == 1 {
		c.logger.Warn("host usage sample failed", slog.String("error", err.Error()))
	}
	c.reasons.Store(reasons)
	c.pressure.Store(after)
	c.metrics.pressure(after)
	if before != after {
		c.logger.Info("admission pressure changed", slog.Bool("pressure", after), slog.Any("reasons", reasons))
	}
}

// updateCPU applies the CPU thresholds. c.mu is held.
func (c *Controller) updateCPU(u hostresources.Usage, now time.Time) {
	high := c.cfg.CPUHighPercent
	if high <= 0 {
		c.cpuPressure = false
		return
	}
	if u.CPUUtilization == nil {
		return // unknown contributes nothing either way
	}
	pct := *u.CPUUtilization * 100
	switch {
	case pct >= high:
		c.cpuHigh++
		c.cpuLow = 0
	case pct <= high-c.cfg.HysteresisPercent:
		c.cpuLow++
		c.cpuHigh = 0
	default:
		c.cpuHigh, c.cpuLow = 0, 0
	}
	if !c.cpuPressure && c.cpuHigh >= cpuEnterSamples {
		c.cpuPressure, c.cpuEntered = true, now
	} else if c.cpuPressure && c.cpuLow >= exitSamples && now.Sub(c.cpuEntered) >= minPressureHold {
		c.cpuPressure = false
	}
}

// updateMemory applies the memory thresholds. c.mu is held.
func (c *Controller) updateMemory(u hostresources.Usage, now time.Time) {
	low := c.cfg.MemoryLowPercent
	if low <= 0 {
		c.memPressure = false
		return
	}
	if u.MemoryAvailableBytes == nil || u.MemoryTotalBytes == nil || *u.MemoryTotalBytes <= 0 {
		return
	}
	pct := float64(*u.MemoryAvailableBytes) / float64(*u.MemoryTotalBytes) * 100
	switch {
	case pct <= low:
		c.memLow = 0
		if !c.memPressure {
			c.memPressure, c.memEntered = true, now
		}
	case pct >= low+c.cfg.HysteresisPercent:
		c.memLow++
	default:
		c.memLow = 0
	}
	if c.memPressure && c.memLow >= exitSamples && now.Sub(c.memEntered) >= minPressureHold {
		c.memPressure = false
	}
}

// CheckPressure refuses new heavy work while the node is under pressure. It
// never blocks.
// CheckPressure 在节点资源紧张时拒绝新的重任务。从不阻塞。
func (c *Controller) CheckPressure(className string) error {
	if !c.pressure.Load() {
		return nil
	}
	reason := ReasonPressureCPU
	if reasons, _ := c.reasons.Load().([]nodeload.Reason); len(reasons) > 0 && reasons[0] == nodeload.ReasonMemory {
		reason = ReasonPressureMemory
	}
	c.metrics.rejection(className, reason)
	return busyError("node is under resource pressure (" + reason + ")")
}

// Acquire reserves one slot of the class semaphore, waiting at most
// QueueMaxWait (and never past ctx's deadline) when it is full. The returned
// release must be called exactly once; extra calls are no-ops. An unknown or
// unlimited class admits immediately.
//
// Acquire 占用分类信号量的一个位置；已满时最多等待 QueueMaxWait（且绝不超过 ctx 的期限）。
// 返回的 release 必须恰好调用一次，多余调用无效果。未知或不限的分类立即准入。
func (c *Controller) Acquire(ctx context.Context, className string) (func(), error) {
	cl, ok := c.classes[className]
	if !ok || cl.sem == nil {
		return func() {}, nil
	}
	if cl.sem.TryAcquire(1) {
		return c.granted(cl), nil
	}
	if c.cfg.QueueMaxWait <= 0 {
		c.metrics.rejection(cl.name, ReasonQueueFull)
		return nil, busyError("node " + cl.name + " concurrency limit reached")
	}
	if cl.waiting.Add(1) > int64(c.cfg.QueueMaxWaiters) {
		c.metrics.waiting(cl.name, int(cl.waiting.Add(-1)))
		c.metrics.rejection(cl.name, ReasonQueueFull)
		return nil, busyError("node " + cl.name + " admission queue is full")
	}
	c.metrics.waiting(cl.name, int(cl.waiting.Load()))
	start := c.clock.Now()
	wctx, cancel := context.WithTimeout(ctx, c.cfg.QueueMaxWait)
	err := cl.sem.Acquire(wctx, 1)
	cancel()
	c.metrics.waiting(cl.name, int(cl.waiting.Add(-1)))
	waited := c.clock.Now().Sub(start)

	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			c.metrics.wait(cl.name, "canceled", waited)
			return nil, ctx.Err()
		}
		c.metrics.wait(cl.name, "timeout", waited)
		c.metrics.rejection(cl.name, ReasonWaitTimeout)
		return nil, busyError("node " + cl.name + " concurrency limit reached after waiting")
	}
	c.metrics.wait(cl.name, "admitted", waited)
	release := c.granted(cl)
	// The machine may have come under pressure while this request waited.
	if err := c.CheckPressure(cl.name); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (c *Controller) granted(cl *class) func() {
	c.metrics.inflight(cl.name, int(cl.active.Add(1)))
	var once sync.Once
	return func() {
		once.Do(func() {
			c.metrics.inflight(cl.name, int(cl.active.Add(-1)))
			cl.sem.Release(1)
		})
	}
}

// Waiting returns the number of requests waiting on any class semaphore.
// Waiting 返回正在等待任一分类信号量的请求数。
func (c *Controller) Waiting() int {
	total := 0
	for _, cl := range c.classes {
		total += int(cl.waiting.Load())
	}
	return total
}

// Report returns the latest sample and verdict for the heartbeat. It reports
// false before the first successful sample.
// Report 返回供心跳使用的最近一次样本与判断。首次成功采样之前返回 false。
func (c *Controller) Report() (nodeload.Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sampledAt.IsZero() {
		return nodeload.Report{}, false
	}
	r := nodeload.Report{
		SampledAt:            c.sampledAt,
		CPUUtilization:       c.usage.CPUUtilization,
		CPUEstimated:         c.usage.CPUEstimated,
		LoadAverage1m:        c.usage.LoadAverage1m,
		MemoryAvailableBytes: c.usage.MemoryAvailableBytes,
		MemoryTotalBytes:     c.usage.MemoryTotalBytes,
		Pressure:             c.cpuPressure || c.memPressure,
		AdmissionWaiting:     c.Waiting(),
	}
	if c.cpuPressure {
		r.PressureReasons = append(r.PressureReasons, nodeload.ReasonCPU)
	}
	if c.memPressure {
		r.PressureReasons = append(r.PressureReasons, nodeload.ReasonMemory)
	}
	return r, true
}

// busyError has the shape of runtime.Limiter's refusal, so the Gateway moves
// the request to another node and its circuit breaker does not count it.
func busyError(message string) error {
	return &runtime.RuntimeError{
		Code:      runtime.ErrorBackpressure,
		Operation: "admission",
		Message:   message,
		Retryable: true,
		Cause:     runtime.ErrConcurrencyLimit,
	}
}
