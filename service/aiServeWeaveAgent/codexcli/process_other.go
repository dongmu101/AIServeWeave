//go:build !darwin && !linux

package codexcli

import "os/exec"

func configureProcess(_ *exec.Cmd) error { return ErrProcess }
