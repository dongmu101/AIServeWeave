package types

import (
	"encoding/json"
	"time"

	"AIServeWeave/common/flowdef"
)

// StudioFlowResponse is one flow. Definition, the latest version's
// common/flowdef JSON, is left out of lists.
//
// StudioFlowResponse 是一个编排。Definition 是最新版本的 common/flowdef JSON，列表中
// 不包含它。
type StudioFlowResponse struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Version    int64           `json:"version"`
	Definition json.RawMessage `json:"definition,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// StudioFlowListResponse is one page of flows.
// / StudioFlowListResponse 是一页编排。
type StudioFlowListResponse struct {
	Data []StudioFlowResponse `json:"data"`
	Next string               `json:"next,omitempty"`
}

// StudioFlowVersionResponse is one saved version of a flow.
// / StudioFlowVersionResponse 是编排的一个已保存版本。
type StudioFlowVersionResponse struct {
	Version    int64           `json:"version"`
	Definition json.RawMessage `json:"definition"`
	CreatedAt  time.Time       `json:"created_at"`
}

// CreateStudioFlowRequest saves a new flow.
// / CreateStudioFlowRequest 保存一个新编排。
type CreateStudioFlowRequest struct {
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"`
}

// UpdateStudioFlowRequest renames a flow and/or saves a new definition.
// BaseVersion, when set, is the version the editor started from; the save is
// refused with studio_flow_changed if the flow has moved on since.
//
// UpdateStudioFlowRequest 重命名编排和/或保存新定义。BaseVersion 设置时是编辑器开始编辑
// 时的版本；若编排此后已被修改，保存以 studio_flow_changed 拒绝。
type UpdateStudioFlowRequest struct {
	Name        *string         `json:"name,omitempty"`
	Definition  json.RawMessage `json:"definition,omitempty"`
	BaseVersion int64           `json:"base_version,omitempty"`
}

// StudioFlowInvalidResponse is the 400 for a definition that fails
// common/flowdef validation: every issue, each naming its node and a stable
// code. Issues name identifiers only, never flow content.
//
// StudioFlowInvalidResponse 是定义未通过 common/flowdef 校验时的 400：列出全部问题，
// 每条都指明所属节点与稳定的错误码。问题只包含标识符，从不包含编排内容。
type StudioFlowInvalidResponse struct {
	Error     string          `json:"error"`
	Code      string          `json:"code"`
	Issues    []flowdef.Issue `json:"issues"`
	Truncated bool            `json:"truncated,omitempty"`
}

// StartStudioRunRequest records a run a Gateway replica is about to execute.
// / StartStudioRunRequest 记录 Gateway 副本即将执行的一次运行。
type StartStudioRunRequest struct {
	ID           string          `json:"id"`
	FlowID       string          `json:"flow_id"`
	FlowVersion  int64           `json:"flow_version"`
	Inputs       json.RawMessage `json:"inputs,omitempty"`
	ReplicaID    string          `json:"replica_id"`
	LeaseSeconds int             `json:"lease_seconds"`
}

// StudioRunResponse is one run. Inputs, Outputs and Steps are left out of
// lists. A run whose replica stopped renewing its lease reads as interrupted.
//
// StudioRunResponse 是一次运行。列表中不包含 Inputs、Outputs 与 Steps。副本已停止续租的
// 运行读作 interrupted。
type StudioRunResponse struct {
	ID               string                  `json:"id"`
	FlowID           string                  `json:"flow_id"`
	FlowVersion      int64                   `json:"flow_version"`
	Status           string                  `json:"status"`
	ErrorCode        string                  `json:"error_code,omitempty"`
	FailedNode       string                  `json:"failed_node,omitempty"`
	Inputs           json.RawMessage         `json:"inputs,omitempty"`
	Outputs          json.RawMessage         `json:"outputs,omitempty"`
	PromptTokens     int64                   `json:"prompt_tokens"`
	CompletionTokens int64                   `json:"completion_tokens"`
	ReplicaID        string                  `json:"replica_id"`
	CreatedAt        time.Time               `json:"created_at"`
	FinishedAt       *time.Time              `json:"finished_at,omitempty"`
	Steps            []StudioRunStepResponse `json:"steps,omitempty"`
}

// StudioRunListResponse is one page of a flow's runs.
// / StudioRunListResponse 是某个编排运行记录的一页。
type StudioRunListResponse struct {
	Data []StudioRunResponse `json:"data"`
	Next string              `json:"next,omitempty"`
}

// StudioRunStepResponse is how one node of a run ended.
// / StudioRunStepResponse 是运行中某个节点的结束情况。
type StudioRunStepResponse struct {
	NodeID           string            `json:"node_id"`
	NodeType         string            `json:"node_type"`
	Status           string            `json:"status"`
	ErrorCode        string            `json:"error_code,omitempty"`
	Model            string            `json:"model,omitempty"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	Outputs          map[string]string `json:"outputs,omitempty"`
	PromptTokens     int64             `json:"prompt_tokens"`
	CompletionTokens int64             `json:"completion_tokens"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
}

// RenewStudioRunRequest extends a replica's lease on a run.
// / RenewStudioRunRequest 延长副本对运行的租约。
type RenewStudioRunRequest struct {
	ReplicaID    string `json:"replica_id"`
	LeaseSeconds int    `json:"lease_seconds"`
}

// RecordStudioRunStepRequest reports how one node of a run ended.
// / RecordStudioRunStepRequest 报告运行中某个节点的结束情况。
type RecordStudioRunStepRequest struct {
	ReplicaID        string            `json:"replica_id"`
	NodeID           string            `json:"node_id"`
	NodeType         string            `json:"node_type"`
	Status           string            `json:"status"`
	ErrorCode        string            `json:"error_code,omitempty"`
	Model            string            `json:"model,omitempty"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	Outputs          map[string]string `json:"outputs,omitempty"`
	PromptTokens     int64             `json:"prompt_tokens"`
	CompletionTokens int64             `json:"completion_tokens"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
}

// FinishStudioRunRequest reports the end of a run.
// / FinishStudioRunRequest 报告运行的结束。
type FinishStudioRunRequest struct {
	ReplicaID        string            `json:"replica_id"`
	Status           string            `json:"status"`
	ErrorCode        string            `json:"error_code,omitempty"`
	FailedNode       string            `json:"failed_node,omitempty"`
	Outputs          map[string]string `json:"outputs,omitempty"`
	PromptTokens     int64             `json:"prompt_tokens"`
	CompletionTokens int64             `json:"completion_tokens"`
}
