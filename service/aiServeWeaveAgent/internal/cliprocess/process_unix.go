//go:build linux || darwin

package cliprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Configure puts a command in its own cancellable process group.
// Configure 将命令放入独立且可取消的进程组。
func Configure(cmd *exec.Cmd) error {
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

// Start starts a configured process group.
// Start 启动已配置的进程组。
func Start(cmd *exec.Cmd) error { return cmd.Start() }
