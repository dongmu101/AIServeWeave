//go:build !linux && !darwin

package hostresources

import "fmt"

// DiskFreeBytes is unsupported on this platform; see the linux/darwin
// implementation in diskfree_unix.go.
//
// DiskFreeBytes 在本平台不支持；实现见 diskfree_unix.go 的 linux/darwin 版本。
func DiskFreeBytes(path string) (int64, error) {
	return 0, fmt.Errorf("hostresources: disk free space detection not supported on this platform")
}
