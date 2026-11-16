package pricecatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// trimKeptFields are the non-price fields Trim keeps besides every field
// whose name mentions a cost.
//
// trimKeptFields 是 Trim 除名称中带 cost 的字段之外保留的非单价字段。
var trimKeptFields = map[string]bool{
	"litellm_provider":  true,
	"mode":              true,
	"max_tokens":        true,
	"max_input_tokens":  true,
	"max_output_tokens": true,
}

// Trim reduces an upstream price list to the entries Parse keeps, each with
// only its cost fields and the fields in trimKeptFields, one entry per line
// in model order so a refreshed snapshot diffs readably. It is how the
// built-in snapshot is made; it shares Parse's rules so the two cannot
// drift.
//
// Trim 把上游价目表裁剪为 Parse 会保留的条目，每个条目只留计价字段与 trimKeptFields
// 中的字段，按模型名排序、每行一个条目，使更新后的快照差异易读。内置快照就是这样生成
// 的；它与 Parse 共用规则，两者不会走样。
func Trim(data []byte) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("pricecatalog: the price list is not a JSON object: %w", err)
	}
	names := make([]string, 0, len(document))
	for name, raw := range document {
		if _, ok := parseEntry(name, raw); ok {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, ErrNoEntries
	}
	sort.Strings(names)
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, name := range names {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(document[name], &fields); err != nil {
			return nil, err
		}
		kept := map[string]json.RawMessage{}
		for field, value := range fields {
			if trimKeptFields[field] || strings.Contains(field, "cost") {
				kept[field] = value
			}
		}
		key, _ := json.Marshal(name)
		value, err := json.Marshal(kept) // map keys marshal sorted / map 的键按顺序序列化
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteString(": ")
		out.Write(value)
		if i < len(names)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}
