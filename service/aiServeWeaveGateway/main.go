// Command aiserveweave-gateway runs the data-plane gateway: it terminates the
// OpenAI-compatible API, schedules each request onto a node through the
// tunnel, and authenticates callers with a static API key list.
//
// Both sides are implemented: this binary listens for Agents over mutually
// authenticated TLS and tracks what each connected node can serve, and it
// binds an HTTP listener serving GET /v1/models, POST /v1/chat/completions
// (streaming and non-streaming) and POST /v1/embeddings, dispatching through
// a scheduler.Scheduler over that same tunnel state. The same listener also
// serves GET /docs (a vendored Swagger UI) and GET /openapi.yaml, unauthenticated,
// describing every route this binary and the operator listener expose.
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
	"AIServeWeave/service/aiServeWeaveGateway/adminapi"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
	"AIServeWeave/service/aiServeWeaveGateway/ratelimit"
	"AIServeWeave/service/aiServeWeaveGateway/registryclient"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// drainGrace is how long connected Agents are given to finish in-flight
// requests when this replica shuts down. It is generous on purpose: cutting a
// request short to shut down a second sooner trades a user's answer for
// nothing.
const drainGrace = 30 * time.Second

// version is stamped at build time via -ldflags="-X main.version=...", see
// the root Dockerfile and scripts/build-release.sh; "dev" is what a plain
// `go build` produces.
//
// version 在构建时通过 -ldflags="-X main.version=..." 注入，见根 Dockerfile 与
// scripts/build-release.sh；直接 `go build` 得到的就是 "dev"。
var version = "dev"

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
	registryJoinTokenFile := flag.String("registry-join-token-file", "",
		"path to the shared secret GatewayDirectory.Join presents to the Registry (STATUS.md's S03); empty omits it, which only works when the Registry has no -gateway-token-file configured")
	advertiseAddr := flag.String("tunnel-advertise-addr", "", "address Agents should dial to reach this replica's tunnel listener; defaults to -tunnel-addr, which is wrong once NAT or a load balancer sits in front of it")
	redisAddr := flag.String("redis-addr", "",
		"Redis host:port for fleet-wide rate limiting; empty enforces per-replica, which admits the configured allowance once per replica")
	routeSource := flag.String("route-source", "file", "model route source: file or controlplane")
	routeStateFile := flag.String("route-state-file", "", "durable last-good route snapshot; required in controlplane mode")
	routeSyncInterval := flag.Duration("route-sync-interval", 30*time.Second, "managed route polling interval")
	modelRoutes := flag.String("model-routes", "",
		"comma-separated files or directories of routing tables mapping logical model names onto deployments; empty passes model ids through unchanged")
	workflowSource := flag.String("workflow-source", "file", "workflow template source: file or controlplane")
	workflowStateFile := flag.String("workflow-state-file", "", "durable last-good workflow template bundle; required in controlplane mode")
	workflowSyncInterval := flag.Duration("workflow-sync-interval", 30*time.Second, "managed workflow template polling interval")
	workflowTemplates := flag.String("workflow-templates", "",
		"comma-separated files or directories of ComfyUI workflow template manifests; empty registers none, and every workflow submit then 404s")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9090",
		"address the Prometheus /metrics listener binds; loopback by default because the exposition names every connected node, empty disables it")
	adminAddr := flag.String("admin-addr", "",
		"address the operator inventory listener binds, e.g. 127.0.0.1:8091; empty disables it. Its token comes from AISW_GATEWAY_ADMIN_TOKEN")
	artifactStorageKind := flag.String("artifact-storage", "",
		"generated artifact storage backend (STATUS.md's P04): local, s3, webdav, or empty to disable byte persistence — artifacts then remain pull-only from the node that produced them, today's pre-P04 behavior")
	artifactStorageLocalDir := flag.String("artifact-storage-local-dir", "", "directory for -artifact-storage=local")
	artifactStorageS3Endpoint := flag.String("artifact-storage-s3-endpoint", "",
		"custom endpoint for -artifact-storage=s3, e.g. a MinIO/Ceph RGW/NAS S3 gateway URL; empty targets AWS itself")
	artifactStorageS3Region := flag.String("artifact-storage-s3-region", "",
		"region for -artifact-storage=s3; most non-AWS S3-compatible servers ignore this but the SDK requires some value to sign with")
	artifactStorageS3Bucket := flag.String("artifact-storage-s3-bucket", "", "bucket for -artifact-storage=s3")
	artifactStorageS3Prefix := flag.String("artifact-storage-s3-prefix", "", "key prefix for -artifact-storage=s3, so one bucket can be shared across purposes or environments")
	artifactStorageS3PathStyle := flag.Bool("artifact-storage-s3-path-style", false,
		"use path-style addressing for -artifact-storage=s3; required by most non-AWS S3-compatible servers (MinIO, Ceph RGW)")
	artifactStorageS3AccessKeyIDFile := flag.String("artifact-storage-s3-access-key-id-file", "", "path to a file holding the S3 access key id for -artifact-storage=s3")
	artifactStorageS3SecretAccessKeyFile := flag.String("artifact-storage-s3-secret-access-key-file", "", "path to a file holding the S3 secret access key for -artifact-storage=s3")
	artifactStorageWebDAVURL := flag.String("artifact-storage-webdav-url", "",
		"WebDAV server root for -artifact-storage=webdav, e.g. https://nas.example.internal/webdav — the protocol most home/office NAS boxes expose even without an S3 gateway")
	artifactStorageWebDAVDir := flag.String("artifact-storage-webdav-dir", "", "path prefix under -artifact-storage-webdav-url, so one WebDAV share can be split across purposes or environments")
	artifactStorageWebDAVUsernameFile := flag.String("artifact-storage-webdav-username-file", "", "path to a file holding the WebDAV username for -artifact-storage=webdav")
	artifactStorageWebDAVPasswordFile := flag.String("artifact-storage-webdav-password-file", "", "path to a file holding the WebDAV password for -artifact-storage=webdav")
	artifactCopyTimeout := flag.Duration("artifact-copy-timeout", 0,
		"bound on one artifact's node-to-storage byte copy; zero uses httpapi.DefaultArtifactCopyTimeout, sized for moving real file bytes rather than a JSON round trip")
	artifactCleanupInterval := flag.Duration("artifact-cleanup-interval", 0,
		"how often the artifact retention sweep runs (STATUS.md's P04); zero uses httpapi.DefaultArtifactCleanupInterval. Only runs when a control plane is configured (-control-plane-addr), the same as job persistence")
	artifactRetention := flag.Duration("artifact-retention", 0,
		"how long a persisted \"output\" artifact is kept before the cleanup sweep reaps it; zero uses httpapi.DefaultArtifactRetention")
	artifactPreviewRetention := flag.Duration("artifact-preview-retention", 0,
		"how long a persisted \"temp\" (preview/intermediate) artifact is kept, deliberately much shorter than -artifact-retention; zero uses httpapi.DefaultArtifactPreviewRetention")
	allowedUploadExtensions := flag.String("workflow-upload-allowed-extensions", "",
		"comma-separated, dot-prefixed file extensions a workflow InputFile upload may use (STATUS.md's P04); empty uses httpapi.DefaultAllowedUploadExtensions, there is no way to disable the check")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		os.Stdout.WriteString("aiserveweave-gateway " + version + "\n")
		return nil
	}

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

	// The Job persistence/recovery adapter shares -control-plane-addr and the
	// same token as key verification above — all three are this replica
	// talking to the same control plane about its own callers' business
	// (STATUS.md's J04/J05/J06). A deployment with no control plane
	// configured gets Gateway-local job tracking only, the same degrade
	// keyVerifier already returns nil for.
	//
	// Job 持久化/恢复适配器与上面的 key 校验共用 -control-plane-addr 与同一个
	// token——三者都是本副本就自己调用方的业务在与同一个控制面对话
	// （STATUS.md 的 J04/J05/J06）。未配置控制面的部署只得到仅限 Gateway 本地的
	// job 跟踪，与 keyVerifier 已经为此返回 nil 的退化相同。
	jobPersistence, err := jobPersistenceAdapter(*controlPlaneAddr, *controlPlaneToken, logger)
	if err != nil {
		return err
	}

	// Templates are loaded (file mode) or pulled from a validated cache
	// (controlplane mode, P03) before anything starts serving: a manifest
	// that binds an input to a node it does not have is an operator mistake,
	// and failing here puts it on the operator's terminal instead of on a
	// caller's request an hour later.
	//
	// 模板在开始服务之前加载（文件模式）或从已校验缓存拉取（controlplane 模式，
	// P03）：把输入绑到不存在节点上的清单是运维的失误，在这里失败能把它摆在运维的
	// 终端上，而不是一小时后摆在某个调用方的请求上。
	workflowStatus, workflowHandle, workflowSyncer, err := configureWorkflows(ctx, *workflowSource, *workflowTemplates, *workflowStateFile, *controlPlaneAddr, *controlPlaneToken, *workflowSyncInterval)
	if err != nil {
		return err
	}
	logger.Info("workflow templates loaded", slog.Int("count", workflowHandle.Len()))

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

	sched := scheduler.New(server, scheduler.Config{Metrics: registry})
	routeStatus, routeSyncer, err := configureRoutes(ctx, *routeSource, *modelRoutes, *routeStateFile, *controlPlaneAddr, *controlPlaneToken, *routeSyncInterval, sched.SetRoutes)
	if err != nil {
		return err
	}
	if routeSyncer != nil {
		syncCtx, stopSync := context.WithCancel(ctx)
		syncDone := make(chan struct{})
		go func() { defer close(syncDone); routeSyncer.Run(syncCtx) }()
		defer func() { stopSync(); <-syncDone }()
	}
	if workflowSyncer != nil {
		syncCtx, stopSync := context.WithCancel(ctx)
		syncDone := make(chan struct{})
		go func() { defer close(syncDone); workflowSyncer.Run(syncCtx) }()
		defer func() { stopSync(); <-syncDone }()
	}

	artifactStorage, err := buildArtifactStorage(*artifactStorageKind, *artifactStorageLocalDir,
		*artifactStorageS3Endpoint, *artifactStorageS3Region, *artifactStorageS3Bucket, *artifactStorageS3Prefix, *artifactStorageS3PathStyle,
		*artifactStorageS3AccessKeyIDFile, *artifactStorageS3SecretAccessKeyFile,
		*artifactStorageWebDAVURL, *artifactStorageWebDAVDir, *artifactStorageWebDAVUsernameFile, *artifactStorageWebDAVPasswordFile)
	if err != nil {
		return err
	}

	httpCfg := httpapi.Config{
		Verifier:                 verifier,
		APIKeys:                  splitCommaList(*apiKeys),
		Logger:                   logger,
		Metrics:                  registry,
		Workflows:                workflowHandle,
		Limiter:                  limiter,
		ArtifactStorage:          artifactStorage,
		ArtifactCopyTimeout:      *artifactCopyTimeout,
		ArtifactCleanupInterval:  *artifactCleanupInterval,
		ArtifactRetention:        *artifactRetention,
		ArtifactPreviewRetention: *artifactPreviewRetention,
		AllowedUploadExtensions:  splitCommaList(*allowedUploadExtensions),
	}
	// jobPersistence is assigned to both interface-typed fields only when it
	// is genuinely non-nil: httpapi.Config's fields are interfaces, and
	// assigning a nil *GatewayPersister to them directly would box a
	// non-nil interface holding a nil pointer — see jobPersistenceAdapter's
	// own doc comment for why that trap matters here.
	//
	// jobPersistence 只在它确实非 nil 时才被赋给这两个接口类型字段：
	// httpapi.Config 的这些字段是接口，直接赋值一个 nil *GatewayPersister
	// 会装箱出一个「非 nil 接口持有 nil 指针」——这个陷阱为何要紧，见
	// jobPersistenceAdapter 自己的文档注释。
	if jobPersistence != nil {
		httpCfg.JobPersistClient = jobPersistence
		httpCfg.JobRecoveryClient = jobPersistence
		httpCfg.ArtifactCleanupClient = jobPersistence
	}
	front := httpapi.New(sched, httpCfg)

	// The docs routes are registered on a mux that wraps front rather than
	// inside httpapi: they describe the API and answer to nobody, so they
	// must not sit behind the API key check front's own routes require.
	//
	// 文档路由注册在包着 front 的 mux 上，而不是 httpapi 内部：它们描述的是这个 API、
	// 不对任何人认证，因此不能落在 front 自己那些路由所要求的 API Key 校验之后。
	topMux := http.NewServeMux()
	if err := mountDocs(topMux); err != nil {
		return err
	}
	topMux.Handle("/", front)
	httpServer := &http.Server{Addr: *addr, Handler: topMux}
	httpServeErr := make(chan error, 1)
	go func() { httpServeErr <- httpServer.ListenAndServe() }()
	logger.Info("gateway started", slog.String("addr", *addr), slog.String("replica_id", id))

	// The operator inventory listener is off unless an address is given, and
	// it refuses to start without a token: a fleet inventory is not something
	// to serve unauthenticated because a variable was unset. Its failure is
	// returned rather than logged, unlike the metrics listener's — an
	// operator who asked for this listener and did not get it should find out
	// at startup, not when a console shows an empty fleet.
	//
	// 运维清单监听器在未给出地址时不启用，且没有 token 时拒绝启动：机群清单不该因为
	// 某个变量没设置就以未认证的方式提供出去。它的失败是返回而不是记录日志，这与指标
	// 监听器不同——一个要求启用它却没能启用的运维，应当在启动时就发现，而不是在控制台
	// 显示出一个空机群时才发现。
	var adminServer *http.Server
	if *adminAddr == "" {
		logger.Info("no -admin-addr; this replica serves no operator inventory")
	} else {
		adminHandler, err := adminapi.New(adminapi.Config{
			Token:     os.Getenv("AISW_GATEWAY_ADMIN_TOKEN"),
			Routes:    routeStatus,
			Workflows: workflowStatus,
			Nodes:     server.Nodes,
			Jobs:      front.JobsFor,
			Templates: front.Templates,
			ReplicaID: id,
		})
		if err != nil {
			return err
		}
		// The port is bound here, before anything is announced, so a bind
		// failure is returned from run() rather than logged next to a line
		// claiming the listener is up. ListenAndServe in a goroutine cannot
		// do that: it binds after the caller has moved on, and an address
		// already in use becomes a log line under a "listening" one.
		//
		// 端口在这里绑定，早于任何宣告，因此绑定失败是从 run() 返回，而不是被记在一行
		// 声称监听器已就绪的日志旁边。在协程里调用 ListenAndServe 做不到这一点：它在
		// 调用方已经继续往下走之后才绑定，于是「地址已被占用」变成了一条压在
		// 「listening」下面的日志。
		adminListener, err := net.Listen("tcp", *adminAddr)
		if err != nil {
			return err
		}
		adminServer = &http.Server{Handler: adminHandler}
		go func() {
			if err := adminServer.Serve(adminListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("operator inventory listener stopped", slog.Any("error", err))
			}
		}()
		logger.Info("operator inventory listening", slog.String("admin_addr", adminListener.Addr().String()))
	}

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
		registryJoinToken, err := loadSecretFile(*registryJoinTokenFile)
		if err != nil {
			return err
		}
		go func() {
			registryErr <- registryclient.Run(ctx, registryclient.Config{
				Addr:         *registryAddr,
				CAFile:       *registryCA,
				ReplicaID:    id,
				Endpoint:     endpoint,
				GatewayToken: registryJoinToken,
				Logger:       logger,
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

	// The background job syncer is stopped before the tunnel: it dispatches
	// through the same scheduler, and stopping it here avoids a burst of
	// "node is not connected" warnings against a tunnel that is closing on
	// purpose rather than one that failed.
	//
	// 后台 job 同步器在隧道之前停止：它经由同一个调度器分派，在这里停止它能避免
	// 对着一条正在有意关闭而非故障的隧道打出一串「node is not connected」告警。
	front.Close()

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
	if adminServer != nil {
		_ = adminServer.Close()
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

// jobPersistenceAdapter builds the Gateway's side of the control plane's Job
// persistence and recovery API, or returns nil when no control plane is
// configured — mirroring keyVerifier's own degrade path.
//
// It returns the concrete *controlplaneclient.GatewayPersister rather than
// either interface it satisfies (httpapi.JobPersistClient,
// httpapi.JobRecoveryClient), so the call site can nil-check it once before
// assigning it to both httpapi.Config fields. Returning an interface type
// instead would risk the classic trap: a nil *GatewayPersister boxed into an
// interface is not itself a nil interface, so httpapi.New's own
// `cfg.JobPersistClient != nil` check would wrongly read true for a Gateway
// that configured no control plane at all.
//
// jobPersistenceAdapter 构建 Gateway 一侧的控制面 Job 持久化与恢复 API 客户端；
// 未配置控制面时返回 nil——与 keyVerifier 自己的退化路径一致。
//
// 它返回具体的 *controlplaneclient.GatewayPersister，而不是它所满足的任一
// 接口（httpapi.JobPersistClient、httpapi.JobRecoveryClient），好让调用点
// 一次性做完 nil 检查，再把它赋给两个 httpapi.Config 字段。若改为返回接口
// 类型，会冒经典陷阱的风险：一个装箱进接口的 nil *GatewayPersister，本身
// 不是一个 nil 接口，httpapi.New 自己的 `cfg.JobPersistClient != nil` 检查
// 就会在一个根本没配置控制面的 Gateway 上错误地读出 true。
func jobPersistenceAdapter(addr, token string, logger *slog.Logger) (*controlplaneclient.GatewayPersister, error) {
	if addr == "" {
		return nil, nil
	}
	if token == "" {
		token = os.Getenv(controlPlaneTokenEnv)
	}
	client, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{
		Endpoint: addr,
		Token:    token,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("persisting and recovering job records against the control plane", slog.String("control_plane_addr", addr))
	return controlplaneclient.NewGatewayPersister(client), nil
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

// loadSecretFile reads a bearer-token secret from path, trimmed of
// surrounding whitespace, or returns "" without error if path is empty — the
// caller treats that as "send no credential," not as a misconfiguration.
func loadSecretFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read " + path + ": " + err.Error())
	}
	return strings.TrimSpace(string(data)), nil
}

// buildArtifactStorage constructs the objectstore.Backend STATUS.md's P04
// artifact persistence uses, from -artifact-storage and its per-backend
// flags. kind == "" disables it — httpapi.Config.ArtifactStorage stays nil,
// and jobPersister reports artifact metadata exactly as it did before P04 —
// the same nil-degrades pattern every other optional dependency in this file
// follows.
//
// Secrets (the S3 key pair, the WebDAV credentials) are read from files via
// loadSecretFile rather than accepted as flag values themselves, matching
// this file's existing -xxx-file convention (see -registry-join-token-file):
// a flag value is visible in a process listing, a secret must not be.
//
// buildArtifactStorage 依据 -artifact-storage 及其各后端子参数，构造
// STATUS.md P04 产物持久化所用的 objectstore.Backend。kind 为空时关闭它——
// httpapi.Config.ArtifactStorage 保持 nil，jobPersister 上报产物元数据的
// 方式与 P04 之前完全一致——与本文件里其余每一个可选依赖遵循的是同一种
// 「为 nil 时退化」模式。
//
// 密钥（S3 的一对 key、WebDAV 凭据）经由 loadSecretFile 从文件读取，而不是
// 直接作为 flag 值接受，与本文件既有的 -xxx-file 约定一致（参见
// -registry-join-token-file）：flag 值在进程列表里可见，密钥不能。
func buildArtifactStorage(kind, localDir,
	s3Endpoint, s3Region, s3Bucket, s3Prefix string, s3PathStyle bool, s3AccessKeyIDFile, s3SecretAccessKeyFile string,
	webdavURL, webdavDir, webdavUsernameFile, webdavPasswordFile string,
) (objectstore.Backend, error) {
	if kind == "" {
		return nil, nil
	}
	cfg := objectstore.Config{Kind: kind}
	switch kind {
	case "local":
		cfg.Local = objectstore.LocalConfig{Dir: localDir}
	case "s3":
		accessKeyID, err := loadSecretFile(s3AccessKeyIDFile)
		if err != nil {
			return nil, err
		}
		secretAccessKey, err := loadSecretFile(s3SecretAccessKeyFile)
		if err != nil {
			return nil, err
		}
		cfg.S3 = objectstore.S3Config{
			Endpoint:        s3Endpoint,
			Region:          s3Region,
			Bucket:          s3Bucket,
			Prefix:          s3Prefix,
			UsePathStyle:    s3PathStyle,
			AccessKeyID:     accessKeyID,
			SecretAccessKey: secretAccessKey,
		}
	case "webdav":
		username, err := loadSecretFile(webdavUsernameFile)
		if err != nil {
			return nil, err
		}
		password, err := loadSecretFile(webdavPasswordFile)
		if err != nil {
			return nil, err
		}
		cfg.WebDAV = objectstore.WebDAVConfig{URL: webdavURL, Dir: webdavDir, Username: username, Password: password}
	}
	return objectstore.New(cfg)
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
