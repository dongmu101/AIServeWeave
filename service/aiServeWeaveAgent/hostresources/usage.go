package hostresources

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
)

// ErrUsageUnsupported reports that this platform has no live usage probe.
// ErrUsageUnsupported 表示本平台没有实时用量探测。
var ErrUsageUnsupported = errors.New("hostresources: live usage not supported on this platform")

// Usage is one live host-load sample. Unlike Detect's static capacity
// snapshot, it is volatile and taken repeatedly. Every field is optional: a
// value the platform cannot measure stays nil and must not be read as zero.
//
// Usage 是一次实时主机负载采样。它不同于 Detect 的静态容量快照，是易变的、会被反复
// 采集。每个字段都是可选的：平台无法测量的值保持 nil，不能被读成零。
type Usage struct {
	// CPUUtilization is the busy share of all CPUs, 0..1, between this sample
	// and the previous one. The first Read on platforms that measure CPU time
	// has no previous sample and leaves it nil.
	//
	// CPUUtilization 是本次与上次采样之间全部 CPU 的忙碌占比，范围 0..1。在测量 CPU
	// 时间的平台上，第一次 Read 没有上一次采样，因此留为 nil。
	CPUUtilization *float64
	// CPUEstimated marks a CPUUtilization derived from the load average
	// rather than measured CPU time (macOS, which has no CPU-time counter
	// reachable without cgo).
	//
	// CPUEstimated 标记由负载均值推算、而不是测量 CPU 时间得出的 CPUUtilization
	// （macOS 在不使用 cgo 时拿不到 CPU 时间计数）。
	CPUEstimated         bool
	LoadAverage1m        *float64
	MemoryAvailableBytes *int64
	MemoryTotalBytes     *int64
}

// UsageReader takes successive Usage samples. It keeps the previous CPU-time
// counters so utilization is a rate over the interval between two Reads, not
// an average since boot. It is safe for concurrent use.
//
// UsageReader 采集连续的 Usage 样本。它保留上一次的 CPU 时间计数，使使用率是两次
// Read 之间的速率，而不是开机以来的平均值。可并发使用。
type UsageReader struct {
	mu       sync.Mutex
	prev     cpuTimes
	havePrev bool
}

// NewUsageReader returns a reader with no previous sample.
// NewUsageReader 返回一个没有上一次样本的读取器。
func NewUsageReader() *UsageReader { return &UsageReader{} }

// Read takes one sample. It returns whatever the platform could measure and
// an error only when nothing at all could be measured.
//
// Read 采集一次样本。它返回平台能测到的全部内容；只有什么都测不到时才返回错误。
func (r *UsageReader) Read() (Usage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readPlatform()
}

// cpuTimes is an aggregate CPU-time counter pair in arbitrary but consistent
// units (jiffies on Linux, 100ns ticks on Windows).
type cpuTimes struct {
	busy, total uint64
}

// cpuUtilization turns two counter readings into a 0..1 busy share, or false
// when the counters did not advance or went backwards (a counter reset).
func cpuUtilization(prev, cur cpuTimes) (float64, bool) {
	if cur.total <= prev.total || cur.busy < prev.busy {
		return 0, false
	}
	share := float64(cur.busy-prev.busy) / float64(cur.total-prev.total)
	return min(max(share, 0), 1), true
}

// observeCPU records cur and returns the utilization since the previous call.
func (r *UsageReader) observeCPU(cur cpuTimes) *float64 {
	defer func() { r.prev, r.havePrev = cur, true }()
	if !r.havePrev {
		return nil
	}
	if share, ok := cpuUtilization(r.prev, cur); ok {
		return &share
	}
	return nil
}

// estimatedCPU derives a 0..1 figure from the one-minute load average.
func estimatedCPU(load1 float64) float64 {
	cpus := float64(max(goruntime.NumCPU(), 1))
	return min(max(load1/cpus, 0), 1)
}

// parseProcStat reads the aggregate "cpu" line of /proc/stat. Busy is
// everything except idle and iowait across user..steal; guest time is
// already counted inside user and nice, so it is not added again.
//
// parseProcStat 读取 /proc/stat 的汇总 "cpu" 行。忙碌时间是 user..steal 中除 idle 与
// iowait 外的全部；guest 时间已经计入 user 与 nice，因此不再重复相加。
func parseProcStat(data []byte) (cpuTimes, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 5 {
			return cpuTimes{}, fmt.Errorf("hostresources: short cpu line in /proc/stat")
		}
		var values [8]uint64
		for i := 0; i < len(values) && i+1 < len(fields); i++ {
			v, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, fmt.Errorf("hostresources: parse /proc/stat: %w", err)
			}
			values[i] = v
		}
		var total uint64
		for _, v := range values {
			total += v
		}
		idle := values[3] + values[4]
		return cpuTimes{busy: total - idle, total: total}, nil
	}
	return cpuTimes{}, fmt.Errorf("hostresources: cpu line not found in /proc/stat")
}

// parseMemInfoUsage extracts available and total memory from /proc/meminfo,
// in bytes. MemAvailable is the kernel's own estimate (3.14+); older kernels
// fall back to MemFree + Buffers + Cached.
//
// parseMemInfoUsage 从 /proc/meminfo 提取可用与总内存（字节）。MemAvailable 是内核
// 自己的估算（3.14+）；更老的内核退回 MemFree + Buffers + Cached。
func parseMemInfoUsage(data []byte) (available, total int64, err error) {
	values := map[string]int64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		values[name] = kb * 1024
	}
	total, ok := values["MemTotal"]
	if !ok {
		return 0, 0, fmt.Errorf("hostresources: MemTotal not found in /proc/meminfo")
	}
	if avail, ok := values["MemAvailable"]; ok {
		return avail, total, nil
	}
	return values["MemFree"] + values["Buffers"] + values["Cached"], total, nil
}

// parseLoadAvg reads the one-minute figure from /proc/loadavg.
// parseLoadAvg 从 /proc/loadavg 读取一分钟负载。
func parseLoadAvg(data []byte) (float64, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("hostresources: empty /proc/loadavg")
	}
	return strconv.ParseFloat(fields[0], 64)
}

// cgroupMemory narrows host memory to a cgroup v2 limit. limitRaw is the
// content of memory.max ("max" means unlimited), currentRaw of
// memory.current, statRaw of memory.stat. Like the kernel's MemAvailable,
// reclaimable page cache counts as available: file pages other than shmem
// (which is swap-backed, not reclaimable) plus reclaimable slab. Counting
// only inactive_file would leave a container that mmaps model weights —
// active_file — looking permanently out of memory. It reports false when
// there is no numeric limit or the limit is not below the host total.
//
// cgroupMemory 把主机内存收窄到 cgroup v2 的限额。limitRaw 是 memory.max 的内容
// （"max" 表示不限），currentRaw 是 memory.current，statRaw 是 memory.stat。与内核的
// MemAvailable 一样，可回收的页缓存计为可用：除 shmem（由 swap 支撑、不可回收）外的
// file 页，加上可回收的 slab。只计 inactive_file 会让一个 mmap 了模型权重（active_file）
// 的容器看起来永远缺内存。没有数值限额或限额不低于主机总量时返回 false。
func cgroupMemory(limitRaw, currentRaw, statRaw []byte, hostTotal int64) (available, total int64, ok bool) {
	limit, err := strconv.ParseInt(strings.TrimSpace(string(limitRaw)), 10, 64)
	if err != nil || limit <= 0 || (hostTotal > 0 && limit >= hostTotal) {
		return 0, 0, false
	}
	current, err := strconv.ParseInt(strings.TrimSpace(string(currentRaw)), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	stat := map[string]int64{}
	sc := bufio.NewScanner(bytes.NewReader(statRaw))
	for sc.Scan() {
		if name, value, found := strings.Cut(sc.Text(), " "); found {
			if v, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				stat[name] = v
			}
		}
	}
	reclaimable, hasFile := stat["file"]
	if hasFile {
		reclaimable -= stat["shmem"]
	} else {
		// Kernels before 4.x lack the aggregate; inactive_file is the
		// conservative part of it.
		reclaimable = stat["inactive_file"]
	}
	reclaimable = max(reclaimable, 0) + max(stat["slab_reclaimable"], 0)
	used := max(current-reclaimable, 0)
	return min(max(limit-used, 0), limit), limit, true
}

// cgroupMemoryDir finds the cgroup v2 directory whose memory.max governs this
// process: the process's own cgroup (from /proc/self/cgroup's "0::" line)
// or the ancestor with the smallest numeric limit, since a parent's limit
// caps every child. Without a cgroup namespace the own path is nested under
// root (a systemd service with MemoryMax=, for example), which reading only
// the root would miss. It returns "" when no ancestor has a numeric limit.
// cgroup v1 is not supported; such a host is measured host-wide.
//
// cgroupMemoryDir 找出约束本进程的 cgroup v2 目录 memory.max：进程自己的 cgroup（取自
// /proc/self/cgroup 的 "0::" 行），或数值限额最小的祖先——父级的限额约束所有子级。没有
// cgroup namespace 时，自身路径嵌套在根下（例如设置了 MemoryMax= 的 systemd 服务），
// 只读根目录会漏掉它。没有任何祖先有数值限额时返回 ""。不支持 cgroup v1，此类主机按整机
// 口径测量。
func cgroupMemoryDir(root string, procSelfCgroup []byte, readFile func(string) ([]byte, error)) string {
	rel := ""
	sc := bufio.NewScanner(bytes.NewReader(procSelfCgroup))
	for sc.Scan() {
		if path, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			rel = path
			break
		}
	}
	best, bestLimit := "", int64(0)
	dir := filepath.Clean(root + "/" + rel)
	for {
		if raw, err := readFile(dir + "/memory.max"); err == nil {
			if limit, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && limit > 0 &&
				(best == "" || limit < bestLimit) {
				best, bestLimit = dir, limit
			}
		}
		if dir == root || len(dir) <= len(root) {
			return best
		}
		dir = filepath.Dir(dir)
	}
}

// parseDarwinLoadavg decodes sysctl vm.loadavg's struct loadavg: three
// uint32 fixed-point values followed by a C long scale, which is 8-byte
// aligned at offset 16 on the 64-bit platforms macOS still supports.
//
// parseDarwinLoadavg 解码 sysctl vm.loadavg 的 struct loadavg：三个 uint32 定点数，
// 后跟一个 C long 刻度；在 macOS 仍支持的 64 位平台上，它按 8 字节对齐位于偏移 16。
func parseDarwinLoadavg(raw []byte) (float64, error) {
	if len(raw) < 16 {
		return 0, fmt.Errorf("hostresources: short vm.loadavg (%d bytes)", len(raw))
	}
	load1 := binary.LittleEndian.Uint32(raw[0:4])
	var scale int64
	if len(raw) >= 24 {
		scale = int64(binary.LittleEndian.Uint64(raw[16:24]))
	} else {
		scale = int64(int32(binary.LittleEndian.Uint32(raw[12:16])))
	}
	if scale <= 0 {
		return 0, fmt.Errorf("hostresources: invalid vm.loadavg scale %d", scale)
	}
	return float64(load1) / float64(scale), nil
}

// darwinMemory converts hw.memsize and kern.memorystatus_level (the percent
// of memory the kernel considers available) into bytes.
//
// darwinMemory 把 hw.memsize 与 kern.memorystatus_level（内核视为可用的内存百分比）
// 换算为字节。
func darwinMemory(memsize uint64, levelPercent uint32) (available, total int64) {
	total = int64(memsize)
	level := min(levelPercent, 100)
	return total / 100 * int64(level), total
}
