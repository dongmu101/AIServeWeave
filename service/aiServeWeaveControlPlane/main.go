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
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/handler"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("controlplane: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	configFile := flag.String("f", "etc/controlplane.yaml", "path to the configuration file")
	flag.Parse()

	// conf.Load rather than conf.MustLoad, and rest.NewServer rather than
	// rest.MustNewServer: go-zero's Must* helpers exit the process from deep
	// inside a library, which skips every deferred Close above them. This
	// binary unwinds through run() like the other three in this repository.
	//
	// 用 conf.Load 而不是 conf.MustLoad，用 rest.NewServer 而不是 rest.MustNewServer：
	// go-zero 的 Must* 系列会从库的深处直接退出进程，从而跳过其上每一个 defer 的 Close。
	// 本二进制与仓库中另外三个一样，沿着 run() 回溯退出。
	var cfg config.Config
	if err := conf.Load(*configFile, &cfg); err != nil {
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

	if !svcCtx.CacheEnabled() {
		logger.Warn("no Redis configured; every API key verification will query the database")
	}
	if cfg.Database.AutoMigrate {
		logger.Warn("AutoMigrate is on; this process alters its own schema at startup")
	}

	server, err := rest.NewServer(cfg.RestConf)
	if err != nil {
		return err
	}
	defer server.Stop()
	handler.RegisterHandlers(server, svcCtx)

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
