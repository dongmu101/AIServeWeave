package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"AIServeWeave/common/localdiscovery"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/ollama"
	"AIServeWeave/common/runtime/vllm"
)

// startLocalInventory discovers configured local backends for the operator inventory.
// Its stop function waits for discovery before closing the runtime manager.
//
// startLocalInventory 为运维清单发现已配置的本机后端。
// 返回的停止函数先等待发现退出，再关闭运行时管理器。
func startLocalInventory(ctx context.Context, logger *slog.Logger, sink runtime.Metrics, enabled bool, interval time.Duration, ollamaURL, vllmURL string) (func() []runtime.Snapshot, func(), error) {
	if !enabled {
		return nil, func() {}, nil
	}
	registry := runtime.NewRegistry()
	for kind, factory := range map[runtime.Kind]runtime.Factory{
		runtime.KindOllama: ollama.New,
		runtime.KindVLLM:   vllm.New,
	} {
		if err := registry.Register(kind, factory); err != nil {
			return nil, nil, err
		}
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	manager := runtime.NewManager(registry, runtime.Dependencies{
		HTTPClient: &http.Client{
			Transport: transport, Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Logger:  logger,
		Metrics: sink,
	})
	candidates := make([]localdiscovery.Candidate, 0, 2)
	for kind, address := range map[runtime.Kind]string{runtime.KindOllama: ollamaURL, runtime.KindVLLM: vllmURL} {
		if address != "" {
			candidates = append(candidates, localdiscovery.Candidate{Kind: kind, BaseURL: address})
		}
	}
	scanner, err := localdiscovery.New(localdiscovery.Config{Manager: manager, Logger: logger, Interval: interval, Candidates: candidates})
	if err != nil {
		return nil, nil, err
	}
	scanCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = scanner.Run(scanCtx)
	}()
	return manager.Snapshot, func() {
		cancel()
		<-done
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		if err := manager.Close(closeCtx); err != nil {
			logger.Error("local inventory shutdown failed", slog.Any("error", err))
		}
		transport.CloseIdleConnections()
	}, nil
}
