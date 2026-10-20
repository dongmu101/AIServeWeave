//go:build browser

package comfyeditor

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// TestEditorBrowser serves only the real editor build on an explicit loopback address.
// TestEditorBrowser 仅在指定回环地址提供真实编辑器产物。
func TestEditorBrowser(t *testing.T) {
	addr := os.Getenv("AISW_EDITOR_BROWSER_ADDR")
	if addr == "" {
		t.Skip("AISW_EDITOR_BROWSER_ADDR is required")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("browser address %q must be loopback", addr)
	}
	editor, err := New(os.Getenv("AISW_COMFY_BROWSER_DIST"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: editor, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		<-done
	}
}
