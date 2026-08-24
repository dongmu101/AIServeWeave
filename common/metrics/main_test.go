package metrics

import (
	"os"
	goruntime "runtime"
	"testing"
	"time"
)

// TestMain asserts no test in this package leaks a goroutine, per the README
// quality gate: this is the single place that check happens, so individual
// tests do not need to hand-write it. A short polling window tolerates
// goroutines (GC workers, finalizers) that wind down slightly after the last
// test returns rather than exactly synchronously.
//
// TestMain 断言本包没有测试泄漏协程，对应 README 的质量门禁：这项检查只在这一处
// 进行，单个测试不必自己手写。一小段轮询窗口用于容忍那些在最后一个测试返回之后
// 才逐渐退出、而非严格同步退出的协程（GC 工作协程、终结器）。
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
