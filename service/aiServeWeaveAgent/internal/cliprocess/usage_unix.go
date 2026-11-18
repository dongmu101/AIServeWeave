//go:build linux || darwin

package cliprocess

import (
	"os"
	goruntime "runtime"
	"syscall"
	"time"
)

// Usage reports an exited process's CPU time and peak resident memory in
// bytes, both zero when state is nil. They cover the process Wait reaped,
// not descendants that outlived it.
// Usage 报告已退出进程的 CPU 时间与峰值常驻内存（字节）；state 为 nil 时均为零。它们只
// 统计 Wait 回收的进程，不包括比它活得更久的后代。
func Usage(state *os.ProcessState) (cpu time.Duration, maxRSS int64) {
	if state == nil {
		return 0, 0
	}
	cpu = state.UserTime() + state.SystemTime()
	if usage, ok := state.SysUsage().(*syscall.Rusage); ok && usage != nil {
		maxRSS = int64(usage.Maxrss)
		if goruntime.GOOS == "linux" {
			maxRSS *= 1024 // Linux reports kilobytes, macOS bytes / Linux 以 KB 报告，macOS 以字节报告
		}
	}
	return cpu, maxRSS
}
