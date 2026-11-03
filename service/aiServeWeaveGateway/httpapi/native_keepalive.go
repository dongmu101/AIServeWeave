package httpapi

import (
	"time"

	"AIServeWeave/common/runtime"
)

// streamKeepAlive is how long a native stream may stay silent before the
// Gateway writes a heartbeat. A CLI can take well over a minute to start and
// produce its first token on a long context, and a reverse proxy in front of
// the Gateway (Nginx's proxy_read_timeout defaults to 60 seconds) drops a
// response that stays silent that long, headers included.
//
// streamKeepAlive 是原生流在 Gateway 写出心跳前允许静默的时长。长上下文下 CLI
// 启动并产出首个 token 可能远超一分钟，而 Gateway 前面的反向代理（Nginx 的
// proxy_read_timeout 默认 60 秒）会切断静默这么久的响应，响应头尚未发出也一样。
const streamKeepAlive = 15 * time.Second

// awaitAlive runs fn and, while it is outstanding, calls ping each time
// streamKeepAlive passes. With a nil clock or ping, fn runs inline. A failed
// ping means the client is gone: heartbeats stop, but awaitAlive still waits
// for fn, which ends once the request context is cancelled, so fn never
// outlives the caller. ping runs on the caller's goroutine, so it may touch
// the caller's response state without locking.
//
// awaitAlive 执行 fn，并在 fn 未返回期间每隔 streamKeepAlive 调用一次 ping。clock
// 或 ping 为 nil 时 fn 在当前协程内执行。ping 失败说明客户端已离开：停止心跳，但
// 仍等待 fn 返回（请求上下文取消后 fn 即结束），因此 fn 不会比调用方存活更久。ping
// 在调用方协程上运行，可以不加锁地操作调用方的响应状态。
func awaitAlive[T any](clock runtime.Clock, ping func() error, fn func() T) T {
	if clock == nil || ping == nil {
		return fn()
	}
	done := make(chan T, 1)
	go func() { done <- fn() }()
	for {
		tick, stop := clock.NewTimer(streamKeepAlive)
		select {
		case v := <-done:
			stop()
			return v
		case <-tick:
			if ping() != nil {
				return <-done
			}
		}
	}
}
