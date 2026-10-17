//go:build !darwin && !linux

package grokcli

import "os/exec"

// configureProcess refuses platforms where the process group cannot be reaped.
// configureProcess 在无法回收整个进程组的平台上拒绝启动。
func configureProcess(_ *exec.Cmd) error { return ErrProcess }
