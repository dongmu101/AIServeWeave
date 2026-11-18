// Package cliphase times the phases of one local CLI round — process start,
// CLI start-up, the upstream request, the first event, teardown — so the
// Agent's share of time to first token can be split into the parts a warm
// process, a shared connection or a smaller history would each save. It
// records timings and process resources only, never request content.
//
// Package cliphase 为一次本机 CLI 回合的各阶段计时——进程启动、CLI 初始化、上游请求、
// 首个事件、收尾——把 Agent 在首 token 延迟中所占的部分拆成预热进程、共享连接或更小的
// 历史各自能省下的几段。它只记录耗时与进程资源，从不记录请求内容。
package cliphase

import (
	"os"
	"strconv"
	"sync"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// Phases, each reported as the time from the round's start to reaching it.
// Not every runtime reaches every phase: the Claude runtime has no thread,
// and Codex hides its upstream connection inside app-server.
//
// 各阶段都以回合开始到达到该阶段的时间报告。并非每个运行时都会经过所有阶段：Claude 运行时
// 没有线程，Codex 的上游连接藏在 app-server 内部。
const (
	// ProcessStarted is the CLI process running.
	// ProcessStarted 表示 CLI 进程已运行。
	ProcessStarted = "process_started"
	// CLIReady is the CLI's first output (Claude) or initialize reply (Codex).
	// CLIReady 表示 CLI 的首个输出（Claude）或 initialize 应答（Codex）。
	CLIReady = "cli_ready"
	// LoginChecked is Codex's account/read reply.
	// LoginChecked 表示 Codex 的 account/read 应答。
	LoginChecked = "login_checked"
	// ThreadStarted is Codex's thread/start reply.
	// ThreadStarted 表示 Codex 的 thread/start 应答。
	ThreadStarted = "thread_started"
	// HistoryInjected is Codex's thread/inject_items reply.
	// HistoryInjected 表示 Codex 的 thread/inject_items 应答。
	HistoryInjected = "history_injected"
	// TurnStarted is Codex's turn/start reply.
	// TurnStarted 表示 Codex 的 turn/start 应答。
	TurnStarted = "turn_started"
	// RequestSent is the Claude CLI's model request reaching the Agent's proxy.
	// RequestSent 表示 Claude CLI 的模型请求到达 Agent 的代理。
	RequestSent = "request_sent"
	// UpstreamHeaders is the upstream's response headers reaching that proxy.
	// UpstreamHeaders 表示上游的响应头到达该代理。
	UpstreamHeaders = "upstream_headers"
	// FirstEvent is the first model event handed to the Gateway.
	// FirstEvent 表示首个模型事件交给 Gateway。
	FirstEvent = "first_event"
	// Done is the round's last model event received from the CLI.
	// Done 表示从 CLI 收到回合的最后一个模型事件。
	Done = "done"
	// Exited is the CLI process reaped.
	// Exited 表示 CLI 进程已被回收。
	Exited = "exited"
)

// Metric names.
// 指标名。
const (
	// MetricPhaseSeconds observes the time from a round's start to each phase.
	// MetricPhaseSeconds 观测回合开始到各阶段的时间。
	MetricPhaseSeconds = "agent_cli_phase_seconds"
	// MetricUpstreamConnectSeconds observes how long the model request waited
	// for an upstream connection, reused or new.
	// MetricUpstreamConnectSeconds 观测模型请求等待上游连接的时间（复用或新建）。
	MetricUpstreamConnectSeconds = "agent_cli_upstream_connect_seconds"
	// MetricProcessCPUSeconds observes the CPU time one round's CLI process used.
	// MetricProcessCPUSeconds 观测一个回合的 CLI 进程所用的 CPU 时间。
	MetricProcessCPUSeconds = "agent_cli_process_cpu_seconds"
	// MetricProcessMaxRSSBytes observes one round's CLI process peak memory.
	// MetricProcessMaxRSSBytes 观测一个回合的 CLI 进程峰值内存。
	MetricProcessMaxRSSBytes = "agent_cli_process_max_rss_bytes"
)

// Descriptions is this package's metric catalogue.
// Descriptions 是本包的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricPhaseSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time from a local CLI round's start to each phase (process_started, cli_ready, request_sent, upstream_headers, first_event, done, exited, and Codex's login_checked, thread_started, history_injected, turn_started), by runtime_id, kind and phase.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricUpstreamConnectSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time the Claude CLI's model request waited for an upstream connection at the Agent's proxy, by runtime_id, kind and reused (true when an idle connection was reused).",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricProcessCPUSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "CPU time used by one round's local CLI process, by runtime_id and kind.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricProcessMaxRSSBytes: {
			Kind:    metrics.KindHistogram,
			Help:    "Peak resident memory of one round's local CLI process, by runtime_id and kind (zero where the platform does not report it).",
			Buckets: []float64{32 << 20, 64 << 20, 128 << 20, 256 << 20, 512 << 20, 1 << 30, 2 << 30},
		},
	}
}

// Timeline records one round's phases against the clock it was created
// with. A nil *Timeline accepts every call and records nothing, so code paths
// that are not timed need no checks. It is safe for concurrent use: the CLI
// reader, the upstream proxy and the Gateway writer each mark their own phase.
//
// Timeline 以创建时的时钟记录一个回合的各阶段。nil *Timeline 接受所有调用但什么都不记录，
// 不计时的代码路径因此无需检查。它可以并发使用：CLI 读取方、上游代理与 Gateway 写入方各自
// 标记自己的阶段。
type Timeline struct {
	clock runtime.Clock
	start time.Time

	mu       sync.Mutex
	order    []string
	marks    map[string]time.Duration
	connect  time.Duration
	reused   bool
	hasConn  bool
	cpu      time.Duration
	maxRSS   int64
	hasUsage bool
}

// New starts a timeline now.
// New 从当前时刻开始一条时间线。
func New(clock runtime.Clock) *Timeline {
	return &Timeline{clock: clock, start: clock.Now(), marks: make(map[string]time.Duration)}
}

// Mark records reaching phase now; only the first mark of a phase counts.
// Mark 记录当前到达 phase；同一阶段只计第一次标记。
func (t *Timeline) Mark(phase string) {
	if t == nil {
		return
	}
	elapsed := t.clock.Now().Sub(t.start)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.marks[phase]; ok {
		return
	}
	t.marks[phase] = elapsed
	t.order = append(t.order, phase)
}

// Connected records how long the model request waited for an upstream
// connection and whether it reused one; only the first call counts.
// Connected 记录模型请求等待上游连接的时间以及是否复用了连接；只计第一次调用。
func (t *Timeline) Connected(wait time.Duration, reused bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasConn {
		t.connect, t.reused, t.hasConn = wait, reused, true
	}
}

// Exited marks Exited and records the reaped process's CPU time and peak memory.
// Exited 标记 Exited，并记录已回收进程的 CPU 时间与峰值内存。
func (t *Timeline) Exited(state *os.ProcessState) {
	if t == nil {
		return
	}
	t.Mark(Exited)
	cpu, maxRSS := cliprocess.Usage(state)
	t.mu.Lock()
	defer t.mu.Unlock()
	if state != nil && !t.hasUsage {
		t.cpu, t.maxRSS, t.hasUsage = cpu, maxRSS, true
	}
}

// LogAttrs returns the recorded timings as slog key-value pairs in
// milliseconds: "<phase>_ms" in the order reached, then upstream_connect_ms
// with upstream_conn_reused, cli_cpu_ms and cli_max_rss_kb when recorded.
// LogAttrs 以毫秒返回已记录的耗时，作为 slog 键值对：按到达顺序的 "<阶段>_ms"，记录过时
// 再加上 upstream_connect_ms 与 upstream_conn_reused、cli_cpu_ms、cli_max_rss_kb。
func (t *Timeline) LogAttrs() []any {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	attrs := make([]any, 0, 2*len(t.order)+8)
	for _, phase := range t.order {
		attrs = append(attrs, phase+"_ms", t.marks[phase].Milliseconds())
	}
	if t.hasConn {
		attrs = append(attrs, "upstream_connect_ms", t.connect.Milliseconds(), "upstream_conn_reused", t.reused)
	}
	if t.hasUsage {
		attrs = append(attrs, "cli_cpu_ms", t.cpu.Milliseconds())
		if t.maxRSS > 0 {
			attrs = append(attrs, "cli_max_rss_kb", t.maxRSS>>10)
		}
	}
	return attrs
}

// Observe records the timeline into m; a nil m records nothing.
// Observe 把时间线记录进 m；m 为 nil 时什么都不记录。
func (t *Timeline) Observe(m runtime.Metrics, runtimeID string, kind runtime.Kind) {
	if t == nil || m == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	base := func() map[string]string { return map[string]string{"runtime_id": runtimeID, "kind": string(kind)} }
	for _, phase := range t.order {
		labels := base()
		labels["phase"] = phase
		m.Histogram(MetricPhaseSeconds, labels).Observe(t.marks[phase].Seconds())
	}
	if t.hasConn {
		labels := base()
		labels["reused"] = strconv.FormatBool(t.reused)
		m.Histogram(MetricUpstreamConnectSeconds, labels).Observe(t.connect.Seconds())
	}
	if t.hasUsage {
		m.Histogram(MetricProcessCPUSeconds, base()).Observe(t.cpu.Seconds())
		if t.maxRSS > 0 {
			m.Histogram(MetricProcessMaxRSSBytes, base()).Observe(float64(t.maxRSS))
		}
	}
}
