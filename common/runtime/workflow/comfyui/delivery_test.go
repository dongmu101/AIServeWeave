package comfyui

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestOnlyADialFailureIsMarkedNotDelivered pins the line a workflow submit
// may be resent across: a dial that never connected sent nothing, while a
// connection that broke later may already have posted the prompt.
func TestOnlyADialFailureIsMarkedNotDelivered(t *testing.T) {
	c := &Client{}
	dial := &url.Error{Op: "Post", URL: "http://comfy/prompt", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	if err := c.transportError(context.Background(), "submit", dial); !errors.Is(err, runtime.ErrNotDelivered) || err.Code != runtime.ErrorConnection {
		t.Fatalf("dial failure = %+v, want connection_failed marked not delivered", err)
	}
	reset := &url.Error{Op: "Post", URL: "http://comfy/prompt", Err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}}
	if err := c.transportError(context.Background(), "submit", reset); errors.Is(err, runtime.ErrNotDelivered) {
		t.Fatalf("a read failure after the request was sent must not be marked not delivered: %+v", err)
	}
}
