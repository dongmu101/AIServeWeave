package cliagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"AIServeWeave/common/runtime"
)

func init() {
	register(&cliSpec{
		Kind:            runtime.KindQoder,
		Label:           "Qoder",
		Executables:     []string{"qodercli"},
		PassEnvPrefixes: []string{"QODER_"},
		SignIn: []string{"not authenticated", "authentication required", "please login", "please log in", "unauthorized",
			"invalid access token", "access token is invalid", "access token invalid", "access token expired", "access token has expired", "expired access token"},
		Discover: qoderDiscover,
		// --tools "" offers no built-in tool, --strict-mcp-config loads no MCP
		// server, and dont_ask denies anything still not pre-approved.
		// --tools "" 不提供任何内建工具，--strict-mcp-config 不加载任何 MCP 服务，
		// dont_ask 拒绝其余一切未预先批准的操作。
		Turn: func(_, _, prompt, model string) ([]string, []byte) {
			return []string{"--print", "--output-format", "stream-json", "--input-format", "text", "--no-session-persistence",
				"--permission-mode", "dont_ask", "--strict-mcp-config", "--setting-sources", "user", "--tools", "", "--model", model}, []byte(prompt)
		},
		Parser: func() parser { return &claudeParser{} },
	})
}

// ansi matches terminal colour and cursor escape sequences.
// ansi 匹配终端颜色与光标转义序列。
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// qoderDiscover parses `--list-models`, which prints either JSON (an array
// under models, data or items) or a table whose first column is the model.
// qoderDiscover 解析 `--list-models`，其输出要么是 JSON（位于 models、data 或
// items 下的数组），要么是第一列为模型的表格。
func qoderDiscover(c *call) ([]string, error) {
	var output bytes.Buffer
	diag, err := c.run([]string{"--list-models", "--setting-sources", "user"}, nil, func(line []byte) (bool, error) {
		if output.Len()+len(line) > maxListBytes {
			return false, ErrProtocol
		}
		output.Write(line)
		output.WriteByte('\n')
		return false, nil
	})
	if c.signedOut(output.Bytes()) || (errors.Is(err, errExit) && c.signedOut(diag)) {
		return nil, ErrAuthentication
	}
	if errors.Is(err, errExit) {
		return nil, ErrRPC
	}
	if err != nil {
		return nil, err
	}
	if trimmed := bytes.TrimSpace(output.Bytes()); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		raw := json.RawMessage(trimmed)
		if obj := object(raw); obj != nil {
			raw = lookup(obj, "models", "data", "items")
		}
		ids, err := claudeModels(raw, "id", "value", "model")
		return qoderEmpty(c, diag, ids, err)
	}
	return qoderEmpty(c, diag, tableModels(output.String(), "model"), nil)
}

// qoderEmpty reports an empty listing after a clean exit as signed out when
// stderr says so.
// qoderEmpty 在正常退出却列表为空、且 stderr 提示未登录时，报告为未登录。
func qoderEmpty(c *call, diag []byte, ids []string, err error) ([]string, error) {
	if err == nil && len(ids) == 0 && c.signedOut(diag) {
		return nil, ErrAuthentication
	}
	return ids, err
}

// tableModels reads the first column of a │, | or tab separated table,
// skipping the header (whose first cell is header) and rule lines.
// tableModels 读取以 │、| 或制表符分隔的表格的第一列，跳过表头（第一格为
// header）与分隔线。
func tableModels(output, header string) []string {
	var ids []string
	for _, line := range strings.Split(ansi.ReplaceAllString(output, ""), "\n") {
		line = strings.Trim(strings.TrimSpace(line), "│|")
		cell, _, _ := strings.Cut(line, "│")
		cell, _, _ = strings.Cut(cell, "|")
		cell, _, _ = strings.Cut(cell, "\t")
		fields := strings.Fields(cell)
		if len(fields) != 1 || strings.EqualFold(fields[0], header) || !modelID.MatchString(fields[0]) {
			continue
		}
		if len(ids) < maxRawModels {
			ids = append(ids, fields[0])
		}
	}
	return ids
}
