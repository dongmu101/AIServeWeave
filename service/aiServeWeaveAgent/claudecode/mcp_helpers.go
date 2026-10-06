package claudecode

import (
	"bytes"
	"encoding/json"
)

type rpcRequest struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcError(code int, message string) map[string]any {
	return map[string]any{"code": code, "message": message}
}

func validRPCID(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var id any
	if dec.Decode(&id) != nil {
		return false
	}
	switch v := id.(type) {
	case string:
		return len(v) <= 128
	case json.Number:
		_, err := v.Int64()
		return err == nil
	default:
		return false
	}
}
