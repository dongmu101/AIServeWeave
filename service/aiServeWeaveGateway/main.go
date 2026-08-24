// Command aiserveweave-gateway runs the data-plane gateway: it terminates the
// OpenAI-compatible API, schedules each request onto a node through the
// tunnel, and authenticates callers with a static API key list.
//
// Both sides are implemented: this binary listens for Agents over mutually
// authenticated TLS and tracks what each connected node can serve, and it
// binds an HTTP listener serving GET /v1/models, POST /v1/chat/completions
// (streaming and non-streaming) and POST /v1/embeddings, dispatching through
// a scheduler.Scheduler over that same tunnel state.
package main

import (
	"github.com/redis/go-redis/v9"

	"context"
	"crypto/tls"
	"crypto/x509"
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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/ratelimit"
	"AIServeWeave/service/aiServeWeaveGateway/registryclient"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// drainGrace is how long connected Agents are given to finish in-flight
// requests when this replica shuts down. It is generous on purpose: cutting a
// request short to shut down a second sooner trades a user's answer for
// nothing.
const drainGrace = 30 * time.Second

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("gateway: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	addr := flag.String("addr", ":8080", "address the HTTP API listener binds")
	apiKeys := flag.String("api-keys", "",
		"comma-separated API keys the HTTP API accepts as a Bearer token, used only when -control-plane-addr is empty; empty disables authentication (local use only)")
	controlPlaneAddr := flag.String("control-plane-addr", "",
		"control plane base URL for API key verification, e.g. http://127.0.0.1:8090; empty falls back to -api-keys")
	controlPlaneToken := flag.String("control-plane-token", "",
		"token presented to the control plane's internal endpoint; must match its InternalToken. Prefer AISW_CONTROL_PLANE_TOKEN over this flag, which is visible in a process listing")
	keyCacheTTL := flag.Duration("key-cache-ttl", controlplaneclient.DefaultCacheTTL,
		"how long a verified API key is trusted in process; this is the window a revoked key keeps working")
	tunnelAddr := flag.String("tunnel-addr", "", "address the tunnel listener binds, e.g. :8443; empty disables the tunnel")
	certFile := flag.String("tls-cert", "", "PEM certificate this replica presents to Agents")
	keyFile := flag.String("tls-key", "", "PEM private key for -tls-cert")
	clientCAFile := flag.String("client-ca", "", "PEM CA bundle that node certificates must chain to")
	replicaID := flag.String("replica-id", "", "identity announced to Agents; defaults to the hostname")
	registryAddr := flag.String("registry-addr", "", "Registry GatewayDirectory endpoint, host:port; empty leaves the roster to be set manually via SetRoster")
	registryCA := flag.String("registry-ca", "", "PEM CA bundle verifying the Registry's server certificate")
	advertiseAddr := flag.String("tunnel-advertise-addr", "", "address Agents should dial to reach this replica's tunnel listener; defaults to -tunnel-addr, which is wrong once NAT or a load balancer sits in front of it")
	redisAddr := flag.String("redis-addr", "",
		"Redis host:port for fleet-wide rate limiting; empty enforces per-replica, which admits the configured allowance once per replica")
	modelRoutes := flag.String("model-routes", "",
		"comma-separated files or directories of routing tables mapping logical model names onto deployments; empty passes model ids through unchanged")
	workflowTemplates := flag.String("workflow-templates", "",
		"comma-separated files or directories of ComfyUI workflow template manifests; empty registers none, and every workflow submit then 404s")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9090",
		"address the Prometheus /metrics listener binds; loopback by default because the exposition names every connected node, empty disables it")
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	id := *replicaID
	if id == "" {
		host, err := os.Hostname()
		if err != nil {
			return errors.New("no -replica-id and the hostname is unavailable: " + err.Error())
		}
		id = host
	}

	// One registry for the whole process: the tunnel server, the scheduler
	// and the front door measure three points on the same request path, and
	// splitting them across registries would mean three endpoints whose
	// numbers cannot be subtracted from one another.
	//
	// 整个进程共用一个注册表：隧道服务端、调度器与前门测的是同一条请求路径上的三个
	// 点，把它们拆到不同注册表里，就意味着三个彼此的数字无法相减的端点。
	registry := metrics.New(
		tunnelserver.Descriptions(),
		scheduler.Descriptions(),
		httpapi.Descriptions(),
	)

	server, err := tunnelserver.New(tunnelserver.Config{
		ReplicaID: id,
		Logger:    logger,
		Metrics:   registry,
		SlotHint: &tunnelv1.SlotHint{
			MinSlots:  2,
			MaxSlots:  8,
			BulkSlots: 1,
		},
	})
	if err != nil {
		return err
	}

	verifier, err := keyVerifier(*controlPlaneAddr, *controlPlaneToken, *keyCacheTTL, logger)
	if err != nil {
		return err
	}

	// Templates are loaded before anything starts serving: a manifest that
	// binds an input to a node it does not have is an operator mistake, and
	// failing here puts it on the operator's terminal instead of on a
	// caller's request an hour later.
	//
	// 模板在开始服务之前加载：把输入绑到不存在节点上的清单是运维的失误，在这里失败
	// 能把它摆在运维的终端上，而不是一小时后摆在某个调用方的请求上。
	workflows, err := workflow.Load(splitCommaList(*workflowTemplates)...)
	if err != nil {
		return err
	}
	logger.Info("workflow templates loaded", slog.Int("count", workflows.Len()))

	// Which limiter this replica gets is a deployment question, not a code
	// one: one replica enforces exactly either way, and several replicas only
	// enforce the configured allowance when they share Redis. Without it each
	// admits a full allowance of its own, which is recorded rather than
	// silently accepted — see the README.
	//
	// 本副本用哪个限流器是部署问题而不是代码问题：单副本两种方式都精确，而多副本只有
	// 共享 Redis 时才真正执行配置的额度。没有它时每个副本各放行一份完整额度，这一点
	// 被记录下来而不是默默接受——见 README。
	limiter, err := rateLimiter(*redisAddr, logger)
	if err != nil {
		return err
	}

	// Routes are loaded before anything serves: a table naming a target with
	// no runtime model is an operator mistake, and failing here puts it on
	// their terminal instead of on a caller's request an hour later.
	//
	// 路由表在开始服务之前加载：一张 target 没有运行时模型的表是运维的失误，在这里
	// 失败能把它摆在他们的终端上，而不是一小时后摆在某个调用方的请求上。
	table, err := routing.Load(splitCommaList(*modelRoutes)...)
	if err != nil {
		return err
	}
	logger.Info("model routes loaded", slog.Int("aliases", table.Len()))

	sched := scheduler.New(server, scheduler.Config{Metrics: registry, Routes: table})
	httpServer := &http.Server{
		Addr: *addr,
		Handler: httpapi.New(sched, httpapi.Config{
			Verifier:  verifier,
			APIKeys:   splitCommaList(*apiKeys),
			Logger:    logger,
			Metrics:   registry,
			Workflows: workflows,
			Limiter:   limiter,
		}),
	}
	httpServeErr := make(chan error, 1)
	go func() { httpServeErr <- httpServer.ListenAndServe() }()
	logger.Info("gateway started", slog.String("addr", *addr), slog.String("replica_id", id))

	// The metrics listener's failure is logged rather than returned: losing
	// observability is bad, and taking a serving Gateway down over it would
	// be worse.
	//
	// 指标监听器的失败只记录、不返回：失去可观测性很糟，但为此让一个正在服务的
	// Gateway 停机更糟。
	var metricsServer *http.Server
	if *metricsAddr == "" {
		logger.Warn("no -metrics-addr; this replica exports no metrics")
	} else {
		metricsServer = registry.Server(*metricsAddr)
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics listener stopped", slog.Any("error", err))
			}
		}()
		logger.Info("metrics listening", slog.String("metrics_addr", *metricsAddr))
	}

	var (
		grpcServer *grpc.Server
		tunnelLis  net.Listener
		tunnelErr  = make(chan error, 1)
	)
	if *tunnelAddr == "" {
		logger.Warn("no -tunnel-addr; no Agent can connect to this replica")
	} else {
		creds, err := tunnelCredentials(*certFile, *keyFile, *clientCAFile)
		if err != nil {
			return err
		}
		tunnelLis, err = net.Listen("tcp", *tunnelAddr)
		if err != nil {
			return err
		}
		grpcServer = grpc.NewServer(
			grpc.Creds(creds),
			// A home network's NAT ages an idle mapping out in minutes. The
			// transport-level ping keeps the mapping alive from this side
			// too, so a silent tunnel does not quietly stop existing.
			grpc.KeepaliveParams(keepalive.ServerParameters{
				Time:    20 * time.Second,
				Timeout: 10 * time.Second,
			}),
			grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
				MinTime:             10 * time.Second,
				PermitWithoutStream: true,
			}),
		)
		tunnelv1.RegisterTunnelServer(grpcServer, server)
		go func() { tunnelErr <- grpcServer.Serve(tunnelLis) }()
		logger.Info("tunnel listening",
			slog.String("replica_id", id),
			slog.String("tunnel_addr", tunnelLis.Addr().String()))
	}

	registryErr := make(chan error, 1)
	if *registryAddr != "" {
		endpoint := *advertiseAddr
		if endpoint == "" {
			endpoint = *tunnelAddr
		}
		go func() {
			registryErr <- registryclient.Run(ctx, registryclient.Config{
				Addr:      *registryAddr,
				CAFile:    *registryCA,
				ReplicaID: id,
				Endpoint:  endpoint,
				Logger:    logger,
			}, server)
		}()
		logger.Info("joining registry roster", slog.String("registry_addr", *registryAddr))
	} else {
		logger.Warn("no -registry-addr; the replica roster must be set manually via Server.SetRoster")
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connected nodes")
	case err := <-httpServeErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case err := <-tunnelErr:
		if err != nil {
			return err
		}
	case err := <-registryErr:
		if err != nil {
			return err
		}
	}

	// The HTTP API is shut down first, and given the tunnel's own drain
	// grace to finish in-flight requests: those requests are still running
	// on the tunnel, so closing the tunnel first would cut them short
	// instead of letting them complete.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainGrace)
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP API did not shut down within the grace period", slog.Any("error", err))
	}
	cancel()

	if grpcServer != nil {
		// Ask the Agents to leave first, then stop accepting: GracefulStop
		// alone would wait for streams that are long-lived by design and
		// never end on their own.
		server.Close("gateway replica shutting down", drainGrace)
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(drainGrace):
			logger.Warn("nodes did not disconnect within the grace period; closing the listener")
			grpcServer.Stop()
			<-stopped
		}
	}

	// The metrics endpoint is closed last, so a scrape landing during the
	// drain still sees the node count going to zero rather than a refused
	// connection.
	//
	// 指标端点最后关闭，这样落在排空期间的一次抓取仍能看到节点数归零，而不是一个被
	// 拒绝的连接。
	if metricsServer != nil {
		_ = metricsServer.Close()
	}

	logger.Info("gateway stopped")
	return nil
}

// controlPlaneTokenEnv is where the control plane token is read from when the
// flag is empty. An environment variable is not secret either, but it does not
// appear in `ps` output the way a flag does, which is the difference between a
// secret readable by the operator and one readable by every user on the host.
//
// controlPlaneTokenEnv 是 flag 为空时读取控制面 token 的来源。环境变量同样算不上
// 保密，但它不像 flag 那样出现在 `ps` 输出里——这正是「运维可读的秘密」与「主机上
// 每个用户都可读的秘密」之间的区别。
const controlPlaneTokenEnv = "AISW_CONTROL_PLANE_TOKEN"

// keyVerifier builds the control plane verifier, or returns nil when no
// control plane is configured, in which case the static -api-keys list is used.
//
// keyVerifier 构建控制面校验器；未配置控制面时返回 nil，此时使用静态的 -api-keys
// 列表。
func keyVerifier(addr, token string, cacheTTL time.Duration, logger *slog.Logger) (httpapi.KeyVerifier, error) {
	if addr == "" {
		return nil, nil
	}
	if token == "" {
		token = os.Getenv(controlPlaneTokenEnv)
	}
	verifier, err := controlplaneclient.New(controlplaneclient.Config{
		Endpoint: addr,
		Token:    token,
		CacheTTL: cacheTTL,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("verifying API keys against the control plane",
		slog.String("control_plane_addr", addr),
		slog.Duration("key_cache_ttl", cacheTTL))
	return verifier, nil
}

// splitCommaList parses a comma-separated flag value, trimming whitespace
// and dropping empty entries.
// rateLimiter builds the quota limiter this replica enforces with.
//
// rateLimiter 构造本副本用于执行配额的限流器。
func rateLimiter(redisAddr string, logger *slog.Logger) (ratelimit.Limiter, error) {
	if redisAddr == "" {
		logger.Warn("no -redis-addr: rate limits are enforced per replica, so a fleet of N replicas admits N times each tenant's configured allowance")
		return ratelimit.NewMemory(ratelimit.MemoryConfig{}), nil
	}
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	limiter, err := ratelimit.NewRedis(ratelimit.RedisConfig{Client: client})
	if err != nil {
		return nil, err
	}
	logger.Info("rate limits are enforced fleet-wide", slog.String("redis_addr", redisAddr))
	return limiter, nil
}

func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// tunnelCredentials builds the replica's mTLS configuration. All three files
// are required: a tunnel listener without client verification would
// authenticate nobody, since the server reads the node identity only from a
// verified certificate chain.
func tunnelCredentials(certFile, keyFile, clientCAFile string) (credentials.TransportCredentials, error) {
	if certFile == "" || keyFile == "" || clientCAFile == "" {
		return nil, errors.New("-tunnel-addr requires -tls-cert, -tls-key and -client-ca: without client verification no Agent can be authenticated")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificate found in " + clientCAFile)
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}), nil
}
