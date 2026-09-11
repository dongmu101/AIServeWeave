// Command aiserveweave-controlplane runs the control plane's Admin API: the
// tenants, users, API keys and audit trail behind the Console, plus the
// internal endpoint the Gateway verifies API keys against.
//
// It is the first service in this repository built on go-zero, and the only
// one that talks to PostgreSQL and Redis. The data plane keeps its flags and
// its standard-library HTTP; the reasoning for that split is in this service's
// README.
//
// aiserveweave-controlplane 运行控制面的 Admin API：Console 背后的租户、用户、API Key
// 与审计线索，以及供 Gateway 校验 API Key 的内部端点。
//
// 它是本仓库第一个基于 go-zero 构建的服务，也是唯一一个与 PostgreSQL 和 Redis 打交道
// 的服务。数据面保持使用 flag 与标准库 HTTP；这一划分的理由写在本服务的 README 里。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/handler"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// version is stamped at build time via -ldflags="-X main.version=...", see
// the root Dockerfile and scripts/build-release.sh; "dev" is what a plain
// `go build` produces.
//
// version 在构建时通过 -ldflags="-X main.version=..." 注入，见根 Dockerfile 与
// scripts/build-release.sh；直接 `go build` 得到的就是 "dev"。
var version = "dev"

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("controlplane: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	configFile := flag.String("f", "etc/controlplane.yaml", "path to the configuration file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	migrate := flag.String("migrate", "", "database-only command: up, status, or resume")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9090",
		"address the Prometheus /metrics listener binds; loopback by default, empty disables it")
	flag.Parse()

	if *showVersion {
		os.Stdout.WriteString("aiserveweave-controlplane " + version + "\n")
		return nil
	}

	// conf.Load rather than conf.MustLoad, and rest.NewServer rather than
	// rest.MustNewServer: go-zero's Must* helpers exit the process from deep
	// inside a library, which skips every deferred Close above them. This
	// binary unwinds through run() like the other three in this repository.
	//
	// 用 conf.Load 而不是 conf.MustLoad，用 rest.NewServer 而不是 rest.MustNewServer：
	// go-zero 的 Must* 系列会从库的深处直接退出进程，从而跳过其上每一个 defer 的 Close。
	// 本二进制与仓库中另外三个一样，沿着 run() 回溯退出。
	// conf.UseEnv expands ${VAR} in the file, which is what lets a deployment
	// keep its secrets out of the config: the committed file names them and
	// the deployment's own secret management supplies the values. Without it
	// every deployment would have to template the file itself, and the usual
	// shortcut for that is to commit the real values.
	//
	// conf.UseEnv 展开文件里的 ${VAR}，这正是让部署把密钥留在配置之外的办法：提交进
	// 仓库的文件只写出它们的名字，取值由部署自己的密钥管理提供。没有它，每个部署都得
	// 自己模板化这个文件，而那件事常见的捷径就是把真实取值提交上去。
	if *migrate != "" {
		var databaseConfig struct{ Database config.DatabaseConf }
		if err := conf.Load(*configFile, &databaseConfig, conf.UseEnv()); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return svc.RunDatabaseCommand(ctx, databaseConfig.Database, *migrate, os.Stdout)
	}

	var cfg config.Config
	if err := conf.Load(*configFile, &cfg, conf.UseEnv()); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svcCtx, err := svc.NewServiceContext(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := svcCtx.Close(); err != nil {
			logger.Warn("releasing connections", slog.Any("error", err))
		}
	}()

	if cfg.Database.AutoMigrate {
		logger.Warn("AutoMigrate is on; this process applies embedded versioned SQL at startup")
	}

	server, err := rest.NewServer(cfg.RestConf)
	if err != nil {
		return err
	}
	defer server.Stop()
	handler.RegisterHandlers(server, svcCtx)

	var metricsServer *http.Server
	if *metricsAddr == "" {
		logger.Warn("no -metrics-addr; this replica exports no metrics")
	} else {
		metricsServer = svcCtx.MetricsRegistry.Server(*metricsAddr)
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics listener stopped", slog.Any("error", err))
			}
		}()
		logger.Info("metrics listening", slog.String("metrics_addr", *metricsAddr))
	}
	// Closed before the REST server's own defer server.Stop() runs (defers
	// unwind LIFO, so this one — declared after server.Stop()'s defer — fires
	// first), so a scrape mid-shutdown still sees an answering process rather
	// than a connection refused for the whole drain window.
	//
	// 在 REST 服务器自己的 defer server.Stop() 之前关闭(defer 按后进先出展开，
	// 这一个——注册在 server.Stop() 的 defer 之后——先执行)，好让一次落在关停
	// 过程中的抓取，看到的仍是一个在应答的进程，而不是整段排空窗口内的连接拒绝。
	defer func() {
		if metricsServer != nil {
			_ = metricsServer.Close()
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		server.Start()
		// Start blocks until Stop is called, and reports a failure only
		// through its own logger. Closing the channel is how this goroutine
		// says it has returned, so a listener that never came up is not
		// mistaken for a running service.
		//
		// Start 会阻塞直到 Stop 被调用，且只通过它自己的 logger 报告失败。关闭该通道
		// 是这个协程宣告自己已返回的方式，好让一个根本没起来的监听器不被误当成正在
		// 运行的服务。
		close(serveErr)
	}()
	logger.Info("control plane started",
		slog.String("host", cfg.Host),
		slog.Int("port", cfg.Port),
		slog.Bool("cache", svcCtx.CacheEnabled()))

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case <-serveErr:
		logger.Error("the HTTP server stopped on its own")
	}
	return nil
}
