package runtimetest

import (
	"context"
	"errors"
	"io"
	"net/http"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

// WSDialer is a scriptable runtime.WSDialer for the ComfyUI adapter's
// tests. Without a configured DialFunc, Dial fails loudly rather than
// silently returning a connection nobody set up.
type WSDialer struct {
	DialFunc func(ctx context.Context, url string, header http.Header) (runtime.WSConn, error)
}

func (d *WSDialer) Dial(ctx context.Context, url string, header http.Header) (runtime.WSConn, error) {
	if d.DialFunc != nil {
		return d.DialFunc(ctx, url, header)
	}
	return nil, errors.New("runtimetest: WSDialer.DialFunc is not configured")
}

var _ runtime.WSDialer = (*WSDialer)(nil)

// WSConn is a scriptable runtime.WSConn. Without a configured ReadFunc,
// Read reports io.EOF, matching a connection that closed with nothing to
// deliver.
type WSConn struct {
	ReadFunc  func(ctx context.Context) (messageType int, data []byte, err error)
	CloseFunc func() error
}

func (c *WSConn) Read(ctx context.Context) (int, []byte, error) {
	if c.ReadFunc != nil {
		return c.ReadFunc(ctx)
	}
	return 0, nil, io.EOF
}

func (c *WSConn) Close() error {
	if c.CloseFunc != nil {
		return c.CloseFunc()
	}
	return nil
}

var _ runtime.WSConn = (*WSConn)(nil)
