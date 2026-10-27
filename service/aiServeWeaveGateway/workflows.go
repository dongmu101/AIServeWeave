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

// validateImagesWorkflow fails process startup when -images-workflow-id is
// configured but the template it names cannot serve POST
// /v1/images/generations, the same fail-fast discipline configureWorkflows
// already applies to a bad -workflow-templates path — an operator's mistake
// belongs on a startup terminal, not on some caller's request an hour later
// (STATUS.md's P2). An empty id is not checked: it disables the endpoint,
// which httpapi.New already handles by answering 404.
//
// This is a one-time static check, not re-run when a control-plane-synced
// bundle (P03) hot-swaps the registry: a republish that strips the required
// input or output from this same template id is a known, documented gap —
// see the Gateway README's 图像生成 section — and only shows up as a 404 or
// a bind error on the next request, not as a restart failure.
//
// validateImagesWorkflow 在 -images-workflow-id 已配置、但它指名的模板无法
// 服务 POST /v1/images/generations 时让进程启动直接失败，与 configureWorkflows
// 已经对一条坏的 -workflow-templates 路径采用的同一种 fail-fast 纪律——运维的
// 失误该摆在启动终端上，而不是一小时后摆在某个调用方的请求上（STATUS.md 的
// P2）。id 为空时不做检查：它关闭该端点，httpapi.New 已经用 404 处理了这种
// 情形。
//
// 这是一次性的静态检查，不会在一次控制面同步的整包（P03）热替换目录时重新
// 运行：一次重新发布把所需的输入或输出从同一个模板 id 上剥离，是一处已知、
// 写进文档的缺口——见 Gateway README「图像生成」一节——只会在下一次请求时
// 表现为 404 或一次绑定错误，而不是一次重启失败。
func validateImagesWorkflow(handle *workflow.Handle, imagesWorkflowID string) error {
	if imagesWorkflowID == "" {
		return nil
	}
	tpl, ok := handle.Lookup(imagesWorkflowID)
	if !ok {
		return errors.New("-images-workflow-id " + imagesWorkflowID + " is not a registered workflow template")
	}
	hasPrompt := false
	for _, in := range tpl.Inputs {
		if in.Name == "prompt" && in.Type == workflow.InputString && in.Required {
			hasPrompt = true
			break
		}
	}
	if !hasPrompt {
		return errors.New("-images-workflow-id " + imagesWorkflowID + ` must declare a required string input named "prompt"`)
	}
	hasImageOutput := false
	for _, out := range tpl.Outputs {
		if out.Type == "image" {
			hasImageOutput = true
			break
		}
	}
	if !hasImageOutput {
		return errors.New("-images-workflow-id " + imagesWorkflowID + ` must declare at least one output typed "image"`)
	}
	return nil
}
