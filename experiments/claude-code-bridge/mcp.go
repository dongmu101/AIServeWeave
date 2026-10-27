package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

type rpcRequest struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func mcpHandler(b *broker, token, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || r.Header.Get("Origin") != "" {
			http.Error(w, "origin or host rejected", 403)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "authentication required", 401)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "JSON required", 415)
			return
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEventBytes))
		var req rpcRequest
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Version != "2.0" || req.Method == "" {
			http.Error(w, "invalid JSON-RPC request", 400)
			return
		}
		if len(req.ID) == 0 {
			if req.Method != "notifications/initialized" {
				http.Error(w, "invalid JSON-RPC request", 400)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if !validRPCID(req.ID) {
			http.Error(w, "invalid JSON-RPC request", 400)
			return
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				reply["error"] = rpcError(-32602, "invalid parameters")
				break
			}
			version := params.ProtocolVersion
			if version != "2025-06-18" && version != "2025-03-26" && version != "2024-11-05" {
				version = "2025-06-18"
			}
			reply["result"] = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "aisw-probe", "version": "0.1.0"}}
		case "ping":
			reply["result"] = map[string]any{}
		case "tools/list":
			reply["result"] = map[string]any{"tools": []any{map[string]any{
				"name": "probe_echo", "description": "Return a synthetic token from the probe client. No files or commands are accessed.",
				"annotations": map[string]bool{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"label": map[string]string{"type": "string"}}, "required": []string{"label"}, "additionalProperties": false},
			}}}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				reply["error"] = rpcError(-32602, "invalid parameters")
				break
			}
			result, err := b.call(r.Context(), params.Name, params.Arguments)
			if err != nil {
				reply["error"] = rpcError(-32602, "probe tool call failed")
				break
			}
			reply["result"] = map[string]any{"content": []any{map[string]string{"type": "text", "text": result.Text}}, "isError": result.IsError}
		default:
			reply["error"] = rpcError(-32601, "method not found")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})
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
