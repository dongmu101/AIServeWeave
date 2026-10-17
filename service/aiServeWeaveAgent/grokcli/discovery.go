package grokcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// discoverModels reads the catalogue from the CLI's ACP initialize response,
// falling back to the human-readable `grok models` listing. Neither path
// submits a prompt or runs inference. Only IDs the CLI actually returned are
// published; there is no static fallback list.
//
// discoverModels 从 CLI 的 ACP initialize 响应读取模型目录，失败时退回人类可读
// 的 `grok models` 列表。两条路径都不提交提示词、不执行推理。只发布 CLI 实际
// 返回的 ID，不使用静态候选列表兜底。
func discoverModels(ctx context.Context, executable string, env []string, touch func()) ([]string, error) {
	ids, err := discoverACP(ctx, executable, env, touch)
	if err == nil || ctx.Err() != nil || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrProcess) {
		return ids, err
	}
	return discoverListing(ctx, executable, env, touch)
}

// discoverACP sends one ACP initialize request to `grok agent stdio` and reads
// modelState from its response.
// discoverACP 向 `grok agent stdio` 发送一次 ACP initialize 请求，并从响应中
// 读取 modelState。
func discoverACP(ctx context.Context, executable string, env []string, touch func()) ([]string, error) {
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": acpRequestID, "method": "initialize", "params": map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "AIServeWeave", "version": "1"},
	}})
	if err != nil {
		return nil, ErrProtocol
	}
	var ids []string
	err = withHome(executable, env, func(dir, exe string, childEnv []string) error {
		answered := false
		diag, err := runCLI(ctx, exe, childEnv, dir, []string{"agent", "--no-leader", "stdio"}, append(request, '\n'), func(line []byte) (bool, error) {
			touch()
			var frame struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(line, &frame) != nil {
				return false, ErrProtocol
			}
			var id string
			if json.Unmarshal(frame.ID, &id) != nil || id != acpRequestID {
				return false, nil // notification or unrelated frame / 通知或无关帧
			}
			answered = true
			if len(frame.Error) > 0 && string(frame.Error) != "null" {
				if mentionsSignIn(frame.Error) {
					return true, ErrAuthentication
				}
				return true, ErrRPC
			}
			var parseErr error
			ids, parseErr = parseACPModels(frame.Result)
			return true, parseErr
		})
		if errors.Is(err, errExit) && mentionsSignIn(diag) {
			return ErrAuthentication
		}
		if err == nil && !answered {
			return ErrProtocol
		}
		return err
	})
	return ids, err
}

// lookup returns the first present key of obj, trying camelCase and snake_case spellings.
// lookup 返回 obj 中第一个存在的键，依次尝试 camelCase 与 snake_case 写法。
func lookup(obj map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if value, ok := obj[key]; ok && len(value) > 0 && string(value) != "null" {
			return value
		}
	}
	return nil
}

// parseACPModels reads modelState from result._meta, result.meta or result
// itself, accepting the compatibility spellings dbx observed in the wild.
// parseACPModels 从 result._meta、result.meta 或 result 本身读取 modelState，
// 接受 dbx 实际观察到的兼容写法。
func parseACPModels(result json.RawMessage) ([]string, error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(result, &top) != nil || top == nil {
		return nil, ErrProtocol
	}
	var state json.RawMessage
	for _, metaKey := range []string{"_meta", "meta"} {
		var meta map[string]json.RawMessage
		if raw := lookup(top, metaKey); raw != nil && json.Unmarshal(raw, &meta) == nil {
			if state = lookup(meta, "modelState", "model_state"); state != nil {
				break
			}
		}
	}
	if state == nil {
		state = lookup(top, "modelState", "model_state")
	}
	var stateObj map[string]json.RawMessage
	if state == nil || json.Unmarshal(state, &stateObj) != nil {
		return nil, ErrProtocol
	}
	var available []map[string]json.RawMessage
	if json.Unmarshal(lookup(stateObj, "availableModels", "available_models"), &available) != nil {
		return nil, ErrProtocol
	}
	var catalogue modelSet
	for _, model := range available {
		var id string
		if raw := lookup(model, "modelId", "model_id", "value", "id"); raw == nil || json.Unmarshal(raw, &id) != nil {
			continue
		}
		if err := catalogue.add(id); err != nil {
			return nil, err
		}
	}
	return catalogue.result()
}

// discoverListing parses `grok models`, whose model lines look like "  * grok-4.5 (default)".
// discoverListing 解析 `grok models` 的输出，模型行形如 "  * grok-4.5 (default)"。
func discoverListing(ctx context.Context, executable string, env []string, touch func()) ([]string, error) {
	var ids []string
	err := withHome(executable, env, func(dir, exe string, childEnv []string) error {
		var catalogue modelSet
		total, signedOut := 0, false
		diag, err := runCLI(ctx, exe, childEnv, dir, []string{"models"}, nil, func(line []byte) (bool, error) {
			touch()
			if total += len(line); total > maxListBytes {
				return false, ErrProtocol
			}
			signedOut = signedOut || mentionsSignIn(line)
			rest, ok := strings.CutPrefix(string(bytes.TrimSpace(line)), "*")
			if !ok {
				return false, nil
			}
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				return false, nil
			}
			return false, catalogue.add(strings.Trim(fields[0], "(),"))
		})
		if signedOut || (errors.Is(err, errExit) && mentionsSignIn(diag)) {
			return ErrAuthentication
		}
		if errors.Is(err, errExit) {
			return ErrRPC
		}
		if err != nil {
			return err
		}
		ids, err = catalogue.result()
		return err
	})
	return ids, err
}

// modelSet collects real model IDs in CLI order, dropping the "default"
// alias, duplicates and IDs unsafe to pass as an argument.
// modelSet 按 CLI 顺序收集真实模型 ID，去掉 "default" 别名、重复项以及不能
// 安全作为参数传递的 ID。
type modelSet struct {
	ids  []string
	seen map[string]bool
}

func (s *modelSet) add(id string) error {
	id = strings.TrimSpace(id)
	if strings.EqualFold(id, "default") || !modelID.MatchString(id) || s.seen[id] {
		return nil
	}
	if len(s.ids) >= maxModels {
		return ErrProtocol
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[id] = true
	s.ids = append(s.ids, id)
	return nil
}

func (s *modelSet) result() ([]string, error) {
	if len(s.ids) == 0 {
		return nil, ErrProtocol
	}
	return s.ids, nil
}
