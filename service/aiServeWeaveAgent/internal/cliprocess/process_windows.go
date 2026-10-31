package cliprocess

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var npmEntry = regexp.MustCompile(`(?i)"%dp0%[\\/]([^"\r\n]+\.(?:js|cjs|mjs))"`)

// Configure resolves supported npm shims without a command interpreter.
// Configure 不通过命令解释器解析受支持的 npm 包装入口。
func Configure(cmd *exec.Cmd) error {
	ext := strings.ToLower(filepath.Ext(cmd.Path))
	if ext == ".cmd" {
		f, err := os.Open(cmd.Path)
		if err != nil {
			return errors.New("unsupported CLI command wrapper")
		}
		data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
		closeErr := f.Close()
		if err != nil || closeErr != nil || len(data) > 64*1024 {
			return errors.New("unsupported CLI command wrapper")
		}
		matches := npmEntry.FindAllSubmatch(data, -1)
		if len(matches) == 0 {
			return errors.New("unsupported CLI command wrapper")
		}
		var script string
		for _, m := range matches {
			rel := filepath.Clean(string(m[1]))
			if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || !strings.HasPrefix(strings.ToLower(rel), "node_modules"+string(os.PathSeparator)) {
				return errors.New("unsupported CLI command wrapper")
			}
			target := filepath.Join(filepath.Dir(cmd.Path), rel)
			if script != "" && script != target {
				return errors.New("ambiguous CLI command wrapper")
			}
			script = target
		}
		if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
			return errors.New("CLI entry missing")
		}
		node := filepath.Join(filepath.Dir(cmd.Path), "node.exe")
		if _, err := os.Stat(node); err != nil {
			node, err = exec.LookPath("node.exe")
			if err != nil {
				return errors.New("CLI command wrapper requires Node")
			}
		}
		cmd.Path = node
		cmd.Args = append([]string{node, script}, cmd.Args[1:]...)
	} else if ext != ".exe" {
		return errors.New("unsupported CLI executable format")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	return nil
}

// Start assigns a suspended command to a kill-on-close Job before executing it.
// Start 在执行命令前，将挂起的进程加入关闭即回收的 Job。
func Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		return errors.New("CLI command must be configured before start")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var once sync.Once
	closeJob := func() error {
		var err error
		once.Do(func() { err = windows.TerminateJobObject(job, 1); _ = windows.CloseHandle(job) })
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = closeJob()
		return err
	}
	cmd.Cancel = closeJob
	if err := cmd.Start(); err != nil {
		_ = closeJob()
		return err
	}
	fail := func(err error) error { _ = cmd.Process.Kill(); _ = closeJob(); _ = cmd.Wait(); return err }
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		return fail(err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(cmd.Process.Pid) {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fail(err)
		}
		_, err = windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if err != nil {
			return fail(err)
		}
		return nil
	}
	return fail(errors.New("CLI initial thread unavailable"))
}
