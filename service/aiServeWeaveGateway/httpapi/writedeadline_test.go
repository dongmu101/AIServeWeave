package httpapi_test

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// firehoseStreamHandler streams large chat chunks until the Gateway stops
// taking them, so a client that never reads fills every buffer in between.
func firehoseStreamHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	content := strings.Repeat("x", 64<<10)
	for i := 0; i < 4096; i++ {
		payload, err := tunnelwire.MarshalChatEvent(runtime.ChatEvent{
			ID:    "chat-1",
			Delta: runtime.ChatMessageDelta{Role: "assistant", Content: content},
		})
		if err != nil {
			return err
		}
		if err := reply(gatewaytest.DataFrame(payload)); err != nil {
			return err
		}
	}
	return nil
}

// TestStreamingHandlerReturnsWhenTheClientStopsReading proves a client that
// keeps its connection open but never reads cannot pin the handler (and the
// node slot it holds) forever: once a write stalls past WriteStallTimeout,
// the handler returns on its own.
func TestStreamingHandlerReturnsWhenTheClientStopsReading(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	front := httpapi.New(scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock}), httpapi.Config{
		Logger:            slog.New(slog.DiscardHandler),
		WriteStallTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(front.Close)

	returned := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		front.ServeHTTP(w, r)
		returned <- struct{}{}
	}))
	t.Cleanup(srv.Close)

	connectNodeWithHandler(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), firehoseStreamHandler)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	// Closing the connection is what a normal disconnect test does; this one
	// keeps it open for the whole wait and simply never reads.
	t.Cleanup(func() { conn.Close() })
	body := `{"model":"qwen3:8b","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if _, err := fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: gateway\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		t.Fatalf("writing the request: %v", err)
	}

	select {
	case <-returned:
	case <-time.After(3 * gatewaytest.Timeout):
		t.Fatal("the streaming handler is still blocked writing to a client that stopped reading")
	}
}
