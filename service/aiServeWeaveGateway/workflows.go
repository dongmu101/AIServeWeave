package main

import (
	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
	"AIServeWeave/service/aiServeWeaveGateway/workflowsync"
	"context"
	"errors"
	"os"
	"time"
)

func configureWorkflows(ctx context.Context, source, files, stateFile, endpoint, token string, interval time.Duration) (func() workflowtemplate.Applied, *workflow.Handle, *workflowsync.Syncer, error) {
	if interval <= 0 {
		return nil, nil, nil, errors.New("-workflow-sync-interval must be positive")
	}
	switch source {
	case "file":
		reg, err := workflow.Load(splitCommaList(files)...)
		if err != nil {
			return nil, nil, nil, err
		}
		handle := workflow.NewHandle(reg)
		digest, err := reg.BundleDigest()
		if err != nil {
			return nil, nil, nil, err
		}
		now := time.Now().UTC()
		status := workflowtemplate.Applied{Mode: "file", TemplateCount: reg.Len(), BundleDigest: digest, AppliedAt: now, CheckedAt: now}
		return func() workflowtemplate.Applied { return status }, handle, nil, nil
	case "controlplane":
		if files != "" {
			return nil, nil, nil, errors.New("-workflow-source=controlplane conflicts with -workflow-templates")
		}
		if token == "" {
			token = os.Getenv(controlPlaneTokenEnv)
		}
		handle := workflow.NewEmptyHandle()
		syncer, err := workflowsync.New(workflowsync.Config{Endpoint: endpoint, Token: token, StateFile: stateFile, Interval: interval, Apply: handle.Store})
		if err != nil {
			return nil, nil, nil, err
		}
		if err = syncer.Start(ctx); err != nil {
			return nil, nil, nil, err
		}
		return syncer.Status, handle, syncer, nil
	default:
		return nil, nil, nil, errors.New("-workflow-source must be file or controlplane")
	}
}
