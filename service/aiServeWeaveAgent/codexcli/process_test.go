//go:build darwin || linux

package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func waitTreeFixture(parent bool) {
	var child *exec.Cmd
	if parent {
		child = exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "AISW_CODEX_FIXTURE=tree-child")
		child.Stdout, child.Stderr = os.Stdout, io.Discard
		if child.Start() != nil {
			os.Exit(2)
		}
	}
	conn, err := net.Dial("unix", os.Getenv("AISW_CODEX_READY"))
	if err != nil {
		os.Exit(3)
	}
	if json.NewEncoder(conn).Encode(map[string]bool{"parent": parent}) != nil {
		os.Exit(4)
	}
	var data [1]byte
	_, _ = conn.Read(data[:])
	_ = conn.Close()
	if child != nil {
		_ = child.Wait()
	}
}

// TestRunningProbeCancellation waits for app-server and its descendant to start.
// TestRunningProbeCancellation 等 app-server 及其后代启动后再取消。
func TestRunningProbeCancellation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "aisw-codex-cancel-")
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
		_, err := probeWithEnv(ctx, Config{Executable: os.Args[0]}, append(os.Environ(), "AISW_CODEX_FIXTURE=wait", "AISW_CODEX_READY="+path))
		done <- err
	}()
	var connections []*net.UnixConn
	seen := map[bool]bool{}
	for range 2 {
		conn, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(deadline)
		var ready struct {
			Parent bool `json:"parent"`
		}
		if err := json.NewDecoder(conn).Decode(&ready); err != nil {
			t.Fatal(err)
		}
		if seen[ready.Parent] {
			t.Fatal("duplicate ready process, want parent and child")
		}
		seen[ready.Parent] = true
		connections = append(connections, conn)
	}
	cancel()
	for _, conn := range connections {
		var data [1]byte
		if _, err := conn.Read(data[:]); err != io.EOF {
			t.Fatalf("connection = %v, want EOF after cancellation", err)
		}
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("probe = %v, want canceled", err)
	}
}
