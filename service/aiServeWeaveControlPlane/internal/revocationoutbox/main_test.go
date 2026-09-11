package revocationoutbox_test

import (
	"os"
	goruntime "runtime"
	"testing"
	"time"
)

// TestMain asserts no test in this package leaks a goroutine. It is the single
// place that check happens, so individual tests do not hand-write it.
//
// TestMain 统一断言本包测试没有协程泄漏。检查只在这里进行，单个测试不自行重复实现。
func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !goroutineCountSettles(before) {
		_, _ = os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func goroutineCountSettles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
