//go:build !windows

package localstate

import (
	"fmt"
	"os"
)

// CheckPrivate rejects links and permissions that expose private state.
// CheckPrivate 拒绝链接及会暴露私有状态的权限。
func CheckPrivate(path string, exact bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("private state is not a regular file")
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 || (exact && mode != 0o600) {
		return fmt.Errorf("private state permissions %#o are not permitted", mode)
	}
	return nil
}

func ensureDirectory(dir string) error        { return os.MkdirAll(dir, 0o700) }
func createTemp(dir string) (*os.File, error) { return os.CreateTemp(dir, ".aisw-state-*") }

// Replace atomically replaces a state file on supported Unix filesystems.
// Replace 在支持的 Unix 文件系统上原子替换状态文件。
func Replace(source, target string) error { return os.Rename(source, target) }
