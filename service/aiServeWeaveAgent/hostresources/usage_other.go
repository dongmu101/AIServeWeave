//go:build !linux && !darwin && !windows

package hostresources

// readPlatform has no probe on this platform.
// readPlatform 在本平台没有探测实现。
func (r *UsageReader) readPlatform() (Usage, error) {
	return Usage{}, ErrUsageUnsupported
}
