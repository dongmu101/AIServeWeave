package openai

import (
	"encoding/json"
	"fmt"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

// marshalToMap marshals dto and decodes it back into a
// map[string]json.RawMessage, giving field-level access to a modeled
// request's wire representation without hand-writing a second encoder.
func marshalToMap(dto any) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// mergeExtraFields marshals dto and merges extra's keys into the result,
// so a caller-supplied backend-private parameter can ride along in the
// request body. A key in extra that names an already-modeled field (per
// modeled, independent of whether that field is currently set — a caller
// cannot use an unset field as a loophole) is rejected with an
// ErrorInvalidConfig RuntimeError rather than silently overwriting or being
// overwritten.
func (c *Client) mergeExtraFields(operation string, dto any, extra map[string]json.RawMessage, modeled map[string]bool) (map[string]json.RawMessage, error) {
	base, err := marshalToMap(dto)
	if err != nil {
		return nil, c.localError(operation, runtime.ErrorInvalidConfig, fmt.Sprintf("encode request: %v", err))
	}
	if len(extra) == 0 {
		return base, nil
	}
	for k := range extra {
		if modeled[k] {
			return nil, c.localError(operation, runtime.ErrorInvalidConfig, fmt.Sprintf("extra key %q collides with a modeled request field", k))
		}
	}
	merged := make(map[string]json.RawMessage, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged, nil
}
