package runtimetest_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/runtimetest"
)

func TestWSDialerDefaultErrorsWithoutDialFunc(t *testing.T) {
	d := &runtimetest.WSDialer{}
	if _, err := d.Dial(context.Background(), "ws://example", nil); err == nil {
		t.Fatal("expected an error when DialFunc is not configured")
	}
}

func TestWSDialerHonorsDialFunc(t *testing.T) {
	conn := &runtimetest.WSConn{}
	var gotURL string
	d := &runtimetest.WSDialer{
		DialFunc: func(ctx context.Context, url string, header http.Header) (runtime.WSConn, error) {
			gotURL = url
			return conn, nil
		},
	}

	got, err := d.Dial(context.Background(), "ws://example/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != conn {
		t.Fatal("Dial() did not return the configured connection")
	}
	if gotURL != "ws://example/ws" {
		t.Fatalf("Dial() url = %q, want ws://example/ws", gotURL)
	}
}

func TestWSConnDefaultReadReturnsEOF(t *testing.T) {
	c := &runtimetest.WSConn{}
	_, _, err := c.Read(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("default Read() error = %v, want io.EOF", err)
	}
}

func TestWSConnHonorsReadAndCloseFuncs(t *testing.T) {
	closed := false
	c := &runtimetest.WSConn{
		ReadFunc: func(ctx context.Context) (int, []byte, error) {
			return 1, []byte("hello"), nil
		},
		CloseFunc: func() error {
			closed = true
			return nil
		},
	}

	mt, data, err := c.Read(context.Background())
	if err != nil || mt != 1 || string(data) != "hello" {
		t.Fatalf("Read() = %d, %q, %v, want 1, hello, nil", mt, data, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("Close() did not invoke CloseFunc")
	}
}
