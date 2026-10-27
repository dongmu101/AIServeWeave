//go:build !darwin && !linux

package geminicli

import "os/exec"

func configureProcess(*exec.Cmd) error { return errProcess }
