package hostresources

import (
	"errors"
	"os"
)

// readPlatform samples /proc for CPU time, memory and load, narrowing memory
// to a cgroup v2 limit when the Agent runs inside one (containers). A CPU
// quota on the cgroup is not applied: utilization stays host-wide.
//
// readPlatform 从 /proc 采集 CPU 时间、内存与负载；Agent 运行在 cgroup v2 中（容器）
// 时把内存收窄到其限额。cgroup 的 CPU 配额不做换算：使用率仍是整机口径。
func (r *UsageReader) readPlatform() (Usage, error) {
	var u Usage
	var errs []error

	if data, err := os.ReadFile("/proc/stat"); err != nil {
		errs = append(errs, err)
	} else if cur, err := parseProcStat(data); err != nil {
		errs = append(errs, err)
	} else {
		u.CPUUtilization = r.observeCPU(cur)
	}

	if data, err := os.ReadFile("/proc/meminfo"); err != nil {
		errs = append(errs, err)
	} else if avail, total, err := parseMemInfoUsage(data); err != nil {
		errs = append(errs, err)
	} else {
		if limit, lerr := os.ReadFile("/sys/fs/cgroup/memory.max"); lerr == nil {
			current, _ := os.ReadFile("/sys/fs/cgroup/memory.current")
			stat, _ := os.ReadFile("/sys/fs/cgroup/memory.stat")
			if a, t, ok := cgroupMemory(limit, current, stat, total); ok {
				avail, total = min(a, avail), t
			}
		}
		u.MemoryAvailableBytes, u.MemoryTotalBytes = &avail, &total
	}

	if data, err := os.ReadFile("/proc/loadavg"); err != nil {
		errs = append(errs, err)
	} else if load, err := parseLoadAvg(data); err != nil {
		errs = append(errs, err)
	} else {
		u.LoadAverage1m = &load
	}

	if u.CPUUtilization == nil && u.MemoryAvailableBytes == nil && u.LoadAverage1m == nil && len(errs) > 0 {
		return u, errors.Join(errs...)
	}
	return u, nil
}
