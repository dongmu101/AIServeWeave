package hostresources

import (
	"errors"

	"golang.org/x/sys/unix"
)

// readPlatform samples sysctl for load and memory. macOS exposes no CPU-time
// counter without cgo (host_statistics is a Mach call), so CPU utilization
// is estimated from the one-minute load average and marked as such.
//
// readPlatform 经 sysctl 采集负载与内存。macOS 不使用 cgo 时拿不到 CPU 时间计数
// （host_statistics 是 Mach 调用），因此 CPU 使用率由一分钟负载均值估算并如实标记。
func (r *UsageReader) readPlatform() (Usage, error) {
	var u Usage
	var errs []error

	if raw, err := unix.SysctlRaw("vm.loadavg"); err != nil {
		errs = append(errs, err)
	} else if load, err := parseDarwinLoadavg(raw); err != nil {
		errs = append(errs, err)
	} else {
		cpu := estimatedCPU(load)
		u.LoadAverage1m, u.CPUUtilization, u.CPUEstimated = &load, &cpu, true
	}

	if memsize, err := unix.SysctlUint64("hw.memsize"); err != nil {
		errs = append(errs, err)
	} else if level, err := unix.SysctlUint32("kern.memorystatus_level"); err != nil {
		errs = append(errs, err)
		total := int64(memsize)
		u.MemoryTotalBytes = &total
	} else {
		avail, total := darwinMemory(memsize, level)
		u.MemoryAvailableBytes, u.MemoryTotalBytes = &avail, &total
	}

	if u.CPUUtilization == nil && u.MemoryAvailableBytes == nil && len(errs) > 0 {
		return u, errors.Join(errs...)
	}
	return u, nil
}
