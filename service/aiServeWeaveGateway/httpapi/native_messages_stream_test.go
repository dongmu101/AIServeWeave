package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	rt "AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/messagesession"
)

// nativeSnapshot describes a runtime serving model over native Messages;
// native additionally advertises the unchanged-document capability, which
// selects the passthrough path (without it the compatibility bridge runs).
//
// nativeSnapshot 描述一个通过原生 Messages 提供 model 的运行时；native 额外声明
// 原样文档能力，从而选择透传路径（没有它则走兼容桥接）。
func nativeSnapshot(runtimeID, model string, native bool) rt.Snapshot {
	caps := rt.CapabilitySet{rt.CapabilityMessages: {Level: rt.SupportSupported}}
	if native {
		caps[rt.CapabilityMessagesNative] = rt.CapabilityEvidence{Level: rt.SupportSupported}
	}
	return rt.Snapshot{
		Descriptor: rt.Descriptor{ID: runtimeID, Kind: rt.KindOllama, BaseURL: "http://127.0.0.1:11434", MaxConcurrent: 4},
		State:      rt.StateHealthy,
		Discovery:  rt.Discovery{Models: []rt.Model{{ID: model, Capabilities: caps}}},
	}
}

// messagesEventsHandler plays the Agent: it answers a Messages request with
// the given events, one tunnel frame each, then ends with finish's error (nil
// for a clean end). wait, when non-nil, is called before event i is sent, so
// a test can hold the stream silent.
//
// messagesEventsHandler 扮演 Agent：用给定事件应答 Messages 请求，每个事件一个隧道帧，
// 最后以 finish 的错误结束（nil 表示正常结束）。wait 非 nil 时，在发送第 i 个事件之前
// 被调用，使测试可以让流保持静默。
func messagesEventsHandler(events []string, wait func(i int), finish error) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if req.GetOperation() != tunnelv1.Operation_OPERATION_MESSAGES {
			return errors.New("unsupported operation")
		}
		for i, event := range events {
			if wait != nil {
				wait(i)
			}
			payload, err := tunnelwire.MarshalMessagesEvent(rt.MessagesEvent{JSON: json.RawMessage(event)})
			if err != nil {
				return err
			}
			if err := reply(gatewaytest.DataFrame(payload)); err != nil {
				return err
			}
		}
		return finish
	}
}

// discardingStore backs the compatibility bridge in tests that never reach a
// tool continuation.
type discardingStore struct{}

func (discardingStore) Publish(context.Context, messagesession.Record) error { return nil }
func (discardingStore) Claim(context.Context, []string, string, string, string) (messagesession.Record, func(context.Context, bool) error, error) {
	return messagesession.Record{}, nil, messagesession.ErrExpired
}

// nativeScenario is a Messages stream covering every block kind the loop
// distinguishes, with deltas deltas on each streamed block. It returns the
// upstream events, the SSE bytes a client must receive, and the aggregated
// JSON a non-streaming client must receive on the passthrough path and on the
// compatibility bridge.
func nativeScenario(deltas int) (events []string, stream, passthroughJSON, bridgeJSON string) {
	events = append(events, `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"upstream-real","content":[],"usage":{"input_tokens":12,"output_tokens":1}}}`)
	events = append(events, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	for i := 0; i < deltas; i++ {
		events = append(events, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"t%d "}}`, i))
	}
	events = append(events, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-a"}}`)
	events = append(events, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-b"}}`)
	events = append(events, `{"type":"content_block_stop","index":0}`)
	events = append(events, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
	for i := 0; i < deltas; i++ {
		events = append(events, fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"w%d "}}`, i))
	}
	events = append(events, `{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x"}}}`)
	events = append(events, `{"type":"content_block_stop","index":1}`)
	events = append(events, `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`)
	events = append(events, `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`)
	events = append(events, `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"/a\"}"}}`)
	events = append(events, `{"type":"content_block_stop","index":2}`)
	events = append(events, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":34}}`)
	events = append(events, `{"type":"message_stop"}`)

	var b strings.Builder
	for _, e := range events {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(e), &head)
		data := e
		if head.Type == "message_start" {
			// The Gateway pins the public model name and re-encodes the
			// document, so its keys come back sorted.
			data = `{"message":{"content":[],"id":"msg_1","model":"claude-x","role":"assistant","type":"message","usage":{"input_tokens":12,"output_tokens":1}},"type":"message_start"}`
		}
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", head.Type, data)
	}
	stream = b.String()

	var thinking, text strings.Builder
	for i := 0; i < deltas; i++ {
		fmt.Fprintf(&thinking, "t%d ", i)
		fmt.Fprintf(&text, "w%d ", i)
	}
	// The passthrough re-encodes the upstream's own blocks (keys sorted,
	// citations kept, stop_sequence echoed); the bridge renders its own typed
	// blocks in struct order and drops what it does not model.
	passthroughJSON = `{"content":[` +
		`{"signature":"sig-asig-b","thinking":"` + thinking.String() + `","type":"thinking"},` +
		`{"citations":[{"type":"char_location","cited_text":"x"}],"text":"` + text.String() + `","type":"text"},` +
		`{"id":"toolu_1","input":{"path":"/a"},"name":"read","type":"tool_use"}],` +
		`"id":"msg_1","model":"claude-x","role":"assistant","stop_reason":"end_turn","stop_sequence":null,"type":"message",` +
		`"usage":{"input_tokens":12,"output_tokens":34}}` + "\n"
	bridgeJSON = `{"content":[` +
		`{"type":"thinking","thinking":"` + thinking.String() + `","signature":"sig-asig-b"},` +
		`{"type":"text","text":"` + text.String() + `"},` +
		`{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"/a"}}],` +
		`"id":"msg_1","model":"claude-x","role":"assistant","stop_reason":"end_turn","type":"message",` +
		`"usage":{"input_tokens":12,"output_tokens":34}}` + "\n"
	return events, stream, passthroughJSON, bridgeJSON
}

func nativeMessagesRequest(t testing.TB, url string, stream bool) *http.Request {
	t.Helper()
	body := fmt.Sprintf(`{"model":"claude-x","max_tokens":1024,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
	req, err := http.NewRequest(http.MethodPost, url+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+goodKey)
	return req
}

func postNativeMessages(t *testing.T, url string, stream bool) (*http.Response, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(nativeMessagesRequest(t, url, stream))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(out)
}

func clip(s string) string {
	if len(s) > 1500 {
		return s[:700] + " ... " + s[len(s)-700:]
	}
	return s
}

// TestNativeMessagesForwardsBytesUnchanged pins the exact bytes of a native
// Messages relay: the SSE stream is the upstream's events plus the one
// rewritten message_start, and the aggregated JSON carries every streamed
// block. It runs the passthrough path and the compatibility bridge, streaming
// and not, so a change to how the loop accumulates content cannot alter what
// a client receives.
//
// TestNativeMessagesForwardsBytesUnchanged 钉住一次原生 Messages 转发的确切字节：SSE 流
// 是上游事件加上唯一被改写的 message_start，聚合出的 JSON 带有每个流式块。它覆盖透传路径
// 与兼容桥接、流式与非流式，使循环累积内容方式的改动不会改变客户端收到的东西。
func TestNativeMessagesForwardsBytesUnchanged(t *testing.T) {
	events, wantStream, wantPassthrough, wantBridge := nativeScenario(40)
	for _, native := range []bool{true, false} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("native=%t/stream=%t", native, stream), func(t *testing.T) {
				srv, h := newServer(t, httpapi.Config{Verifier: acceptingVerifier(), MessagesSessions: discardingStore{}})
				connectNode(t, h, "node-a", "backend-1", nativeSnapshot("backend-1", "claude-x", native), messagesEventsHandler(events, nil, nil))

				resp, got := postNativeMessages(t, srv.URL, stream)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, body=%s", resp.StatusCode, got)
				}
				want := wantBridge
				switch {
				case stream:
					want = wantStream
				case native:
					want = wantPassthrough
				}
				if got != want {
					t.Fatalf("body differs\n got: %s\nwant: %s", clip(got), clip(want))
				}
			})
		}
	}
}

// sseLines reads a response body line by line on its own goroutine, so a test
// can interleave clock advances with what the Gateway has written so far.
func sseLines(t *testing.T, body io.Reader) <-chan string {
	t.Helper()
	lines := make(chan string, 1024)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

// nextEvent returns the type of the next "event:" line.
func nextEvent(t *testing.T, lines <-chan string) string {
	t.Helper()
	timeout := time.After(gatewaytest.Timeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the stream ended while waiting for an event")
			}
			if name, found := strings.CutPrefix(line, "event: "); found {
				return name
			}
		case <-timeout:
			t.Fatal("timed out waiting for an SSE event")
		}
	}
}

// TestNativeMessagesKeepAliveFollowsSilence pins the heartbeat timing: a
// stream that stays silent for streamKeepAlive (15 seconds) gets a ping, the
// silence is measured from the last event rather than from the start of the
// stream, events arriving within the window suppress it, and a stream that
// stays silent keeps getting one ping per window.
//
// TestNativeMessagesKeepAliveFollowsSilence 钉住心跳时机：静默满 streamKeepAlive（15 秒）
// 的流会收到 ping，静默从最后一个事件起算而不是从流开始起算，窗口内到达的事件会抑制
// 心跳，保持静默的流则每个窗口收到一个 ping。
func TestNativeMessagesKeepAliveFollowsSilence(t *testing.T) {
	events, _, _, _ := nativeScenario(2)
	gates := map[int]chan struct{}{}
	for _, i := range []int{0, 2, 3, 4} {
		gates[i] = make(chan struct{})
	}
	handler := messagesEventsHandler(events, func(i int) {
		if gate, ok := gates[i]; ok {
			select {
			case <-gate:
			case <-time.After(gatewaytest.Timeout):
			}
		}
	}, nil)
	srv, h := newImagesServer(t, httpapi.Config{Verifier: acceptingVerifier()})
	connectNode(t, h, "node-a", "backend-1", nativeSnapshot("backend-1", "claude-x", true), handler)
	base := h.Clock.PendingTimers()

	// The response headers only go out with the first heartbeat or event, so
	// the request cannot be awaited before the clock is advanced.
	// 响应头只随第一个心跳或事件发出，所以不能在推进时钟之前等待请求返回。
	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := http.DefaultClient.Do(nativeMessagesRequest(t, srv.URL, true))
		done <- result{resp, err}
	}()
	var lines <-chan string
	connect := func() {
		t.Helper()
		got := <-done
		if got.err != nil {
			t.Fatalf("POST: %v", got.err)
		}
		t.Cleanup(func() { got.resp.Body.Close() })
		lines = sseLines(t, got.resp.Body)
	}

	armed := func(what string) {
		t.Helper()
		gatewaytest.WaitFor(t, "the heartbeat timer to be armed "+what, func() bool { return h.Clock.PendingTimers() == base+1 })
	}
	expect := func(want string) {
		t.Helper()
		if got := nextEvent(t, lines); got != want {
			t.Fatalf("next SSE event = %q, want %q", got, want)
		}
	}

	// Silent before the first event: the heartbeat opens the response.
	armed("while waiting for the first event")
	h.Clock.Advance(14 * time.Second)
	h.Clock.Advance(time.Second)
	connect()
	expect("ping")
	armed("again after the ping")
	close(gates[0])
	expect("message_start")
	expect("content_block_start")

	// 14 silent seconds after an event is not enough; the event that follows
	// is the next thing on the wire.
	armed("after content_block_start")
	h.Clock.Advance(14 * time.Second)
	close(gates[2])
	expect("content_block_delta")

	// The window restarts at that event, not at the stream's start.
	armed("after the first delta")
	h.Clock.Advance(14 * time.Second)
	close(gates[3])
	expect("content_block_delta")

	// A full window of silence after an event pings, then pings again.
	armed("after the second delta")
	h.Clock.Advance(15 * time.Second)
	expect("ping")
	armed("after the second ping")
	h.Clock.Advance(15 * time.Second)
	expect("ping")
	close(gates[4])
	expect("content_block_delta")
}

// TestNativeMessagesClientDisconnectStopsRelay checks that a client leaving
// mid-stream ends the handler and leaves no goroutine behind, with Recv and
// Close never overlapping (go test -race reports it otherwise).
//
// TestNativeMessagesClientDisconnectStopsRelay 检查客户端中途离开会结束 handler 并且
// 不留下协程；Recv 与 Close 绝不重叠（否则 go test -race 会报告）。
func TestNativeMessagesClientDisconnectStopsRelay(t *testing.T) {
	events, _, _, _ := nativeScenario(50)
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	defer release()
	handler := messagesEventsHandler(events, func(i int) {
		if i == 20 {
			select {
			case <-hold:
			case <-time.After(gatewaytest.Timeout):
			}
		}
	}, nil)
	srv, h := newImagesServer(t, httpapi.Config{Verifier: acceptingVerifier()})
	connectNode(t, h, "node-a", "backend-1", nativeSnapshot("backend-1", "claude-x", true), handler)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	settle := func() int {
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	before := settle()

	ctx, cancel := context.WithCancel(context.Background())
	req := nativeMessagesRequest(t, srv.URL, true).WithContext(ctx)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	lines := sseLines(t, resp.Body)
	for i := 0; i < 5; i++ {
		nextEvent(t, lines)
	}
	cancel()
	resp.Body.Close()
	release()
	client.CloseIdleConnections()
	gatewaytest.WaitFor(t, "the relay's goroutines to exit", func() bool { return runtime.NumGoroutine() <= before })
}

// TestNativeMessagesUpstreamFailureEndsTheStream checks the three ways a
// stream can break after the client has seen events: the Agent ends it with an
// error, it ends before message_stop, and an event the Gateway cannot relay.
// Each must close the SSE stream with one error event and nothing after it.
//
// TestNativeMessagesUpstreamFailureEndsTheStream 检查客户端已见到事件后流中断的三种方式：
// Agent 以错误结束、在 message_stop 之前结束，以及 Gateway 无法转发的事件。每一种都必须
// 以一个 error 事件结束 SSE 流，其后没有任何内容。
func TestNativeMessagesUpstreamFailureEndsTheStream(t *testing.T) {
	events, _, _, _ := nativeScenario(3)
	for _, tc := range []struct {
		name    string
		events  []string
		finish  error
		message string
	}{
		{"agent error", events[:5], errors.New("boom"), "native Messages stream failed"},
		{"ends before message_stop", events[:len(events)-1], nil, "native Messages stream failed"},
		{"unsupported event", append(append([]string(nil), events[:4]...), `{"type":"ping"}`), nil, "unsupported native Messages event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{Verifier: acceptingVerifier()})
			connectNode(t, h, "node-a", "backend-1", nativeSnapshot("backend-1", "claude-x", true), messagesEventsHandler(tc.events, nil, tc.finish))
			_, got := postNativeMessages(t, srv.URL, true)
			index := strings.LastIndex(got, "event: error\n")
			if index < 0 || !strings.Contains(got[index:], tc.message) || strings.Count(got[index:], "\n\n") != 1 {
				t.Fatalf("stream tail = %q, want one final error event mentioning %q", clip(got[max(0, index):]), tc.message)
			}
		})
	}
}

// TestNativeMessagesStreamCost reports the per-request cost of relaying a
// long native stream (20000 events by default) as Go benchmark output, so a
// change to the relay loop can be measured. It needs a gatewaytest harness,
// which only takes a *testing.T, so it drives testing.Benchmark itself and is
// opt-in:
//
//	AISW_STREAM_BENCH=1 go test -run TestNativeMessagesStreamCost -v ./service/aiServeWeaveGateway/httpapi/
//
// TestNativeMessagesStreamCost 以 Go 基准输出报告转发一个长原生流（默认 20000 个事件）
// 的单请求开销，使转发循环的改动可以被度量。gatewaytest 框架只接受 *testing.T，所以它自己
// 驱动 testing.Benchmark，并且需要主动开启（上面的命令）。
func TestNativeMessagesStreamCost(t *testing.T) {
	if os.Getenv("AISW_STREAM_BENCH") == "" {
		t.Skip("set AISW_STREAM_BENCH=1 to run the relay cost benchmark")
	}
	events, _, _, _ := nativeScenario(10000)
	for _, tc := range []struct {
		name   string
		stream bool
		native bool
	}{{"passthrough-stream", true, true}, {"passthrough-json", false, true}, {"bridge-stream", true, false}, {"bridge-json", false, false}} {
		srv, h := newImagesServer(t, httpapi.Config{Verifier: acceptingVerifier(), MessagesSessions: discardingStore{}})
		connectNode(t, h, "node-a", "backend-1", nativeSnapshot("backend-1", "claude-x", tc.native), messagesEventsHandler(events, nil, nil))
		handler := srv.Config.Handler
		result := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				w := &countingWriter{header: http.Header{}}
				handler.ServeHTTP(w, nativeMessagesRequest(b, srv.URL, tc.stream))
				if w.status != 0 && w.status != http.StatusOK || w.n == 0 {
					b.Fatalf("status=%d bytes=%d", w.status, w.n)
				}
				// The Agent's slot re-parks asynchronously after a request.
				for gatewaytest.IdleCount(h, "node-a") != 1 {
					time.Sleep(50 * time.Microsecond)
				}
			}
		})
		t.Logf("%s (%d events): %s  %s", tc.name, len(events), result.String(), result.MemString())
	}
}

// countingWriter is a ResponseWriter that only counts what is written.
type countingWriter struct {
	header http.Header
	status int
	n      int
}

func (w *countingWriter) Header() http.Header { return w.header }
func (w *countingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *countingWriter) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }
func (w *countingWriter) Flush()                      {}

var _ http.Flusher = (*countingWriter)(nil)
