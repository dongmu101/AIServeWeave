package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
)

var errMessagesSetup = errors.New("Messages experiment requires a loopback IP and distinct nonempty keys")

func serveMessages(ctx context.Context, program, model, address string, out io.Writer) error {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	key := os.Getenv("AISW_CLAUDE_BRIDGE_KEY")
	second := os.Getenv("AISW_CLAUDE_BRIDGE_KEY_2")
	if err != nil || ip == nil || !ip.IsLoopback() || key == "" || len(key) > 512 || len(second) > 512 || key == second {
		return errMessagesSetup
	}
	if err := checkAuth(ctx, program, probeEnv(os.Environ())); err != nil {
		return err
	}
	keys := map[string]claudecode.Identity{key: {TenantID: "experiment-1", KeyID: "key-1"}}
	if second != "" {
		keys[second] = claudecode.Identity{TenantID: "experiment-2", KeyID: "key-2"}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errProbeSetup
	}
	defer listener.Close()
	bridge, closeBridge := claudecode.NewExperimentalHandler(program, model, keys, runtime.NewSystemClock())
	defer closeBridge()
	server := &http.Server{Handler: bridge, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if _, err := fmt.Fprintf(out, "Experimental Messages listener: http://%s (not registered in Agent/Gateway)\n", listener.Addr()); err != nil {
		_ = server.Close()
		<-done
		return errProbeSetup
	}
	select {
	case <-ctx.Done():
		_ = server.Close()
		<-done
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errProbeSetup
	}
}
