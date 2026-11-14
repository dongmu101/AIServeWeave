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
	cpuMissing  int // consecutive successful samples without a CPU figure
	memMissing  int // consecutive successful samples without a memory figure
	sampleErrs  int

	// pressure mirrors cpuPressure || memPressure for the lock-free hot path,
	// and pressureReason is the rejection label it implies. Both are written
	// under mu, together, so a reader never sees one without the other.
	//
	// pressure 镜像 cpuPressure || memPressure 供无锁热路径读取，pressureReason 是它
	// 对应的拒绝标签。二者在 mu 下一起写入，读者不会只看到其中一个。
	pressure       atomic.Bool
	pressureReason atomic.Value // string
}

// class is one admission semaphore. waiting and active are kept under mu so
// a gauge is always set to the value its own update produced; atomics alone
// would let two concurrent Sets land out of order and leave a stale reading.
//
// class 是一个准入信号量。waiting 与 active 在 mu 下维护，使 gauge 总是写入它自己那次
// 更新产生的值；只用原子量的话，两次并发的 Set 可能乱序落地，留下陈旧读数。
type class struct {
	name    string
	sem     *semaphore.Weighted
	mu      sync.Mutex
	waiting int
	active  int
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
	c.pressureReason.Store(ReasonPressureCPU)
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
// call it directly. It is safe for concurrent use.
// Sample 采集一次样本并更新判断。由 Run 调用；测试可直接调用。可并发调用。
func (c *Controller) Sample() {
	usage, err := c.cfg.Sampler.Read()
	now := c.clock.Now()

	c.mu.Lock()
	before := c.cpuPressure || c.memPressure
	if err != nil {
		c.sampleErrs++
		c.metrics.sampleError()
		if c.sampleErrs >= failOpenErrors {
			c.clearCPU()
			c.clearMemory()
		}
	} else {
		c.sampleErrs = 0
		c.usage, c.sampledAt = usage, now
		c.updateCPU(usage, now)
		c.updateMemory(usage, now)
		c.metrics.usage(usage)
	}
	after := c.cpuPressure || c.memPressure
	reason := ReasonPressureCPU
	if c.memPressure {
		// Memory is the more urgent of the two when both hold.
		reason = ReasonPressureMemory
	}
	c.pressureReason.Store(reason)
	c.pressure.Store(after)
	reasons := c.reasonsLocked()
	errs := c.sampleErrs
	c.mu.Unlock()

	if err != nil && errs == 1 {
		c.logger.Warn("host usage sample failed", slog.String("error", err.Error()))
	}
	c.metrics.pressure(after)
	if before != after {
		c.logger.Info("admission pressure changed", slog.Bool("pressure", after), slog.Any("reasons", reasons))
	}
}

func (c *Controller) clearCPU()    { c.cpuPressure, c.cpuHigh, c.cpuLow = false, 0, 0 }
func (c *Controller) clearMemory() { c.memPressure, c.memLow = false, 0 }

// reasonsLocked lists the active pressure reasons. c.mu is held.
func (c *Controller) reasonsLocked() []nodeload.Reason {
	var reasons []nodeload.Reason
	if c.cpuPressure {
		reasons = append(reasons, nodeload.ReasonCPU)
	}
	if c.memPressure {
		reasons = append(reasons, nodeload.ReasonMemory)
	}
	return reasons
}

// cpuExit is the utilization at or below which CPU pressure may clear. It is
// high minus the hysteresis, but never below half of high, so the exit stays
// reachable however the two were configured.
//
// cpuExit 是 CPU 压力可以解除的使用率上限：high 减去迟滞，但不低于 high 的一半，
// 使无论两者如何配置，退出线都可达。
func (c *Controller) cpuExit() float64 {
	high := c.cfg.CPUHighPercent
	return max(high-c.cfg.HysteresisPercent, high/2)
}

// memExit is the available share at or above which memory pressure may
// clear: low plus the hysteresis, but never above halfway to 100%.
//
// memExit 是内存压力可以解除的可用比例下限：low 加上迟滞，但不高于 low 到 100% 的中点。
func (c *Controller) memExit() float64 {
	low := c.cfg.MemoryLowPercent
	return min(low+c.cfg.HysteresisPercent, (low+100)/2)
}

// updateCPU applies the CPU thresholds. c.mu is held.
func (c *Controller) updateCPU(u hostresources.Usage, now time.Time) {
	high := c.cfg.CPUHighPercent
	if high <= 0 {
		c.clearCPU()
		return
	}
	if u.CPUUtilization == nil {
		// Unknown contributes nothing either way, but a figure that stays
		// missing must not pin the node in pressure: fail open, as for a
		// failing probe.
		// 未知值不影响判断；但持续缺失的数值不能把节点钉在压力态：与探测失败一样 fail-open。
		if c.cpuMissing++; c.cpuMissing >= failOpenErrors {
			c.clearCPU()
		}
		return
	}
	c.cpuMissing = 0
	pct := *u.CPUUtilization * 100
	switch {
	case pct >= high:
		c.cpuHigh++
		c.cpuLow = 0
	case pct <= c.cpuExit():
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
		c.clearMemory()
		return
	}
	if u.MemoryAvailableBytes == nil || u.MemoryTotalBytes == nil || *u.MemoryTotalBytes <= 0 {
		if c.memMissing++; c.memMissing >= failOpenErrors {
			c.clearMemory()
		}
		return
	}
	c.memMissing = 0
	pct := float64(*u.MemoryAvailableBytes) / float64(*u.MemoryTotalBytes) * 100
	switch {
	case pct <= low:
		c.memLow = 0
		if !c.memPressure {
			c.memPressure, c.memEntered = true, now
		}
	case pct >= c.memExit():
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
	reason, _ := c.pressureReason.Load().(string)
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
	if !c.addWaiter(cl) {
		c.metrics.rejection(cl.name, ReasonQueueFull)
		return nil, busyError("node " + cl.name + " admission queue is full")
	}
	start := c.clock.Now()
	wctx, cancel := context.WithTimeout(ctx, c.cfg.QueueMaxWait)
	err := cl.sem.Acquire(wctx, 1)
	cancel()
	c.removeWaiter(cl)
	waited := c.clock.Now().Sub(start)

	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			// The request itself ended: the caller left, or its own deadline
			// passed. Neither is this node being busy, so it is not answered
			// as a retryable backpressure that would send an expired
			// request on to another node.
			// 请求本身已经结束：调用方离开，或其自身期限已过。这都不是本节点繁忙，因此不以
			// 可重试背压作答——那会把一个已过期的请求再送到另一个节点。
			c.metrics.wait(cl.name, "canceled", waited)
			if errors.Is(cerr, context.DeadlineExceeded) {
				return nil, &runtime.RuntimeError{Code: runtime.ErrorTimeout, Operation: "admission",
					Message: "request deadline passed while waiting for admission", Cause: cerr}
			}
			return nil, cerr
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

// addWaiter registers one waiter unless QueueMaxWaiters already wait.
func (c *Controller) addWaiter(cl *class) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.waiting >= c.cfg.QueueMaxWaiters {
		return false
	}
	cl.waiting++
	c.metrics.waiting(cl.name, cl.waiting)
	return true
}

func (c *Controller) removeWaiter(cl *class) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.waiting--
	c.metrics.waiting(cl.name, cl.waiting)
}

func (c *Controller) granted(cl *class) func() {
	cl.mu.Lock()
	cl.active++
	c.metrics.inflight(cl.name, cl.active)
	cl.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			cl.mu.Lock()
			cl.active--
			c.metrics.inflight(cl.name, cl.active)
			cl.mu.Unlock()
			cl.sem.Release(1)
		})
	}
}

// Waiting returns the number of requests waiting on any class semaphore.
// Waiting 返回正在等待任一分类信号量的请求数。
func (c *Controller) Waiting() int {
	total := 0
	for _, cl := range c.classes {
		cl.mu.Lock()
		total += cl.waiting
		cl.mu.Unlock()
	}
	return total
}

// Report returns the latest sample and verdict for the heartbeat. It reports
// false before the first successful sample, and again once sampling has
// failed failOpenErrors times in a row: a stale figure sent as if it were
// live is worse than "not reported".
//
// Report 返回供心跳使用的最近一次样本与判断。首次成功采样之前返回 false；采样连续失败
// failOpenErrors 次之后也返回 false：把陈旧数值当作实时数据发送，比「未上报」更糟。
func (c *Controller) Report() (nodeload.Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sampledAt.IsZero() || c.sampleErrs >= failOpenErrors {
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
		PressureReasons:      c.reasonsLocked(),
		AdmissionWaiting:     c.Waiting(),
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
