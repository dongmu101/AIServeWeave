// Command aiserveweave-registry runs the control-plane registry: it accepts
// node registration, tracks heartbeats and node state, and serves the current
// node and model inventory to the Gateway's scheduler.
//
// Only the process skeleton exists so far: the binary starts, logs its
// configuration and shuts down cleanly on a signal. The gRPC listener and the
// node state machine are still to be implemented.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	addr := flag.String("addr", ":9090", "address the gRPC listener will bind once implemented")
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		os.Stderr.WriteString("registry: " + err.Error() + "\n")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("registry started", slog.String("addr", *addr))
	logger.Warn("registry has no registration path yet; serving nothing")

	<-ctx.Done()
	logger.Info("registry stopped")
}
