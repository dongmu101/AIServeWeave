package cliprocess_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
	"golang.org/x/sys/windows"
)

func TestCancellationReapsWindowsDescendants(t *testing.T) {
	switch os.Getenv("AISW_TREE_HELPER") {
	case "child":
		<-time.NewTimer(10 * time.Minute).C
		return
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestCancellationReapsWindowsDescendants$")
		child.Env = append(os.Environ(), "AISW_TREE_HELPER=child")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		fmt.Println(child.Process.Pid)
		_ = child.Wait()
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCancellationReapsWindowsDescendants$")
	cmd.Env = append(os.Environ(), "AISW_TREE_HELPER=parent")
	if err := cliprocess.Configure(cmd); err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cliprocess.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer cmd.Cancel()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	cancel()
	_ = cmd.Wait()
	state, err := windows.WaitForSingleObject(handle, 2000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant wait=(%d,%v), want terminated", state, err)
	}
}

func TestNpmShimResolvesWithoutShell(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "node_modules", "example", "main.js")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("// fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(dir, "node.exe")
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "cli.cmd")
	if err := os.WriteFile(shim, []byte(`@echo off
"%_prog%" "%dp0%\node_modules\example\main.js" %*
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), shim, "literal & argument")
	if err := cliprocess.Configure(cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != node || len(cmd.Args) != 3 || cmd.Args[1] != script || cmd.Args[2] != "literal & argument" {
		t.Fatalf("resolved command=(%s,%q), want Node and literal arguments", cmd.Path, cmd.Args)
	}
}

func TestWindowsEnvironmentNamesAreCaseInsensitive(t *testing.T) {
	if !cliprocess.AllowedKey("SystemRoot", nil) || !cliprocess.AllowedKey("Path", map[string]bool{"PATH": true}) || !cliprocess.KeyEqual("home", "HOME") {
		t.Fatal("required environment name was not recognized")
	}
	if cliprocess.AllowedKey("UNDECLARED_SECRET", nil) {
		t.Fatal("unlisted environment variable allowed")
	}
}

func TestOwnerCrashReapsNestedJobDescendants(t *testing.T) {
	switch os.Getenv("AISW_CRASH_HELPER") {
	case "child":
		<-time.NewTimer(10 * time.Minute).C
		return
	case "worker":
		child := exec.Command(os.Args[0], "-test.run=^TestOwnerCrashReapsNestedJobDescendants$")
		child.Env = append(os.Environ(), "AISW_CRASH_HELPER=child")
		if child.Start() != nil {
			os.Exit(3)
		}
		fmt.Println(child.Process.Pid)
		_ = child.Wait()
		return
	case "owner":
		// An outer job without kill-on-close tests nesting without masking inner cleanup.
		// 外层 Job 不启用关闭回收，既验证嵌套，也不掩盖内层清理缺陷。
		outer, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			os.Exit(4)
		}
		if windows.AssignProcessToJobObject(outer, windows.CurrentProcess()) != nil {
			os.Exit(5)
		}
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestOwnerCrashReapsNestedJobDescendants$")
		cmd.Env = append(os.Environ(), "AISW_CRASH_HELPER=worker")
		cmd.Stdout = os.Stdout
		if cliprocess.Configure(cmd) != nil || cliprocess.Start(cmd) != nil {
			os.Exit(6)
		}
		var ack [1]byte
		_, _ = os.Stdin.Read(ack[:])
		os.Exit(23)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOwnerCrashReapsNestedJobDescendants$")
	cmd.Env = append(os.Environ(), "AISW_CRASH_HELPER=owner")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	defer stdin.Close()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("owner exit=nil, want forced termination")
	}
	state, err := windows.WaitForSingleObject(h, 2000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant after owner exit=(%d,%v), want terminated", state, err)
	}
}

func TestJobConstraintFailsBeforeExecutingChild(t *testing.T) {
	switch os.Getenv("AISW_JOB_FAILURE_HELPER") {
	case "child":
		_ = os.WriteFile(os.Getenv("AISW_EXECUTED_MARKER"), []byte("executed"), 0o600)
		return
	case "owner":
		outer, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			os.Exit(4)
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		info.BasicLimitInformation.ActiveProcessLimit = 1
		if _, err := windows.SetInformationJobObject(outer, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			os.Exit(5)
		}
		if windows.AssignProcessToJobObject(outer, windows.CurrentProcess()) != nil {
			os.Exit(6)
		}
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestJobConstraintFailsBeforeExecutingChild$")
		cmd.Env = append(os.Environ(), "AISW_JOB_FAILURE_HELPER=child")
		if cliprocess.Configure(cmd) != nil {
			os.Exit(7)
		}
		if cliprocess.Start(cmd) == nil {
			_ = cmd.Cancel()
			_ = cmd.Wait()
			os.Exit(8)
		}
		return
	}
	marker := filepath.Join(t.TempDir(), "executed")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestJobConstraintFailsBeforeExecutingChild$")
	cmd.Env = append(os.Environ(), "AISW_JOB_FAILURE_HELPER=owner", "AISW_EXECUTED_MARKER="+marker)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("constrained helper=(%s,%v), want safe launch failure", output, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("execution marker stat=%v, want child never executed", err)
	}
}
