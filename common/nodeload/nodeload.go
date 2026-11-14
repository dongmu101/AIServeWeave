// Package nodeload is the shape of one live host-load sample an Agent reports
// on its heartbeat, shared by the Agent that measures it, the tunnel codec
// that carries it and the Gateway that schedules on it. Unlike the static
// NodeResources sent once at Hello, this is volatile runtime state.
//
// Every measurement is optional. A platform that cannot measure a value
// leaves it nil, and a consumer must render nil as "not reported" rather than
// as zero: a missing CPU figure is not an idle CPU.
//
// nodeload 是 Agent 在心跳上报的一次实时主机负载采样的形状，由测量它的 Agent、
// 运送它的隧道编解码以及据此调度的 Gateway 共用。它不同于 Hello 时只发一次的静态
// NodeResources，是易变的运行时状态。
//
// 每一项测量都是可选的。无法测量某个值的平台把它留为 nil，使用方必须把 nil 渲染为
// 「未上报」而不是零：缺失的 CPU 数值不等于空闲的 CPU。
package nodeload

import "time"

// Reason names why the Agent is under pressure.
// Reason 说明 Agent 处于资源紧张状态的原因。
type Reason string

const (
	// ReasonCPU means CPU utilization crossed the configured high mark.
	// ReasonCPU 表示 CPU 使用率越过了配置的上限。
	ReasonCPU Reason = "cpu"
	// ReasonMemory means available memory fell below the configured low mark.
	// ReasonMemory 表示可用内存低于配置的下限。
	ReasonMemory Reason = "memory"
)

// Report is one host-load sample plus the Agent's own admission verdict.
// Report 是一次主机负载采样加上 Agent 自己的准入判断。
type Report struct {
	SampledAt time.Time
	// CPUUtilization is 0..1 over the last sample interval.
	// CPUUtilization 是上一个采样间隔内的使用率，范围 0..1。
	CPUUtilization *float64
	// CPUEstimated is true when CPUUtilization was derived from the load
	// average rather than measured CPU time (macOS without cgo).
	// CPUEstimated 为 true 表示 CPUUtilization 由负载均值推算，而不是测量的 CPU
	// 时间（无 cgo 的 macOS）。
	CPUEstimated         bool
	LoadAverage1m        *float64
	MemoryAvailableBytes *int64
	MemoryTotalBytes     *int64
	// Pressure means the Agent is refusing new heavy work right now.
	// Pressure 表示 Agent 此刻正在拒绝新的重任务。
	Pressure        bool
	PressureReasons []Reason
	// AdmissionWaiting counts requests waiting on the Agent's local semaphores.
	// AdmissionWaiting 统计正在等待 Agent 本地信号量的请求数。
	AdmissionWaiting int
}
