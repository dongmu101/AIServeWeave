package hostresources

import "golang.org/x/sys/windows"

// DiskFreeBytes returns the bytes available to this user on the target volume.
// DiskFreeBytes 返回目标卷上当前用户可用的剩余字节数。
func DiskFreeBytes(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(p, &available, nil, nil); err != nil {
		return 0, err
	}
	return int64(available), nil
}
