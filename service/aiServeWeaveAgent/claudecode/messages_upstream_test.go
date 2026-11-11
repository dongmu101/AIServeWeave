package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"AIServeWeave/common/runtime"
)

func TestBackendFailurePreservesCapturedUpstreamErrors(t *testing.T) {
	r := &Runtime{cfg: runtime.Config{ID: "fixture"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tc := range []struct {
		status int
		kind   string
	}{{400, "invalid_request_error"}, {429, "rate_limit_error"}, {529, "overloaded_error"}} {
		t.Run(tc.kind, func(t *testing.T) {
			body := []byte(`{"type":"error","error":{"type":"` + tc.kind + `","message":"fixture"}}`)
			record := &upstreamRecord{status: tc.status, body: body, header: http.Header{"Retry-After": []string{"7"}}}
			capture := &upstreamCapture{}
			capture.record(record)
			failure := withUpstream(r.roundError(errCLIResult), errCLIResult, capture)
			var public *runtime.RuntimeError
			if !errors.As(failure, &public) || public.StatusCode != tc.status || public.Upstream == nil || !bytes.Equal(public.Upstream.Body, body) {
				t.Fatalf("local classification replaced captured upstream failure: %+v", public)
			}
			if tc.status == 429 && (!public.Retryable || public.Code != runtime.ErrorRateLimited || public.RetryAfter != 7*time.Second) {
				t.Fatalf("lost upstream retry information: %+v", public)
			}
		})
	}
}

func TestUpstreamProxyCloseCancelsPendingConnect(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "HTTPS"}[secure], func(t *testing.T) {
			testUpstreamProxyCloseCancelsPendingConnect(t, secure)
		})
	}
}

func testUpstreamProxyCloseCancelsPendingConnect(t *testing.T, secure bool) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	parent := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("unexpected parent proxy method: %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		close(entered)
		<-release // The parent accepts TCP but never answers CONNECT until cleanup.
		w.WriteHeader(http.StatusBadGateway)
	}))
	if secure {
		parent.StartTLS()
	} else {
		parent.Start()
	}
	t.Cleanup(parent.Close)
	p, err := startUpstreamProxy(&upstreamCapture{}, []string{"HTTPS_PROXY=" + parent.URL})
	if err != nil {
		unblock()
		t.Fatal(err)
	}
	if secure {
		p.transport.TLSClientConfig.RootCAs.AddCert(parent.Certificate())
	}
	stopped := make(chan struct{})
	stop := sync.OnceFunc(func() { go func() { p.close(); close(stopped) }() })
	t.Cleanup(func() { unblock(); stop(); <-stopped })
	client, err := net.DialTimeout("tcp", p.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: "tunnel.invalid:443"}, Host: "tunnel.invalid:443", Header: make(http.Header)}
	request.Header.Set("Proxy-Authorization", p.authority)
	if err := request.Write(client); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("local CONNECT failed: %v / %v", response, err)
	}
	_ = response.Body.Close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("parent never received CONNECT")
	}
	stop()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		unblock()
		<-stopped
		t.Fatal("proxy shutdown waited for the parent's CONNECT response after cancellation")
	}
}

func TestUpstreamProxyEstablishedConnectSurvivesDialCancellation(t *testing.T) {
	finished := make(chan struct{})
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		conn, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nready"); err != nil {
			return
		}
		_, _ = io.Copy(conn, buffered)
	}))
	t.Cleanup(parent.Close)
	p, err := startUpstreamProxy(&upstreamCapture{}, []string{"HTTPS_PROXY=" + parent.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := p.dialThrough(ctx, "tunnel.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); <-finished })
	cancel() // tunnel releases the dialing context immediately after connection.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	ready := make([]byte, len("ready"))
	if _, err := io.ReadFull(conn, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("lost CONNECT buffered bytes: %q / %v", ready, err)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ping" {
		t.Fatalf("established tunnel closed with dialing context: %q / %v", got, err)
	}
}

func TestUpstreamSSEErrorFraming(t *testing.T) {
	body := `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`
	for _, tc := range []struct {
		name, stream, want string
	}{
		{"single line", "event: error\ndata: " + body + "\n\n", body},
		{"multiline", "event: error\ndata: {\"type\":\"error\",\ndata: \"error\":{\"type\":\"overloaded_error\",\"message\":\"fixture\"}}\n\n", "{\"type\":\"error\",\n\"error\":{\"type\":\"overloaded_error\",\"message\":\"fixture\"}}"},
		{"CRLF", "event:error\r\ndata:" + body + "\r\n\r\n", body},
		{"CR", "event:error\rdata:" + body + "\r\r", body},
		{"event after data", "data: " + body + "\nevent: error\n\n", body},
		{"comments", ": keep alive\nevent: error\n: comment\ndata: " + body + "\n\n", body},
		{"event name replaced", "event: error\nevent: message\ndata: " + body + "\n\n", ""},
		{"incomplete event", "event: error\ndata: " + body + "\n", ""},
		{"oversized event then valid", "event:error\ndata:" + strings.Repeat("x", runtime.MaxUpstreamErrorBytes/2) + "\ndata:" + strings.Repeat("x", runtime.MaxUpstreamErrorBytes/2) + "\n\nevent:error\ndata:" + body + "\n\n", body},
		{"oversized line then valid", "event:error\ndata:" + strings.Repeat("x", runtime.MaxUpstreamErrorBytes+32) + "\n\nevent:error\ndata:" + body + "\n\n", body},
		{"ordinary event then error", "event: message_start\ndata: {}\n\nevent:error\ndata:" + body + "\n\n", body},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured []byte
			reader := &sseErrorReader{Reader: iotest.OneByteReader(strings.NewReader(tc.stream)), found: func(data []byte) {
				if runtime.ValidUpstreamErrorBody(data) {
					captured = bytes.Clone(data)
				}
			}}
			got, err := io.ReadAll(reader)
			if err != nil || string(got) != tc.stream {
				t.Fatal("SSE bytes changed during capture")
			}
			if string(captured) != tc.want {
				t.Fatalf("captured error = %q; want %q", captured, tc.want)
			}
		})
	}
}

func TestUpstreamSSEMultilineFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   runtime.ErrorCode
	}{
		{"overloaded_error", 529, runtime.ErrorUpstream},
		{"rate_limit_error", 429, runtime.ErrorRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "{\"type\":\"error\",\n\"error\":{\"type\":\"" + tc.name + "\",\"message\":\"fixture\"}}"
			wire := "event:error\ndata: " + strings.ReplaceAll(body, "\n", "\ndata: ") + "\n\n"
			env := nativeFixtureEnvironment(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Retry-After", "7")
				_, _ = io.WriteString(w, wire)
			}))
			capture := &upstreamCapture{}
			p, err := startUpstreamProxy(capture, env)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.close)
			input := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"stream":true}`))
			response := httptest.NewRecorder()
			p.forward(response, input, &url.URL{Scheme: "https", Host: upstreamAPIHost, Path: "/v1/messages"}, true)
			if response.Code != 200 || response.Body.String() != wire {
				t.Fatal("upstream stream changed during capture")
			}
			failure := withUpstream(&runtime.RuntimeError{Code: runtime.ErrorUpstream}, errMissingResult, capture)
			var public *runtime.RuntimeError
			if !errors.As(failure, &public) || public.Code != tc.code || public.StatusCode != tc.status || public.Upstream == nil || string(public.Upstream.Body) != body || public.Upstream.Header.Get("Retry-After") != "7" {
				t.Fatalf("lost upstream error: %+v", public)
			}
			if tc.status == 429 && (!public.Retryable || public.RetryAfter != 7*time.Second) {
				t.Fatalf("lost retry classification: %+v", public)
			}
		})
	}
}
