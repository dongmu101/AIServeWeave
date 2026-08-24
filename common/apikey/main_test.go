package apikey_test

import (
	"os"
	goruntime "runtime"
	"testing"
	"time"
)

// TestMain asserts no test in this package leaks a goroutine, per the README
// quality gate: this is the single place that check happens, so individual
// tests do not need to hand-write it.
//
// TestMain 断言本包没有测试泄漏协程，对应 README 的质量门禁：这项检查只在这一处进行，
// 单个测试不必自己手写。
func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !goroutineCountSettles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
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
