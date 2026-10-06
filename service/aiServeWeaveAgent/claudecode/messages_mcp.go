package claudecode

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

// callTool forwards a matched model call; it never executes submitted arguments.
// callTool 转发与模型事件匹配的工具调用，从不执行提交的参数。
func (s *messagesSession) callTool(ctx context.Context, name string, input json.RawMessage) (toolResult, error) {
	s.mu.Lock()
	if s.mcpCalls >= maxPending {
		s.mu.Unlock()
		return toolResult{}, errCapacity
	}
	s.mcpCalls++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.mcpCalls--; s.mu.Unlock() }()
	for {
		s.mu.Lock()
		for id, p := range s.pending {
			if !p.claimed && p.block.Name == name && canonical(normalizedToolInput(name, p.block.Input, s.request.Tools)) == canonical(normalizedToolInput(name, input, s.request.Tools)) {
				p.claimed = true
				s.mu.Unlock()
				select {
				case result := <-p.result:
					s.mu.Lock()
					delete(s.pending, id)
					s.mu.Unlock()
					return result, nil
				case <-ctx.Done():
					return toolResult{}, ctx.Err()
				case <-s.ctx.Done():
					return toolResult{}, errClosed
				}
			}
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return toolResult{}, ctx.Err()
		case <-s.ctx.Done():
			return toolResult{}, errClosed
		}
	}
}

func bridgeMCPHandler(s *messagesSession, token, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || r.Header.Get("Origin") != "" {
			http.Error(w, "origin or host rejected", 403)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "authentication required", 401)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST required", 405)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "JSON required", 415)
			return
		}
		var req rpcRequest
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEventBytes))
		if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || req.Version != "2.0" {
			http.Error(w, "invalid JSON-RPC request", 400)
			return
		}
		if len(req.ID) == 0 && req.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		if !validRPCID(req.ID) {
			http.Error(w, "invalid JSON-RPC request", 400)
			return
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "aisw-claude-bridge", "version": "0.2.0"}}
		case "ping":
			reply["result"] = map[string]any{}
		case "tools/list":
			tools := make([]any, len(s.request.Tools))
			for i, t := range s.request.Tools {
				tools[i] = map[string]any{"name": bridgeToolName(i), "description": t.Description, "inputSchema": t.InputSchema}
			}
			reply["result"] = map[string]any{"tools": tools}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
				Meta      json.RawMessage `json:"_meta,omitempty"`
			}
			if decodeStrict(req.Params, &params) != nil {
				reply["error"] = rpcError(-32602, "invalid parameters")
				break
			}
			name := ""
			for i, t := range s.request.Tools {
				if params.Name == bridgeToolName(i) {
					name = t.Name
				}
			}
			if name == "" {
				reply["error"] = rpcError(-32602, "unknown tool")
				break
			}
			var input map[string]json.RawMessage
			if json.Unmarshal(params.Arguments, &input) != nil || input == nil {
				reply["error"] = rpcError(-32602, "invalid arguments")
				break
			}
			result, err := s.callTool(r.Context(), name, params.Arguments)
			if err != nil {
				reply["error"] = rpcError(-32602, "tool result unavailable")
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
