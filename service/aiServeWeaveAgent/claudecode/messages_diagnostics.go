package claudecode

import (
	"errors"

	"AIServeWeave/common/runtime"
)

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
	case len(results) != len(s.pending):
		reason = "continuation_tool_count_mismatch"
	default:
		for id := range results {
			if call := s.pending[id]; call != nil && call.delivered {
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
