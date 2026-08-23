// Command aiserveweave-agent runs the node-side agent: it manages the local
// inference runtimes (Ollama, vLLM, SGLang, ComfyUI) and will dial out to the
// Gateway over the tunnel. The agent never listens on a public port.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/ollama"
	"AIServeWeave/common/runtime/sglang"
	"AIServeWeave/common/runtime/vllm"
	"AIServeWeave/common/runtime/workflow/comfyui"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

const (
	// agentVersion is reported in the tunnel handshake and to the Registry.
	// It is a constant until the build stamps a real version in.
	agentVersion = "dev"

	// shutdownTimeout bounds how long the agent waits for in-flight runtime
	// work to wind down before exiting anyway.
	shutdownTimeout = 15 * time.Second

	// dialTimeout and responseHeaderTimeout bound how long the agent waits to
	// reach a local backend and to see its response headers. They are set on
	// the transport rather than as http.Client.Timeout on purpose: a client
	// deadline also covers reading the body, which would truncate long-lived
	// SSE streams. Per-request deadlines come from the caller's context.
	dialTimeout           = 10 * time.Second
	responseHeaderTimeout = 60 * time.Second
)

func main() {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	opts := registerTunnelFlags()
	ollamaURL := flag.String("ollama-url", "",
		"base URL of a local Ollama instance to register, e.g. http://127.0.0.1:11434; empty registers no runtime")
	ollamaID := flag.String("ollama-id", "ollama", "runtime id to register the Ollama instance under")
	flag.Parse()

	logger, err := newLogger(*logLevel)
	if err != nil {
		// The logger is not usable yet, so report the failure directly.
		os.Stderr.WriteString("agent: " + err.Error() + "\n")
		os.Exit(2)
	}

	if err := run(logger, opts, *ollamaURL, *ollamaID); err != nil {
		logger.Error("agent exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// tunnelOptions is the tunnel's configuration. It comes from flags because
// the agent has no configuration file yet; the `tunnel` section of the file
// described in tunnel/README.md replaces this wholesale once it lands, and
// the flag names deliberately mirror that section's keys.
type tunnelOptions struct {
	gateway         string
	registry        string
	nodeID          string
	certFile        string
	keyFile         string
	caFile          string
	bootstrapToken  string
	allowedRuntimes string
	maxGateways     int
}

func registerTunnelFlags() *tunnelOptions {
	opts := &tunnelOptions{}
	flag.StringVar(&opts.gateway, "gateway", "",
		"comma-separated seed list of gateway replicas as host:port; empty disables the tunnel. "+
			"Only one has to be reachable: the roster supplies the rest")
	flag.IntVar(&opts.maxGateways, "max-gateways", 0, "upper bound on simultaneous gateway tunnels (default 16)")
	flag.StringVar(&opts.registry, "registry", "", "registry endpoint for node certificate issuance, as host:port")
	flag.StringVar(&opts.nodeID, "node-id", "", "node identity; empty lets the registry assign one")
	flag.StringVar(&opts.certFile, "cert-file", "", "node certificate path, mode 0600")
	flag.StringVar(&opts.keyFile, "key-file", "", "node private key path, mode 0600")
	flag.StringVar(&opts.caFile, "ca-file", "", "registry CA bundle path")
	flag.StringVar(&opts.bootstrapToken, "bootstrap-token-file", "", "one-time registration token path, deleted after use")
	flag.StringVar(&opts.allowedRuntimes, "allowed-runtimes", "",
		"comma-separated runtime ids the gateway may dispatch to; empty means every configured runtime")
	return opts
}

// enabled reports whether the operator asked for a tunnel at all.
func (o *tunnelOptions) enabled() bool { return len(o.seeds()) > 0 }

// seeds parses the seed endpoint list. The roster replaces it as soon as one
// replica answers, so it only has to contain one address that works.
func (o *tunnelOptions) seeds() []string {
	var endpoints []string
	for _, endpoint := range strings.Split(o.gateway, ",") {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

// runtimeIDs parses the allowlist. An empty list means no local narrowing,
// which is only safe because the agent currently configures no runtimes at
// all; once runtimes are loaded from the config file, main must pass every
// configured id here so the allowlist is a real allowlist.
func (o *tunnelOptions) runtimeIDs() []string {
	var ids []string
	for _, id := range strings.Split(o.allowedRuntimes, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// run wires the runtime registry and manager, then blocks until the process is
// signalled to stop. It returns the first error that prevents a clean start or
// a clean shutdown.
//
// ollamaURL and ollamaID are a stand-in for the runtime section of the agent
// config file described in tunnel/README.md: until that file lands, this is
// the only way to give the agent a real backend to dispatch to. An empty
// ollamaURL registers nothing, matching today's behavior.
func run(logger *slog.Logger, opts *tunnelOptions, ollamaURL, ollamaID string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry, err := newRegistry()
	if err != nil {
		return err
	}

	deps := newDependencies(logger)
	manager := runtime.NewManager(registry, deps)

	configuredRuntimes := 0
	if ollamaURL != "" {
		if err := manager.Add(ctx, runtime.Config{ID: ollamaID, Kind: runtime.KindOllama, BaseURL: ollamaURL}); err != nil {
			return err
		}
		configuredRuntimes = 1
	}

	// Runtime configuration is not loaded from disk yet, so beyond the
	// Ollama instance above the manager starts with no instances. Declared
	// runtimes will be added here once the agent config file lands.
	logger.Info("agent started",
		slog.Any("supported_kinds", registry.Kinds()),
		slog.Int("configured_runtimes", configuredRuntimes),
		slog.Bool("tunnel_enabled", opts.enabled()),
	)

	tunnelErr, err := startTunnel(ctx, logger, manager, deps.Metrics, opts)
	if err != nil {
		return err
	}

	// Whichever comes first: the operator stopping the agent, or the tunnel
	// giving up for a reason no reconnect can fix. Either way the runtimes are
	// drained the same way, so a fatal tunnel error is carried past the
	// shutdown rather than short-circuiting it.
	var fatal error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining runtimes")
	case err := <-tunnelErr:
		stop()
		if err != nil && !errors.Is(err, tunnel.ErrShutdownRequested) {
			logger.Error("tunnel failed permanently", slog.Any("error", err))
			fatal = err
		} else {
			logger.Info("tunnel closed; shutting down")
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := manager.Close(shutdownCtx); err != nil {
		return err
	}
	if fatal != nil {
		return fatal
	}

	logger.Info("agent stopped")
	return nil
}

// startTunnel wires the connection table and runs it in the background,
// returning the channel its outcome arrives on. A nil channel is returned
// when no gateway is configured, which blocks forever in the select above and
// leaves the agent running its runtimes only.
//
// The seed endpoints only have to get the agent to one reachable replica: the
// roster that comes back over that tunnel supplies the rest, so scaling the
// Gateway out never means restarting an agent.
//
// metrics is the same sink the runtime layer records against: a node has one
// metrics backend, not one per layer. Today it discards, so wiring a real one
// is a change in exactly one place.
func startTunnel(ctx context.Context, logger *slog.Logger, manager runtime.Manager, metrics runtime.Metrics, opts *tunnelOptions) (<-chan error, error) {
	if !opts.enabled() {
		return nil, nil
	}

	caPEM, err := os.ReadFile(opts.caFile)
	if err != nil {
		return nil, err
	}
	connector, err := tunnel.NewGRPCRegistryConnector(opts.registry, caPEM)
	if err != nil {
		return nil, err
	}
	identities, err := tunnel.NewIdentityManager(tunnel.IdentityConfig{
		NodeID:             opts.nodeID,
		RegistryEndpoint:   opts.registry,
		CertFile:           opts.certFile,
		KeyFile:            opts.keyFile,
		CAFile:             opts.caFile,
		BootstrapTokenFile: opts.bootstrapToken,
		AgentVersion:       agentVersion,
		Logger:             logger,
	}, connector)
	if err != nil {
		return nil, err
	}

	// The identity is obtained before the first dial: a node with no valid
	// certificate has nothing to say to a gateway, and failing here gives the
	// operator a clear error instead of an authentication loop. The
	// connection table keeps it rotated from then on.
	identity, err := identities.Ensure(ctx)
	if err != nil {
		return nil, err
	}

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         manager,
		NodeID:          identity.NodeID,
		AllowedRuntimes: opts.runtimeIDs(),
		Metrics:         metrics,
		Logger:          logger,
	})
	if err != nil {
		return nil, err
	}

	tunnels, err := tunnel.NewManager(tunnel.ManagerConfig{
		Client: tunnel.ClientConfig{
			NodeID:          identity.NodeID,
			AgentVersion:    agentVersion,
			Manager:         manager,
			AllowedRuntimes: opts.runtimeIDs(),
			Handler:         dispatcher,
			Metrics:         metrics,
			Logger:          logger,
		},
		SeedEndpoints: opts.seeds(),
		MaxGateways:   opts.maxGateways,
		Identities:    identities,
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- tunnels.Run(ctx) }()
	return done, nil
}

// newRegistry returns a runtime registry with every supported inference
// backend registered.
func newRegistry() (runtime.Registry, error) {
	registry := runtime.NewRegistry()
	factories := map[runtime.Kind]runtime.Factory{
		runtime.KindOllama:  ollama.New,
		runtime.KindVLLM:    vllm.New,
		runtime.KindSGLang:  sglang.New,
		runtime.KindComfyUI: comfyui.New,
	}
	for kind, factory := range factories {
		if err := registry.Register(kind, factory); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// newDependencies builds the production dependency set: the real wall clock,
// a shared HTTP client and the WebSocket dialer the ComfyUI adapter needs.
func newDependencies(logger *slog.Logger) runtime.Dependencies {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: dialTimeout}).DialContext
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	httpClient := &http.Client{Transport: transport}
	return runtime.Dependencies{
		HTTPClient: httpClient,
		WSDialer:   comfyui.NewDialer(httpClient),
		Clock:      runtime.NewSystemClock(),
		Logger:     logger,
		Metrics:    nopMetrics{},
	}
}

// newLogger returns a JSON logger at the requested level.
func newLogger(level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), nil
}

// nopMetrics satisfies runtime.Metrics while the agent has no metrics backend
// wired up. Every instrument it hands out discards its samples.
type nopMetrics struct{}

func (nopMetrics) Counter(string, map[string]string) runtime.Counter     { return nopInstrument{} }
func (nopMetrics) Gauge(string, map[string]string) runtime.Gauge         { return nopInstrument{} }
func (nopMetrics) Histogram(string, map[string]string) runtime.Histogram { return nopInstrument{} }

// nopInstrument discards every sample recorded against it.
type nopInstrument struct{}

func (nopInstrument) Add(float64)     {}
func (nopInstrument) Set(float64)     {}
func (nopInstrument) Observe(float64) {}
