package hostresources

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// golang.org/x/sys/windows wraps neither GetSystemTimes nor
// GlobalMemoryStatusEx, so they are bound here through the system DLL loader,
// which only resolves from System32.
//
// golang.org/x/sys/windows 没有封装 GetSystemTimes 与 GlobalMemoryStatusEx，因此在这里
// 经系统 DLL 加载器绑定，它只从 System32 解析。
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func filetimeTicks(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// readPlatform samples system CPU times and physical memory. Windows has no
// load average, so LoadAverage1m stays nil. Kernel time includes idle time,
// so busy is kernel + user - idle.
//
// readPlatform 采集系统 CPU 时间与物理内存。Windows 没有负载均值，因此 LoadAverage1m
// 保持 nil。内核时间包含空闲时间，所以忙碌时间是 kernel + user - idle。
func (r *UsageReader) readPlatform() (Usage, error) {
	var u Usage
	var errs []error

	var idle, kernel, user windows.Filetime
	if ok, _, err := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		errs = append(errs, fmt.Errorf("hostresources: GetSystemTimes: %w", err))
	} else {
		total := filetimeTicks(kernel) + filetimeTicks(user)
		idleTicks := filetimeTicks(idle)
		busy := uint64(0)
		if total > idleTicks {
			busy = total - idleTicks
		}
		u.CPUUtilization = r.observeCPU(cpuTimes{busy: busy, total: total})
	}

	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); ok == 0 {
		errs = append(errs, fmt.Errorf("hostresources: GlobalMemoryStatusEx: %w", err))
	} else {
		avail, total := int64(status.AvailPhys), int64(status.TotalPhys)
		u.MemoryAvailableBytes, u.MemoryTotalBytes = &avail, &total
	}

	if u.CPUUtilization == nil && u.MemoryAvailableBytes == nil && len(errs) > 0 {
		return u, errors.Join(errs...)
	}
	return u, nil
}
