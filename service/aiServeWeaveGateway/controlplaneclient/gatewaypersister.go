package controlplaneclient

import (
	"context"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// GatewayPersister adapts a JobsClient to httpapi.JobPersistClient's
// minimal, primitive-typed interface.
//
// The adaptation exists only because of the direction this package's
// dependency already runs: this file imports httpapi (for the interface it
// implements), and Verifier's own file already imports httpapi too (for
// Identity and KeyVerifier). httpapi therefore cannot import
// controlplaneclient back — that would be a cycle — so httpapi.JobPersistClient
// is declared there in plain types, and this adapter is what lets JobsClient,
// which speaks in this package's own richer types, satisfy it.
//
// GatewayPersister 把一个 JobsClient 适配成 httpapi.JobPersistClient 那个
// 最小的、只用原始类型的接口。
//
// 这层适配存在，纯粹是因为本包依赖方向已经如此：本文件导入 httpapi（为了它
// 所实现的接口），而 Verifier 自己的文件也已经导入了 httpapi（为了 Identity
// 与 KeyVerifier）。因此 httpapi 不能反过来导入 controlplaneclient——那会
// 成环——所以 httpapi.JobPersistClient 在那边是用朴素类型声明的，而这个适配器
// 正是让说着本包自己更丰富类型的 JobsClient 能满足它的东西。
type GatewayPersister struct {
	client *JobsClient
}

// NewGatewayPersister wraps client for use as an httpapi.JobPersistClient.
//
// NewGatewayPersister 把 client 包装成一个 httpapi.JobPersistClient 使用。
func NewGatewayPersister(client *JobsClient) *GatewayPersister {
	return &GatewayPersister{client: client}
}

// CreateJob implements httpapi.JobPersistClient.
func (g *GatewayPersister) CreateJob(ctx context.Context, jobID, tenantID, workflowID, workflowVersion, nodeID, runtimeID, backendRunID, state string, observedSeq int64) error {
	_, err := g.client.CreateJob(ctx, CreateJobRequest{
		JobID:           jobID,
		TenantID:        tenantID,
		WorkflowID:      workflowID,
		WorkflowVersion: workflowVersion,
		NodeID:          nodeID,
		RuntimeID:       runtimeID,
		BackendRunID:    backendRunID,
		State:           state,
		ObservedSeq:     observedSeq,
	})
	return err
}

// UpdateJobState implements httpapi.JobPersistClient.
func (g *GatewayPersister) UpdateJobState(ctx context.Context, tenantID, jobID, state, errorSummary string, observedSeq int64) (bool, error) {
	applied, _, err := g.client.UpdateJobState(ctx, tenantID, jobID, JobStateUpdate{
		State:        state,
		ErrorSummary: errorSummary,
		ObservedSeq:  observedSeq,
	})
	return applied, err
}

var _ httpapi.JobPersistClient = (*GatewayPersister)(nil)
