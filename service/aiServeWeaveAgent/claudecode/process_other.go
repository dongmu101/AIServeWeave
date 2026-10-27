//go:build !darwin && !linux

package claudecode

import (
	"errors"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) error {
	return errors.New("the CLI probe supports macOS and Linux only")
}
