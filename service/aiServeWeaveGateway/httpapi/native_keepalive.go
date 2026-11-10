package httpapi

import (
	"context"
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

// receiveResult is one Recv outcome handed from the receiving goroutine to
// the relay loop.
// receiveResult 是由接收协程交给转发循环的一次 Recv 结果。
type receiveResult[T any] struct {
	item T
	err  error
}

// streamReceiver reads a stream on one goroutine for the whole relay, so the
// relay loop can select between the next event and the heartbeat timer
// without starting a goroutine and a channel for every event.
//
// The goroutine is the only user of the stream: it calls Recv, and it calls
// Close, but only after Recv has returned and the relay has asked it to stop.
// tunnelserver.Response is single-consumer (see its recording state in
// call.go), so Close must never run beside a Recv; stop therefore cancels the
// context the stream was opened with, which makes a blocked Recv return, and
// waits for the goroutine instead of closing the stream itself. The goroutine
// reads at most one event ahead of the loop (results is unbuffered), so a slow
// client still backs up into the tunnel as it did before.
//
// streamReceiver 用一个协程为整个转发读取一条流，使转发循环可以在「下一个事件」与
// 「心跳定时器」之间 select，而不必为每个事件起一个协程和一个 channel。
//
// 这个协程是该流唯一的使用者：它调用 Recv，也调用 Close，但 Close 只在 Recv 已返回、
// 且转发循环要求停止之后才执行。tunnelserver.Response 是单消费者的（见 call.go 里的
// 记录状态），所以 Close 绝不能与 Recv 并行；因此 stop 取消流开启时所用的 context（让
// 阻塞中的 Recv 返回）并等待该协程，而不是自己去关闭流。该协程至多比循环多读一个事
// 件（results 无缓冲），慢客户端仍会像以前一样向隧道一侧产生背压。
type streamReceiver[T any] struct {
	results chan receiveResult[T]
	quit    chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
}

// streamReceiverStopWait bounds how long stop waits for a stream whose Recv
// ignores context cancellation. The goroutine then still closes the stream
// itself as soon as that Recv returns.
// streamReceiverStopWait 限定 stop 等待一个 Recv 不理会 context 取消的流的时长。到时
// 该协程仍会在那次 Recv 返回后自行关闭流。
const streamReceiverStopWait = 10 * time.Second

// startStreamReceiver takes over stream. cancel must cancel the context the
// stream was opened with.
// startStreamReceiver 接管 stream。cancel 必须能取消该流开启时所用的 context。
func startStreamReceiver[T any](stream runtime.Stream[T], cancel context.CancelFunc) *streamReceiver[T] {
	r := &streamReceiver[T]{
		results: make(chan receiveResult[T]),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		cancel:  cancel,
	}
	go func() {
		defer close(r.done)
		defer stream.Close()
		for {
			item, err := stream.Recv()
			select {
			case r.results <- receiveResult[T]{item, err}:
			case <-r.quit:
				return
			}
			if err != nil {
				// The loop has its final answer; keep the stream open until
				// the relay is finished with it, as a deferred Close did.
				// 循环已拿到最终结果；与原先 defer Close 一样，让流保持打开直到转发结束。
				<-r.quit
				return
			}
		}
	}()
	return r
}

// next returns the next Recv outcome. While it waits it calls ping each time
// streamKeepAlive passes, with awaitAlive's rules: a nil clock or ping waits
// plainly, ping runs on the caller's goroutine, and a failed ping stops the
// heartbeats but not the wait, which ends when the request context is
// cancelled and Recv returns. An event that is already waiting is returned
// without arming a timer.
//
// next 返回下一次 Recv 的结果。等待期间每隔 streamKeepAlive 调用一次 ping，规则与
// awaitAlive 相同：clock 或 ping 为 nil 时直接等待，ping 在调用方协程上运行，ping 失败
// 只停止心跳而不停止等待，等待在请求 context 取消、Recv 返回后结束。已在等待的事件会
// 直接返回，不装定时器。
func (r *streamReceiver[T]) next(clock runtime.Clock, ping func() error) (T, error) {
	select {
	case got := <-r.results:
		return got.item, got.err
	default:
	}
	if clock == nil || ping == nil {
		got := <-r.results
		return got.item, got.err
	}
	for {
		tick, stop := clock.NewTimer(streamKeepAlive)
		select {
		case got := <-r.results:
			stop()
			return got.item, got.err
		case <-tick:
			if ping() != nil {
				got := <-r.results
				return got.item, got.err
			}
		}
	}
}

// stop ends the relay's use of the stream: it unblocks a pending Recv through
// the context, lets the goroutine close the stream and waits for that.
// stop 结束转发对流的使用：通过 context 解除挂起的 Recv，让协程关闭流并等待其完成。
func (r *streamReceiver[T]) stop() {
	close(r.quit)
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(streamReceiverStopWait):
	}
}
