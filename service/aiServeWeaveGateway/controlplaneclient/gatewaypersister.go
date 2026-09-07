package controlplaneclient

import (
	"context"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// GatewayPersister adapts a JobsClient to httpapi.JobPersistClient's and
// httpapi.JobRecoveryClient's minimal, primitive-typed interfaces. One
// adapter satisfies both: writing job records (J05) and reading back which
// ones a restarted replica has forgotten (J06) are different concerns, but
// both are just JobsClient calls reshaped to types httpapi can name without
// importing this package.
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

// CreateJobArtifact implements httpapi.JobPersistClient.
func (g *GatewayPersister) CreateJobArtifact(ctx context.Context, jobID, artifactID, tenantID, filename, subfolder, artifactType string) error {
	_, err := g.client.CreateJobArtifact(ctx, jobID, CreateJobArtifactRequest{
		ArtifactID: artifactID,
		TenantID:   tenantID,
		Filename:   filename,
		Subfolder:  subfolder,
		Type:       artifactType,
	})
	return err
}

// ListActiveJobsForRoute implements httpapi.JobRecoveryClient.
func (g *GatewayPersister) ListActiveJobsForRoute(ctx context.Context, nodeID, runtimeID string) ([]httpapi.RecoveredJob, error) {
	jobs, err := g.client.ListActiveJobsForRoute(ctx, nodeID, runtimeID)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.RecoveredJob, len(jobs))
	for i, j := range jobs {
		out[i] = httpapi.RecoveredJob{
			JobID:           j.JobID,
			TenantID:        j.TenantID,
			WorkflowID:      j.WorkflowID,
			WorkflowVersion: j.WorkflowVersion,
			NodeID:          j.NodeID,
			RuntimeID:       j.RuntimeID,
			BackendRunID:    j.BackendRunID,
			State:           j.State,
			ErrorSummary:    j.ErrorSummary,
			ObservedSeq:     j.ObservedSeq,
			CreatedAt:       j.CreatedAt,
			UpdatedAt:       j.UpdatedAt,
		}
	}
	return out, nil
}

var (
	_ httpapi.JobPersistClient  = (*GatewayPersister)(nil)
	_ httpapi.JobRecoveryClient = (*GatewayPersister)(nil)
)
