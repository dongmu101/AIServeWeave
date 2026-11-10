package httpapi

import (
	"net/http"
	"time"
)

// DefaultWriteStallTimeout is how long one write or flush to a client may
// block before the connection is abandoned.
//
// A streamed response cannot use http.Server.WriteTimeout, which bounds the
// whole response and would cut off any generation longer than it. What
// actually needs bounding is a client that stops reading: its TCP window
// fills, the next write blocks, and the handler — together with the tunnel
// slot it holds on a node — waits with it. A per-write deadline catches
// that while leaving long pauses between chunks (a model thinking) alone,
// since no write is in progress then.
//
// DefaultWriteStallTimeout 是对客户端的单次写出或 flush 在连接被放弃之前最多能
// 阻塞多久。
//
// 流式响应不能使用 http.Server.WriteTimeout：它限定整个响应，会切断任何比它更长的
// 生成。真正需要限定的是停止读取的客户端：它的 TCP 窗口被填满，下一次写出阻塞，
// 处理函数——连同它在节点上占着的隧道槽——一起等下去。按次写出的超时能抓住这种情况，
// 同时不影响 chunk 之间的长停顿（模型在思考），因为那时并没有正在进行的写出。
const DefaultWriteStallTimeout = 60 * time.Second

// withWriteDeadline arms a fresh write deadline before every write and flush
// the handler makes. When one expires, net/http fails the write and cancels
// the request's context, so the handler returns and its deferred tunnel
// Close carries the cancel to the Agent, freeing the slot. A non-positive
// timeout leaves next unwrapped.
//
// withWriteDeadline 在处理函数每次写出与 flush 之前设置新的写截止时间。一旦超时，
// net/http 让该次写出失败并取消请求的 context，于是处理函数返回，它 defer 的隧道
// Close 把取消送给 Agent，槽位随之释放。timeout 不为正时不包装 next。
func withWriteDeadline(timeout time.Duration, next http.Handler) http.Handler {
	if timeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		// http.Server without a WriteTimeout never resets the write deadline
		// between requests, so a keep-alive connection would otherwise carry
		// the previous response's deadline, possibly already past, into this
		// one — including into the 100 Continue net/http sends when the body
		// is first read.
		//
		// 没有 WriteTimeout 的 http.Server 不会在请求之间重置写截止时间，否则一条
		// keep-alive 连接会把上一个响应的截止时间（可能已经过去）带进这个请求——
		// 包括 net/http 在首次读取请求体时发出的 100 Continue。
		_ = rc.SetWriteDeadline(time.Time{})
		dw := &deadlineWriter{ResponseWriter: w, rc: rc, timeout: timeout}
		// net/http flushes whatever is still buffered after the handler
		// returns; that final write gets a bound of its own too.
		//
		// 处理函数返回后，net/http 还会把缓冲中剩余的内容写出；最后这次写出同样要有上限。
		defer dw.arm()
		next.ServeHTTP(dw, r)
	})
}

// deadlineWriter is the ResponseWriter withWriteDeadline hands down.
//
// deadlineWriter 是 withWriteDeadline 往下传递的 ResponseWriter。
type deadlineWriter struct {
	http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

// arm ignores the error: a connection that does not support deadlines (a
// test recorder, a hijacked connection) simply goes unbounded, as before.
//
// arm 忽略错误：不支持截止时间的连接（测试用的 recorder、被劫持的连接）只是像以前
// 一样没有上限。
func (w *deadlineWriter) arm() {
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.timeout))
}

func (w *deadlineWriter) Write(b []byte) (int, error) {
	w.arm()
	return w.ResponseWriter.Write(b)
}

func (w *deadlineWriter) Flush() {
	_ = w.FlushError()
}

// FlushError is what http.ResponseController.Flush prefers, so callers that
// flush through a controller see the timeout instead of losing it.
//
// FlushError 是 http.ResponseController.Flush 优先使用的方法，因此经由控制器
// flush 的调用方能看到超时错误，而不是把它丢掉。
func (w *deadlineWriter) FlushError() error {
	w.arm()
	return w.rc.Flush()
}

// Unwrap lets http.ResponseController reach the connection beneath.
//
// Unwrap 让 http.ResponseController 能够触及下层连接。
func (w *deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
