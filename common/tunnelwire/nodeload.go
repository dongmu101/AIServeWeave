package tunnelwire

import (
	"math"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeload"
)

// maxLoadAverage bounds a reported load average. Real hosts stay far below
// it; the bound only keeps a corrupt or hostile figure from distorting
// scheduling or rendering.
//
// maxLoadAverage 限定上报的负载均值。真实主机远低于此值；这个上界只是防止损坏或恶意的
// 数值扭曲调度或展示。
const maxLoadAverage = 1 << 20

// maxPressureReasons bounds the reasons list; there are only two real ones.
// maxPressureReasons 限定原因列表长度；真实原因只有两个。
const maxPressureReasons = 4

// NodeLoadToProto encodes an Agent's live host-load sample for its heartbeat.
// NodeLoadToProto 把 Agent 的实时主机负载采样编码进心跳。
func NodeLoadToProto(r nodeload.Report) *tunnelv1.NodeLoad {
	pb := &tunnelv1.NodeLoad{
		SampledUnixMs:        unixMilliOrZero(r.SampledAt),
		CpuUtilization:       r.CPUUtilization,
		CpuEstimated:         r.CPUEstimated,
		LoadAverage_1M:       r.LoadAverage1m,
		MemoryAvailableBytes: r.MemoryAvailableBytes,
		MemoryTotalBytes:     r.MemoryTotalBytes,
		Pressure:             r.Pressure,
		AdmissionWaiting:     clampInt32(r.AdmissionWaiting),
	}
	for _, reason := range r.PressureReasons {
		switch reason {
		case nodeload.ReasonCPU:
			pb.PressureReasons = append(pb.PressureReasons, tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_CPU)
		case nodeload.ReasonMemory:
			pb.PressureReasons = append(pb.PressureReasons, tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_MEMORY)
		}
	}
	return pb
}

// NodeLoadFromProto restores a heartbeat's load sample, or reports false when
// the heartbeat carried none (an older Agent). Values arrive from an Agent
// that is authenticated but not trusted to be correct, so the decoder
// sanitizes rather than rejects: CPU is clamped to 0..1, a NaN or infinite
// figure is dropped, memory is never negative, available never exceeds total,
// and unknown reasons are discarded.
//
// NodeLoadFromProto 还原心跳中的负载采样；心跳未携带时（旧版 Agent）返回 false。
// 这些值来自已认证、但不被信任一定正确的 Agent，因此解码器做清洗而不是拒绝：CPU 收敛到
// 0..1，NaN 或无穷值被丢弃，内存从不为负，可用内存不超过总量，未知原因被丢弃。
func NodeLoadFromProto(pb *tunnelv1.NodeLoad) (nodeload.Report, bool) {
	if pb == nil {
		return nodeload.Report{}, false
	}
	r := nodeload.Report{
		SampledAt:        timeFromUnixMilli(pb.GetSampledUnixMs()),
		CPUEstimated:     pb.GetCpuEstimated(),
		Pressure:         pb.GetPressure(),
		AdmissionWaiting: max(int(pb.GetAdmissionWaiting()), 0),
	}
	if pb.CpuUtilization != nil {
		if v, ok := finite(pb.GetCpuUtilization()); ok {
			v = min(max(v, 0), 1)
			r.CPUUtilization = &v
		}
	}
	if pb.LoadAverage_1M != nil {
		if v, ok := finite(pb.GetLoadAverage_1M()); ok {
			v = min(max(v, 0), maxLoadAverage)
			r.LoadAverage1m = &v
		}
	}
	if pb.MemoryTotalBytes != nil {
		v := max(pb.GetMemoryTotalBytes(), 0)
		r.MemoryTotalBytes = &v
	}
	if pb.MemoryAvailableBytes != nil {
		v := max(pb.GetMemoryAvailableBytes(), 0)
		if r.MemoryTotalBytes != nil && *r.MemoryTotalBytes > 0 {
			v = min(v, *r.MemoryTotalBytes)
		}
		r.MemoryAvailableBytes = &v
	}
	seen := map[nodeload.Reason]bool{}
	for _, reason := range pb.GetPressureReasons() {
		if len(r.PressureReasons) >= maxPressureReasons {
			break
		}
		var decoded nodeload.Reason
		switch reason {
		case tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_CPU:
			decoded = nodeload.ReasonCPU
		case tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_MEMORY:
			decoded = nodeload.ReasonMemory
		default:
			continue
		}
		if !seen[decoded] {
			seen[decoded] = true
			r.PressureReasons = append(r.PressureReasons, decoded)
		}
	}
	return r, true
}

func finite(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func clampInt32(v int) int32 {
	return int32(min(max(v, 0), math.MaxInt32))
}
