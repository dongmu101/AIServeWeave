package cliagent

import (
	"encoding/json"
	"errors"

	"AIServeWeave/common/runtime"
)

const codeBuddyRPCID = "aisw_model_discovery"

func init() {
	register(&cliSpec{
		Kind:            runtime.KindCodeBuddy,
		Label:           "CodeBuddy",
		Executables:     []string{"codebuddy", "cbc"},
		PassEnvPrefixes: []string{"CODEBUDDY_"},
		SignIn:          []string{"not authenticated", "authentication required", "please login", "please log in", "unauthorized"},
		Aliases:         []string{"default-model"},
		Discover:        codeBuddyDiscover,
		// --tools "" offers no built-in tool, --strict-mcp-config loads no MCP
		// server, and dontAsk denies anything still not pre-approved.
		// --tools "" 不提供任何内建工具，--strict-mcp-config 不加载任何 MCP 服务，
		// dontAsk 拒绝其余一切未预先批准的操作。
		Turn: func(_, _, prompt, model string) ([]string, []byte) {
			return []string{"--print", "--output-format", "stream-json", "--verbose", "--input-format", "text", "--no-session-persistence",
				"--permission-mode", "dontAsk", "--setting-sources", "user", "--strict-mcp-config", "--tools", "", "--model", model}, []byte(prompt)
		},
		Parser: func() parser { return &claudeParser{} },
	})
}

// codeBuddyDiscover sends one initialize control request over stream-json
// input and reads the model list from its control_response. A null account
// in that response does not mean signed out.
//
// codeBuddyDiscover 通过 stream-json 输入发送一次 initialize 控制请求，并从
// control_response 中读取模型列表。响应中 account 为 null 并不代表未登录。
func codeBuddyDiscover(c *call) ([]string, error) {
	request, err := json.Marshal(map[string]any{"type": "control_request", "request_id": codeBuddyRPCID, "request": map[string]string{"subtype": "initialize"}})
	if err != nil {
		return nil, ErrProtocol
	}
	var ids []string
	answered := false
	diag, err := c.rpc([]string{"--print", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--no-session-persistence", "--setting-sources", "user", "--strict-mcp-config"}, append(request, '\n'), func(line []byte) (bool, error) {
		obj := object(line)
		if str(obj, "type") != "control_response" {
			return false, nil
		}
		response := object(obj["response"])
		if id := str(response, "request_id"); id != "" && id != codeBuddyRPCID {
			return false, nil
		}
		answered = true
		if str(response, "subtype") == "error" {
			if c.signedOut(line) {
				return true, ErrAuthentication
			}
			return true, ErrRPC
		}
		body := response
		if inner := object(response["response"]); inner != nil {
			body = inner
		}
		var parseErr error
		ids, parseErr = claudeModels(body["models"], "value", "id")
		return true, parseErr
	})
	if errors.Is(err, errExit) && c.signedOut(diag) {
		return nil, ErrAuthentication
	}
	if err == nil && !answered {
		if c.signedOut(diag) {
			return nil, ErrAuthentication
		}
		return nil, ErrProtocol
	}
	return ids, err
}
