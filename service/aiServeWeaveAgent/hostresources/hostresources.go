// Package hostresources detects this node's CPU, memory and GPU capacity for
// the tunnel handshake's NodeResources (STATUS.md's P2 resource collection).
// Detection goes through os/exec against platform tools already installed
// with the OS (Linux's /proc/meminfo and nvidia-smi, macOS's sysctl) rather
// than a library such as gopsutil or an NVML binding, so the Agent's
// dependency line stays exactly what AGENTS.md requires: "Agent 与 Registry
// 的直接依赖只有 gRPC、protobuf 与 coder/websocket".
//
// hostresources 探测本节点的 CPU、内存与 GPU 容量，供隧道握手的 NodeResources
// 使用（STATUS.md 的 P2 资源采集）。探测一律经由 os/exec 调用系统自带工具
// （Linux 的 /proc/meminfo 与 nvidia-smi、macOS 的 sysctl），而不是引入
// gopsutil 一类的库或 NVML binding，这样 Agent 的依赖线就仍是 AGENTS.md 要求的
// 「Agent 与 Registry 的直接依赖只有 gRPC、protobuf 与 coder/websocket」。
package hostresources

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// probeTimeout bounds every external command, so a hung or missing tool
// never delays the Agent's startup or its Hello handshake.
//
// probeTimeout 限定每一次外部命令的时长，确保挂起或缺失的工具不会拖慢 Agent 启动
// 或它的 Hello 握手。
const probeTimeout = 3 * time.Second

// Detect returns a best-effort snapshot of this node's hardware. A field
// that could not be determined is left at its zero value rather than
// failing the call: this is capacity information for the scheduler's future
// admission check, not a health signal, so a partial answer is still useful
// and a missing tool must never block startup. CPU core count and OS/Arch
// always succeed (they come from the Go runtime, not an external process).
//
// Detect 返回本节点硬件的尽力而为快照。无法确定的字段保留零值而不是让调用失败：
// 这是供调度器未来准入检查使用的容量信息，不是健康信号，因此部分结果依然有用，
// 缺失某个工具也绝不能阻塞启动。CPU 核数与 OS/Arch 恒定成功（它们来自 Go 运行时
// 本身，不涉及外部进程）。
func Detect(ctx context.Context, logger *slog.Logger) *tunnelv1.NodeResources {
	if logger == nil {
		logger = slog.Default()
	}
	res := &tunnelv1.NodeResources{
		CpuCores: int32(goruntime.NumCPU()),
		Os:       goruntime.GOOS,
		Arch:     goruntime.GOARCH,
	}
	if mem, err := memoryTotalBytes(ctx); err != nil {
		logger.Debug("host resource probe: memory total unavailable", slog.String("error", err.Error()))
	} else {
		res.MemoryBytes = mem
	}
	if count, gpuBytes, err := gpuInventory(ctx); err != nil {
		logger.Debug("host resource probe: gpu inventory unavailable", slog.String("error", err.Error()))
	} else {
		res.GpuCount = count
		res.GpuMemoryBytes = gpuBytes
	}
	return res
}

// memoryTotalBytes returns total physical memory. Linux reads /proc/meminfo
// directly, no external process needed; other platforms shell out to the
// OS's own inventory tool.
func memoryTotalBytes(ctx context.Context) (int64, error) {
	switch goruntime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, fmt.Errorf("hostresources: read /proc/meminfo: %w", err)
		}
		return parseProcMemInfo(data)
	case "darwin":
		out, err := runCommand(ctx, "sysctl", "-n", "hw.memsize")
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	default:
		return 0, fmt.Errorf("hostresources: memory detection not supported on %s", goruntime.GOOS)
	}
}

// parseProcMemInfo extracts MemTotal from /proc/meminfo's content, which is
// reported in kB regardless of the kernel's actual page size.
func parseProcMemInfo(data []byte) (int64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("hostresources: unexpected MemTotal line %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("hostresources: parse MemTotal: %w", err)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("hostresources: MemTotal not found in /proc/meminfo")
}

// gpuInventory returns the GPU count and total VRAM summed across every GPU
// nvidia-smi reports. Only Linux+NVIDIA has a reliable, always-installed
// inventory command today; the design doc's evaluation of gopsutil/NVML
// found both to be either incomplete for GPUs or a dependency the Agent's
// red line does not allow. Every other platform (including macOS, which has
// no generic VRAM query for Apple Silicon) returns an error and Detect
// leaves the GPU fields at zero.
func gpuInventory(ctx context.Context) (count int32, totalBytes int64, err error) {
	if goruntime.GOOS != "linux" {
		return 0, 0, fmt.Errorf("hostresources: gpu detection not supported on %s", goruntime.GOOS)
	}
	out, err := runCommand(ctx, "nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return 0, 0, err
	}
	return parseNvidiaSMIMemoryTotal(out)
}

// parseNvidiaSMIMemoryTotal parses nvidia-smi's
// "--query-gpu=memory.total --format=csv,noheader,nounits" output: one
// number of MiB per line, one line per GPU.
func parseNvidiaSMIMemoryTotal(out string) (count int32, totalBytes int64, err error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		mib, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("hostresources: unexpected nvidia-smi output %q: %w", line, err)
		}
		totalBytes += mib * 1024 * 1024
		count++
	}
	if count == 0 {
		return 0, 0, fmt.Errorf("hostresources: nvidia-smi reported no GPUs")
	}
	return count, totalBytes, nil
}

// runCommand runs a fixed command line (never built from external input, so
// there is nothing to inject) and returns its trimmed stdout.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("hostresources: run %s: %w", name, err)
	}
	return stdout.String(), nil
}
