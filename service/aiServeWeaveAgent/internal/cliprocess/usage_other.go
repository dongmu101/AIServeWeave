//go:build !linux && !darwin

package cliprocess

import (
	"os"
	"time"
)

// Usage reports an exited process's CPU time; peak memory is not available
// on this platform and stays zero.
// Usage 报告已退出进程的 CPU 时间；本平台无法取得峰值内存，保持为零。
func Usage(state *os.ProcessState) (cpu time.Duration, maxRSS int64) {
	if state == nil {
		return 0, 0
	}
	return state.UserTime() + state.SystemTime(), 0
}
