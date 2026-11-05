package claudecode

import (
	"encoding/json"
	"errors"

	"AIServeWeave/common/runtime"
)

// messagesRoundDiagnostics retains only fixed categories from delivered events.
// messagesRoundDiagnostics 只保留已投递事件的固定分类，不保留请求或判定载荷。
type messagesRoundDiagnostics struct {
	path, status string
}

func newMessagesRoundDiagnostics(native, guarded bool) *messagesRoundDiagnostics {
	switch {
	case guarded:
		return &messagesRoundDiagnostics{path: "native_safeguards", status: "missing"}
	case native:
		return &messagesRoundDiagnostics{path: "native", status: "not_requested"}
	}
	return &messagesRoundDiagnostics{path: "mcp_bridge", status: "not_requested"}
}

func (d *messagesRoundDiagnostics) observe(raw json.RawMessage) {
	if d.path != "native_safeguards" {
		return
	}
	var event struct {
		Type  string
		Delta struct {
			Results json.RawMessage `json:"safeguard_results"`
		}
	}
	if json.Unmarshal(raw, &event) != nil || event.Type != "message_delta" || len(event.Delta.Results) == 0 {
		return
	}
	var results []struct {
		Type   string
		Status struct{ Type string }
	}
	if json.Unmarshal(event.Delta.Results, &results) != nil {
		d.status = "invalid"
		return
	}
	for _, result := range results {
		if result.Type != "dangerous_tool_use" {
			continue
		}
		status := result.Status.Type
		switch status {
		case "available", "unsupported", "unavailable":
		default:
			status = "unknown"
		}
		if d.status == "missing" || d.status == status {
			d.status = status
		} else {
			d.status = "mixed"
		}
	}
}

// admissionError retains only fixed local categories, never caller-supplied data.
// admissionError 仅保留本地固定分类，不携带调用方提交的数据。
type admissionError struct {
	cause   error
	reason  string
	changes []string
}

func (e *admissionError) Error() string { return e.cause.Error() }
func (e *admissionError) Unwrap() error { return e.cause }

func rejectAdmission(cause error, reason string) error {
	return &admissionError{cause: cause, reason: reason}
}

// continuationMismatch inspects a session while its mutex is held.
// continuationMismatch 在持有会话锁时检查不匹配原因，仅返回固定分类及字段名。
func continuationMismatch(s *messagesSession, owner principal, req messagesRequest, results map[string]toolResult, last []messageBlock) error {
	reason := "continuation_tool_ids_mismatch"
	var changes []string
	switch {
	case s.owner != owner:
		reason = "continuation_owner_mismatch"
	case s.ctx.Err() != nil:
		reason = "continuation_canceled"
	case s.busy:
		reason = "continuation_busy"
	case !s.waiting:
		reason = "continuation_not_waiting"
	case sessionConfig(s.request) != sessionConfig(req):
		reason = "continuation_config_mismatch"
		changes = sessionConfigChanges(s.request, req)
	case s.historyHash(req.Messages[:len(req.Messages)-1]) != s.history:
		reason = "continuation_history_mismatch"
	case len(last) != len(results):
		reason = "continuation_extra_content"
	case len(results) != len(s.roundTools):
		reason = "continuation_tool_count_mismatch"
	default:
		for id := range results {
			if call := s.roundTools[id]; call != nil && call.delivered {
				reason = "continuation_already_delivered"
				break
			}
		}
	}
	return &admissionError{cause: errCallRejected, reason: reason, changes: changes}
}

// sessionConfigChanges names changed settings without logging their values or hashes.
// sessionConfigChanges 只列出变更设置的字段名，不记录值或哈希。
func sessionConfigChanges(a, b messagesRequest) []string {
	var changed []string
	for _, field := range []struct {
		name string
		a, b any
	}{
		{"model", a.Model, b.Model},
		{"max_tokens", a.MaxTokens, b.MaxTokens},
		{"system", a.System, b.System},
		{"tools", a.Tools, b.Tools},
		{"tool_choice", a.ToolChoice, b.ToolChoice},
		{"thinking", a.Thinking, b.Thinking},
		{"output_config", a.OutputConfig, b.OutputConfig},
		{"temperature", a.Temperature, b.Temperature},
		{"safeguards", a.Safeguards, b.Safeguards},
		{"stop_sequences", a.StopSequences, b.StopSequences},
		{"api_version", a.apiVersion, b.apiVersion},
		{"beta_features", a.betaFeatures, b.betaFeatures},
	} {
		if canonical(field.a) != canonical(field.b) {
			changed = append(changed, field.name)
		}
	}
	return changed
}

func (r *Runtime) rejectMessages(code runtime.ErrorCode, reason string, cause error) error {
	var rejected *admissionError
	if errors.As(cause, &rejected) {
		reason = rejected.reason
	}
	attrs := []any{"runtime_id", r.cfg.ID, "code", string(code), "reason", reason}
	if rejected != nil && len(rejected.changes) > 0 {
		attrs = append(attrs, "config_changes", rejected.changes)
	}
	r.logger.Warn("Claude Messages request rejected", attrs...)
	failure := claudeError(code).(*runtime.RuntimeError)
	failure.RuntimeID = r.cfg.ID
	return failure
}
