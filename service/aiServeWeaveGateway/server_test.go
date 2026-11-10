package main

import (
	"net/http"
	"testing"
)

// TestNewHTTPServerBoundsIdleConnectionPhases keeps every listener this
// binary starts protected against trickled headers and idle keep-alive
// connections. WriteTimeout stays unset on purpose: it would cut off long
// streams, which httpapi bounds per write instead.
func TestNewHTTPServerBoundsIdleConnectionPhases(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, want a positive bound", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout = %v, want a positive bound", srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want unset so long streams are not cut off", srv.WriteTimeout)
	}
}
