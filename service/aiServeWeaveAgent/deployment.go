package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
	"AIServeWeave/service/aiServeWeaveAgent/internal/localcontrol"
	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
)

// initializeDeployment creates configuration once without replacing existing state.
// initializeDeployment 仅首次创建部署配置，不替换现有状态。
func initializeDeployment(path string, opts *tunnelOptions) error {
	cfg := &agentconfig.Config{Gateway: agentconfig.GatewayConfig{
		Endpoints: opts.seeds(), Registry: opts.registry, NodeID: opts.nodeID, CertFile: opts.certFile, KeyFile: opts.keyFile, CAFile: opts.caFile, BootstrapTokenFile: opts.bootstrapToken, AllowedRuntimes: opts.runtimeIDs(), Labels: opts.nodeLabels(), MaxGateways: opts.maxGateways}}
	if err := validateDeployment(cfg); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(dir, ".initialize-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	candidate := filepath.Join(staging, "agent.yaml")
	if err := agentconfig.Save(candidate, cfg); err != nil {
		return err
	}
	// A hard link publishes the complete file exclusively on supported local filesystems.
	// 硬链接在受支持的本地文件系统上以独占方式发布完整文件。
	return os.Link(candidate, path)
}

// checkDeployment validates the deployed connection configuration without networking.
// checkDeployment 在不联网的情况下校验部署连接配置。
func checkDeployment(path string) error {
	if err := localstate.CheckPrivate(path, false); err != nil {
		return err
	}
	cfg, err := agentconfig.Load(path)
	if err != nil {
		return err
	}
	return validateDeployment(cfg)
}

func validateDeployment(cfg *agentconfig.Config) error {
	g := cfg.Gateway
	if g.NodeID == "" || len(g.Endpoints) == 0 || g.Registry == "" {
		return errors.New("deployment requires node ID, Registry and Gateway endpoints")
	}
	for _, endpoint := range append(append([]string{}, g.Endpoints...), g.Registry) {
		host, port, err := net.SplitHostPort(endpoint)
		number, numberErr := strconv.Atoi(port)
		if err != nil || host == "" || numberErr != nil || number < 1 || number > 65535 {
			return errors.New("deployment endpoints must be host:port")
		}
	}
	for _, path := range []string{g.CertFile, g.KeyFile, g.CAFile, g.BootstrapTokenFile} {
		if !filepath.IsAbs(path) {
			return errors.New("deployment identity paths must be absolute")
		}
	}
	if cfg.AutoDiscoverInterval != "" {
		duration, err := time.ParseDuration(cfg.AutoDiscoverInterval)
		if err != nil || duration <= 0 {
			return errors.New("invalid auto discovery interval")
		}
	}
	if cfg.LogLevel != "" {
		var level slog.Level
		if level.UnmarshalText([]byte(cfg.LogLevel)) != nil {
			return errors.New("invalid log level")
		}
	}
	seen := map[string]bool{}
	for _, declared := range runtimesFromConfig(cfg.Runtimes) {
		if seen[declared.ID] {
			return errors.New("duplicate runtime ID")
		}
		seen[declared.ID] = true
		if err := declared.Normalize().Validate(); err != nil {
			return errors.New("invalid runtime declaration; review local runtime settings")
		}
	}
	if cfg.MetricsAddr != nil && *cfg.MetricsAddr != "" {
		host, _, err := net.SplitHostPort(*cfg.MetricsAddr)
		ip := net.ParseIP(host)
		if err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("metrics address must be loopback")
		}
	}

	return nil
}

func controlCommand(path, action string) error {
	response, err := localcontrol.Request(context.Background(), path, action)
	if err != nil {
		return fmt.Errorf("local control: %w", err)
	}
	_, err = os.Stdout.Write(response)
	return err
}

// loadEnvironment imports bounded private JSON without evaluating shell code.
// loadEnvironment 导入大小受限的私有 JSON，不求值 shell 代码。
func loadEnvironment(path string) error {
	if err := localstate.CheckPrivate(path, false); err != nil {
		return errors.New("unsafe environment file permissions")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open environment file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(data) > 65536 {
		return errors.New("cannot read bounded environment file")
	}
	var values map[string]string
	// A UTF-8 BOM is accepted for files saved by Windows PowerShell 5.1.
	// 接受 Windows PowerShell 5.1 保存文件时使用的 UTF-8 BOM。
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if json.Unmarshal(data, &values) != nil || len(values) > 256 {
		return errors.New("invalid environment JSON")
	}
	for key, value := range values {
		if !environmentKey.MatchString(key) || strings.ContainsRune(value, 0) {
			return errors.New("invalid environment entry")
		}
	}
	for key, value := range values {
		if os.Setenv(key, value) != nil {
			return errors.New("cannot apply environment entry")
		}
	}
	return nil
}

var environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
