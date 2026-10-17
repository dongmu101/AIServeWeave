//go:build darwin || linux

package grokcli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcess puts the CLI in its own process group so cancellation also
// kills any helper it spawned.
// configureProcess 把 CLI 放入独立进程组，取消时一并终止它派生的辅助进程。
func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
