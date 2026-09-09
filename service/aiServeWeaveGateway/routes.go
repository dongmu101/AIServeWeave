package main

import (
	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveGateway/routesync"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"context"
	"errors"
	"os"
	"time"
)

func configureRoutes(ctx context.Context, source, files, stateFile, endpoint, token string, interval time.Duration, apply func(*routing.Table)) (func() modelroute.Applied, *routesync.Syncer, error) {
	if interval <= 0 {
		return nil, nil, errors.New("-route-sync-interval must be positive")
	}
	switch source {
	case "file":
		table, err := routing.Load(splitCommaList(files)...)
		if err != nil {
			return nil, nil, err
		}
		digest, err := modelroute.Digest(table.Routes())
		if err != nil {
			return nil, nil, err
		}
		apply(table)
		now := time.Now().UTC()
		status := modelroute.Applied{Mode: "file", Digest: digest, AppliedAt: now, CheckedAt: now}
		return func() modelroute.Applied { return status }, nil, nil
	case "controlplane":
		if files != "" {
			return nil, nil, errors.New("-route-source=controlplane conflicts with -model-routes")
		}
		if token == "" {
			token = os.Getenv(controlPlaneTokenEnv)
		}
		syncer, err := routesync.New(routesync.Config{Endpoint: endpoint, Token: token, StateFile: stateFile, Interval: interval, Apply: apply})
		if err != nil {
			return nil, nil, err
		}
		if err = syncer.Start(ctx); err != nil {
			return nil, nil, err
		}
		return syncer.Status, syncer, nil
	default:
		return nil, nil, errors.New("-route-source must be file or controlplane")
	}
}
