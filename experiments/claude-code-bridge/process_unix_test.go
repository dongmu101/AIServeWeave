//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProcessTreeHelper keeps a parent and its child alive until cancellation.
// TestProcessTreeHelper 让父进程及其子进程保持运行，直到测试取消。
func TestProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("AISW_TREE_MODE")
	if mode == "" {
		return
	}
	var child *exec.Cmd
	if strings.HasPrefix(mode, "parent") {
		child = exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
		child.Env = append(os.Environ(), "AISW_TREE_MODE=child")
		child.Stdout, child.Stderr = os.Stdout, io.Discard
		if mode != "parent" {
			child.Stdout = io.Discard
		}
		if child.Start() != nil {
			os.Exit(2)
		}
	}
	conn, err := net.Dial("unix", os.Getenv("AISW_TREE_SOCKET"))
	if err != nil {
		os.Exit(3)
	}
	if json.NewEncoder(conn).Encode(map[string]any{"mode": mode, "pid": os.Getpid()}) != nil {
		os.Exit(4)
	}
	var value [1]byte
	_, _ = conn.Read(value[:])
	_ = conn.Close()
	if mode == "parent-exit" || mode == "parent-error" {
		fmt.Println(`{"type":"result","subtype":"success"}`)
		if mode == "parent-error" {
			os.Exit(7)
		}
		os.Exit(0)
	}
	if child != nil {
		_ = child.Wait()
	}
	os.Exit(0)
}

// TestRunCLIReapsOrphan checks descendants after the parent exits on its own.
// TestRunCLIReapsOrphan 检查父进程自行退出后仍会回收后代。
func TestRunCLIReapsOrphan(t *testing.T) {
	for _, mode := range []string{"parent-exit", "parent-error"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "aisw-orphan-test-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "s")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			deadline := time.Now().Add(5 * time.Second)
			_ = listener.SetDeadline(deadline)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := runCLI(ctx, os.Args[0], []string{"-test.run=^TestProcessTreeHelper$"}, dir,
					append(os.Environ(), "AISW_TREE_MODE="+mode, "AISW_TREE_SOCKET="+path), strings.NewReader(""), "")
				done <- err
			}()
			connections := map[string]*net.UnixConn{}
			for range 2 {
				conn, err := listener.AcceptUnix()
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetDeadline(deadline)
				var ready struct {
					Mode string `json:"mode"`
				}
				if err := json.NewDecoder(conn).Decode(&ready); err != nil {
					t.Fatal(err)
				}
				connections[ready.Mode] = conn
			}
			if connections[mode] == nil || connections["child"] == nil {
				t.Fatal("missing handshake, want parent and child")
			}
			if _, err := connections[mode].Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if (mode == "parent-exit" && err != nil) || (mode == "parent-error" && !errors.Is(err, errCLIExit)) {
				t.Fatalf("exit = %v, want outcome matching %s", err, mode)
			}
			var value [1]byte
			if _, err := connections["child"].Read(value[:]); err != io.EOF {
				t.Fatalf("child connection = %v, want EOF after parent exit", err)
			}
		})
	}
}

// TestRunCLICancelProcessTree waits for both processes before canceling.
// TestRunCLICancelProcessTree 等父子进程都完成握手后再取消。
func TestRunCLICancelProcessTree(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "aisw-process-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	deadline := time.Now().Add(10 * time.Second)
	_ = listener.SetDeadline(deadline)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runCLI(ctx, os.Args[0], []string{"-test.run=^TestProcessTreeHelper$"}, dir,
			append(os.Environ(), "AISW_TREE_MODE=parent", "AISW_TREE_SOCKET="+path), strings.NewReader(""), "")
		done <- err
	}()
	var connections []*net.UnixConn
	seen := map[string]bool{}
	for range 2 {
		conn, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(deadline)
		var ready struct {
			Mode string `json:"mode"`
			PID  int    `json:"pid"`
		}
		if err := json.NewDecoder(conn).Decode(&ready); err != nil {
			t.Fatal(err)
		}
		if ready.PID <= 0 || seen[ready.Mode] {
			t.Fatalf("handshake = %+v, want two distinct ready processes", ready)
		}
		seen[ready.Mode] = true
		connections = append(connections, conn)
	}
	if !seen["parent"] || !seen["child"] {
		t.Fatalf("processes = %v, want parent and child", seen)
	}
	cancel()
	for _, conn := range connections {
		var value [1]byte
		if _, err := conn.Read(value[:]); err != io.EOF {
			t.Fatalf("process connection = %v, want EOF after group cancellation", err)
		}
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run = %v, want canceled", err)
	}
}
