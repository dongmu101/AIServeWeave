package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain checks goroutine cleanup once for the entire command package.
// TestMain 统一检查整个命令包的协程回收。
func TestMain(m *testing.M) {
	before := runtime.NumGoroutine()
	code := m.Run()
	deadline := time.Now().Add(2 * time.Second)
	for code == 0 && runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "goroutines remain, want initial count")
			code = 1
			break
		}
		runtime.Gosched()
	}
	os.Exit(code)
}

// TestCommandOptions keeps live execution explicit and bounded.
// TestCommandOptions 保证真实执行必须显式启用且参数有界。
func TestCommandOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		fail bool
	}{
		{"help", nil, false}, {"too many sessions", []string{"-live", "-sessions", "3"}, true},
		{"zero sessions", []string{"-live", "-sessions", "0"}, true}, {"timeout", []string{"-live", "-timeout", "3m"}, true},
		{"zero timeout", []string{"-live", "-timeout", "0s"}, true}, {"extra input", []string{"-live", "unexpected"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(t.Context(), tc.args, &out)
			if (err != nil) != tc.fail {
				t.Fatalf("error = %v, want failure %v", err, tc.fail)
			}
			if !tc.fail && out.Len() == 0 {
				t.Fatal("output empty, want usage")
			}
		})
	}
}
