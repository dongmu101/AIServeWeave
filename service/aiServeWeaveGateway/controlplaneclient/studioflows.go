package controlplaneclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

var _ httpapi.StudioFlowStore = (*StudioClient)(nil)

// internalStudioPath builds an internal Studio path under /internal/v1/studio
// carrying the owner in its query.
//
// internalStudioPath 构造 /internal/v1/studio 下、在查询串中携带所有者的内部路径。
func internalStudioPath(owner httpapi.StudioOwner, path string, extra url.Values) string {
	q := url.Values{"tenant_id": {owner.TenantID}, "user_id": {owner.UserID}}
	for k, v := range extra {
		q[k] = v
	}
	return "/internal/v1/studio" + path + "?" + q.Encode()
}

func pageQuery(cursor string, limit int) url.Values {
	extra := url.Values{}
	if cursor != "" {
		extra.Set("cursor", cursor)
	}
	if limit > 0 {
		extra.Set("limit", strconv.Itoa(limit))
	}
	return extra
}

type studioFlowWire struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Version    int64           `json:"version"`
	Definition json.RawMessage `json:"definition"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

func (w studioFlowWire) toFlow() httpapi.StudioFlow {
	return httpapi.StudioFlow{ID: w.ID, Name: w.Name, Version: w.Version, Definition: w.Definition, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
}

type studioRunStepWire struct {
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

type studioRunWire struct {
	ID               string              `json:"id"`
	FlowID           string              `json:"flow_id"`
	FlowVersion      int64               `json:"flow_version"`
	Status           string              `json:"status"`
	ErrorCode        string              `json:"error_code"`
	FailedNode       string              `json:"failed_node"`
	Inputs           json.RawMessage     `json:"inputs"`
	Outputs          map[string]string   `json:"outputs"`
	PromptTokens     int64               `json:"prompt_tokens"`
	CompletionTokens int64               `json:"completion_tokens"`
	ReplicaID        string              `json:"replica_id"`
	CreatedAt        time.Time           `json:"created_at"`
	FinishedAt       *time.Time          `json:"finished_at"`
	Steps            []studioRunStepWire `json:"steps"`
}

func (w studioRunWire) toRun() httpapi.StudioRun {
	run := httpapi.StudioRun{
		ID: w.ID, FlowID: w.FlowID, FlowVersion: w.FlowVersion, Status: w.Status, ErrorCode: w.ErrorCode, FailedNode: w.FailedNode,
		Inputs: w.Inputs, Outputs: w.Outputs, PromptTokens: w.PromptTokens, CompletionTokens: w.CompletionTokens,
		ReplicaID: w.ReplicaID, CreatedAt: w.CreatedAt, FinishedAt: w.FinishedAt,
	}
	for _, s := range w.Steps {
		run.Steps = append(run.Steps, httpapi.StudioRunStep{
			NodeID: s.NodeID, NodeType: s.NodeType, Status: s.Status, ErrorCode: s.ErrorCode, Model: s.Model, Inputs: s.Inputs, Outputs: s.Outputs,
			PromptTokens: s.PromptTokens, CompletionTokens: s.CompletionTokens, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
		})
	}
	return run
}

// CreateFlow implements httpapi.StudioFlowStore.
func (c *StudioClient) CreateFlow(ctx context.Context, owner httpapi.StudioOwner, name string, definition json.RawMessage) (httpapi.StudioFlow, error) {
	body := struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition,omitempty"`
	}{name, definition}
	var out studioFlowWire
	err := c.call(ctx, http.MethodPost, internalStudioPath(owner, "/flows", nil), body, &out)
	return out.toFlow(), err
}

// ListFlows implements httpapi.StudioFlowStore.
func (c *StudioClient) ListFlows(ctx context.Context, owner httpapi.StudioOwner, cursor string, limit int) (httpapi.StudioFlowPage, error) {
	var out struct {
		Data []studioFlowWire `json:"data"`
		Next string           `json:"next"`
	}
	if err := c.call(ctx, http.MethodGet, internalStudioPath(owner, "/flows", pageQuery(cursor, limit)), nil, &out); err != nil {
		return httpapi.StudioFlowPage{}, err
	}
	page := httpapi.StudioFlowPage{Flows: make([]httpapi.StudioFlow, len(out.Data)), Next: out.Next}
	for i, w := range out.Data {
		page.Flows[i] = w.toFlow()
	}
	return page, nil
}

// GetFlow implements httpapi.StudioFlowStore.
func (c *StudioClient) GetFlow(ctx context.Context, owner httpapi.StudioOwner, id string) (httpapi.StudioFlow, error) {
	var out studioFlowWire
	err := c.call(ctx, http.MethodGet, internalStudioPath(owner, "/flows/"+url.PathEscape(id), nil), nil, &out)
	return out.toFlow(), err
}

// UpdateFlow implements httpapi.StudioFlowStore.
func (c *StudioClient) UpdateFlow(ctx context.Context, owner httpapi.StudioOwner, id string, patch httpapi.StudioFlowPatch) (httpapi.StudioFlow, error) {
	body := struct {
		Name        *string         `json:"name,omitempty"`
		Definition  json.RawMessage `json:"definition,omitempty"`
		BaseVersion int64           `json:"base_version,omitempty"`
	}{patch.Name, patch.Definition, patch.BaseVersion}
	var out studioFlowWire
	err := c.call(ctx, http.MethodPatch, internalStudioPath(owner, "/flows/"+url.PathEscape(id), nil), body, &out)
	return out.toFlow(), err
}

// DeleteFlow implements httpapi.StudioFlowStore.
func (c *StudioClient) DeleteFlow(ctx context.Context, owner httpapi.StudioOwner, id string) error {
	return c.call(ctx, http.MethodDelete, internalStudioPath(owner, "/flows/"+url.PathEscape(id), nil), nil, nil)
}

// GetFlowVersion implements httpapi.StudioFlowStore.
func (c *StudioClient) GetFlowVersion(ctx context.Context, owner httpapi.StudioOwner, id string, version int64) (httpapi.StudioFlowVersion, error) {
	var out struct {
		Version    int64           `json:"version"`
		Definition json.RawMessage `json:"definition"`
		CreatedAt  time.Time       `json:"created_at"`
	}
	path := "/flows/" + url.PathEscape(id) + "/versions/" + strconv.FormatInt(version, 10)
	if err := c.call(ctx, http.MethodGet, internalStudioPath(owner, path, nil), nil, &out); err != nil {
		return httpapi.StudioFlowVersion{}, err
	}
	return httpapi.StudioFlowVersion{Version: out.Version, Definition: out.Definition, CreatedAt: out.CreatedAt}, nil
}

// ListRuns implements httpapi.StudioFlowStore.
func (c *StudioClient) ListRuns(ctx context.Context, owner httpapi.StudioOwner, flowID, cursor string, limit int) (httpapi.StudioRunPage, error) {
	var out struct {
		Data []studioRunWire `json:"data"`
		Next string          `json:"next"`
	}
	path := "/flows/" + url.PathEscape(flowID) + "/runs"
	if err := c.call(ctx, http.MethodGet, internalStudioPath(owner, path, pageQuery(cursor, limit)), nil, &out); err != nil {
		return httpapi.StudioRunPage{}, err
	}
	page := httpapi.StudioRunPage{Runs: make([]httpapi.StudioRun, len(out.Data)), Next: out.Next}
	for i, w := range out.Data {
		page.Runs[i] = w.toRun()
	}
	return page, nil
}

// StartRun implements httpapi.StudioFlowStore.
func (c *StudioClient) StartRun(ctx context.Context, owner httpapi.StudioOwner, start httpapi.StudioRunStart) (httpapi.StudioRun, error) {
	body := struct {
		ID           string          `json:"id"`
		FlowID       string          `json:"flow_id"`
		FlowVersion  int64           `json:"flow_version"`
		Inputs       json.RawMessage `json:"inputs,omitempty"`
		ReplicaID    string          `json:"replica_id"`
		LeaseSeconds int             `json:"lease_seconds"`
	}{start.ID, start.FlowID, start.FlowVersion, start.Inputs, start.ReplicaID, int(start.Lease / time.Second)}
	var out studioRunWire
	err := c.call(ctx, http.MethodPost, internalStudioPath(owner, "/runs", nil), body, &out)
	return out.toRun(), err
}

// GetRun implements httpapi.StudioFlowStore.
func (c *StudioClient) GetRun(ctx context.Context, owner httpapi.StudioOwner, id string) (httpapi.StudioRun, error) {
	var out studioRunWire
	err := c.call(ctx, http.MethodGet, internalStudioPath(owner, "/runs/"+url.PathEscape(id), nil), nil, &out)
	return out.toRun(), err
}

// RenewRun implements httpapi.StudioFlowStore.
func (c *StudioClient) RenewRun(ctx context.Context, owner httpapi.StudioOwner, id, replica string, lease time.Duration) error {
	body := struct {
		ReplicaID    string `json:"replica_id"`
		LeaseSeconds int    `json:"lease_seconds"`
	}{replica, int(lease / time.Second)}
	return c.call(ctx, http.MethodPost, internalStudioPath(owner, "/runs/"+url.PathEscape(id)+"/lease", nil), body, nil)
}

// RecordStep implements httpapi.StudioFlowStore.
func (c *StudioClient) RecordStep(ctx context.Context, owner httpapi.StudioOwner, runID, replica string, step httpapi.StudioRunStep) error {
	body := struct {
		ReplicaID string `json:"replica_id"`
		studioRunStepWire
	}{replica, studioRunStepWire{
		NodeID: step.NodeID, NodeType: step.NodeType, Status: step.Status, ErrorCode: step.ErrorCode, Model: step.Model,
		Inputs: step.Inputs, Outputs: step.Outputs, PromptTokens: step.PromptTokens, CompletionTokens: step.CompletionTokens,
		StartedAt: step.StartedAt, FinishedAt: step.FinishedAt,
	}}
	return c.call(ctx, http.MethodPost, internalStudioPath(owner, "/runs/"+url.PathEscape(runID)+"/steps", nil), body, nil)
}

// FinishRun implements httpapi.StudioFlowStore.
func (c *StudioClient) FinishRun(ctx context.Context, owner httpapi.StudioOwner, id, replica string, finish httpapi.StudioRunFinish) error {
	body := struct {
		ReplicaID        string            `json:"replica_id"`
		Status           string            `json:"status"`
		ErrorCode        string            `json:"error_code,omitempty"`
		FailedNode       string            `json:"failed_node,omitempty"`
		Outputs          map[string]string `json:"outputs,omitempty"`
		PromptTokens     int64             `json:"prompt_tokens"`
		CompletionTokens int64             `json:"completion_tokens"`
	}{replica, finish.Status, finish.ErrorCode, finish.FailedNode, finish.Outputs, finish.PromptTokens, finish.CompletionTokens}
	return c.call(ctx, http.MethodPost, internalStudioPath(owner, "/runs/"+url.PathEscape(id)+"/finish", nil), body, nil)
}
