package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestBoundedCapture caps authentication output without disclosing its contents.
// TestBoundedCapture 限制认证输出大小且不暴露正文。
func TestBoundedCapture(t *testing.T) {
	w := &boundedCapture{limit: 4}
	if n, err := w.Write([]byte("1234")); n != 4 || err != nil {
		t.Fatalf("write = %d, %v, want 4, nil", n, err)
	}
	if _, err := w.Write([]byte("SECRET")); !errors.Is(err, errAuthCheck) {
		t.Fatalf("overflow = %v, want auth check failure", err)
	}
	if got := w.buffer.String(); got != "1234" {
		t.Fatalf("buffer = %q, want 1234", got)
	}
}

// TestBoundedCaptureCopy exercises io.Copy's optional ReaderFrom fast path.
// TestBoundedCaptureCopy 覆盖 io.Copy 可选 ReaderFrom 快速路径。
func TestBoundedCaptureCopy(t *testing.T) {
	w := &boundedCapture{limit: 4}
	_, err := io.Copy(w, struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 100))})
	if !errors.Is(err, errAuthCheck) || w.buffer.Len() > 4 {
		t.Fatalf("copy = %v, bytes = %d; want auth check failure and <= 4 bytes", err, w.buffer.Len())
	}
}

// TestBoundedCaptureSubprocess covers os/exec's actual stdout copy path.
// TestBoundedCaptureSubprocess 覆盖 os/exec 实际 stdout 复制路径。
func TestBoundedCaptureSubprocess(t *testing.T) {
	w := &boundedCapture{limit: 64 * 1024}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIHelper$")
	cmd.Env = append(os.Environ(), "AISW_PROBE_HELPER=auth-output")
	cmd.Stdout = w
	err := cmd.Run()
	if err == nil || w.buffer.Len() > 64*1024 {
		t.Fatalf("subprocess = %v, bytes = %d; want failure and <= 65536 bytes", err, w.buffer.Len())
	}
}

// TestCommandDefaults requires explicit consent to use a model subscription.
// TestCommandDefaults 要求显式开关才能使用模型订阅。
func TestCommandDefaults(t *testing.T) {
	var out bytes.Buffer
	if err := runCommand(t.Context(), nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("output empty, want usage")
	}
	for _, args := range [][]string{{"-live", "-sessions", "3"}, {"-live", "-timeout", "0s"}, {"-live", "-timeout", "11m"}, {"-live", "-model", ""}} {
		if err := runCommand(t.Context(), args, &out); !errors.Is(err, errProbeOptions) {
			t.Fatalf("args %v = %v, want invalid options", args, err)
		}
	}
}
