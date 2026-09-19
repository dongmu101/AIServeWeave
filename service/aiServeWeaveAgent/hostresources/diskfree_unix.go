//go:build linux || darwin

package hostresources

import (
	"fmt"
	"syscall"
)

// DiskFreeBytes returns the free space available to an unprivileged process
// on the filesystem holding path, using the standard library's
// syscall.Statfs — no external tool and no additional dependency, unlike the
// rest of this package's os/exec-based probes. It is a separate function
// from Detect (see the package doc's disk-space section), for
// modelpull's subtask 4 (docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md)
// to call directly before writing bytes to path's filesystem.
//
// DiskFreeBytes 用标准库 syscall.Statfs 返回 path 所在文件系统上非特权进程可
// 用的剩余空间——不涉及外部工具，也不新增依赖，这一点与本包其余基于
// os/exec 的探测不同。它是与 Detect 分开的独立函数（见本包文档的磁盘一
// 节），供 modelpull 子任务四
// （docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md）
// 在向 path 所在文件系统写入字节之前直接调用。
func DiskFreeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("hostresources: statfs %q: %w", path, err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
