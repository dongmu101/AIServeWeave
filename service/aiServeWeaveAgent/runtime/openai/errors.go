package openai

import "encoding/json"

// maxErrorMessageLen bounds how much of an upstream error body ends up in a
// RuntimeError's Message, so a misbehaving backend cannot inflate logs or
// error responses with an unbounded body.
const maxErrorMessageLen = 2048

type errorEnvelope struct {
	Error *errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

// parseErrorMessage extracts a human-readable message from an
// OpenAI-compatible error body ({"error":{"message":...}}). If the body
// does not match that shape, it falls back to the raw body so diagnostic
// information is not silently dropped. The caller is responsible for
// redacting secrets from the result before exposing it.
func parseErrorMessage(body []byte) string {
	if len(body) == 0 {
		return "empty error response"
	}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && env.Error.Message != "" {
		return truncate(env.Error.Message, maxErrorMessageLen)
	}
	return truncate(string(body), maxErrorMessageLen)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
