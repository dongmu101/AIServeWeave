//go:build !linux && !darwin && !windows

package cliprocess

import (
	"errors"
	"os/exec"
)

// Configure refuses systems without process-tree reclamation support.
// Configure 拒绝不支持进程树回收的系统。
func Configure(*exec.Cmd) error { return errors.New("CLI process platform unsupported") }

// Start refuses systems without process-tree reclamation support.
// Start 拒绝不支持进程树回收的系统。
func Start(*exec.Cmd) error { return errors.New("CLI process platform unsupported") }
