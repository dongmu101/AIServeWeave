package flow

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"AIServeWeave/common/flowdef"
)

// compare evaluates a condition. Both operands are trimmed first, since a
// model reply usually ends in a newline. It reports false when a numeric
// operator meets an operand that is not a number.
//
// compare 对条件求值。两个操作数先去掉首尾空白，因为模型回复通常以换行结尾。数值比较
// 遇到不是数字的操作数时报告 false。
func compare(op flowdef.Operator, left, right string) (result, ok bool) {
	l, r := strings.TrimSpace(left), strings.TrimSpace(right)
	switch op {
	case flowdef.OpEquals:
		return l == r, true
	case flowdef.OpNotEquals:
		return l != r, true
	case flowdef.OpContains:
		return strings.Contains(l, r), true
	case flowdef.OpNotContains:
		return !strings.Contains(l, r), true
	case flowdef.OpEmpty:
		return l == "", true
	case flowdef.OpNotEmpty:
		return l != "", true
	}
	a, errA := strconv.ParseFloat(l, 64)
	b, errB := strconv.ParseFloat(r, 64)
	if errA != nil || errB != nil {
		return false, false
	}
	switch op {
	case flowdef.OpGT:
		return a > b, true
	case flowdef.OpGTE:
		return a >= b, true
	case flowdef.OpLT:
		return a < b, true
	case flowdef.OpLTE:
		return a <= b, true
	}
	return false, false
}

func branchOf(b bool) flowdef.Branch {
	if b {
		return flowdef.BranchTrue
	}
	return flowdef.BranchFalse
}

// withJSONInstruction appends to the system prompt the instruction that asks
// for structured output. Structured output is requested through the prompt
// rather than a response_format parameter because many of the platform's
// models — Claude Code and Codex accounts among them — do not support one;
// parseJSONFields is lenient to match.
//
// withJSONInstruction 在系统提示词末尾追加要求结构化输出的说明。之所以通过提示词而不是
// response_format 参数请求结构化输出，是因为平台上的许多模型——包括 Claude Code 与
// Codex 账号——不支持该参数；parseJSONFields 相应地宽容解析。
func withJSONInstruction(system string, fields []flowdef.JSONField) string {
	var b strings.Builder
	if strings.TrimSpace(system) != "" {
		b.WriteString(system)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply with a single JSON object and nothing else. It must have exactly these fields:\n")
	for _, f := range fields {
		fmt.Fprintf(&b, "- %q (%s)", f.Name, jsonTypeName(f.Type))
		if d := strings.TrimSpace(f.Description); d != "" {
			b.WriteString(": ")
			b.WriteString(d)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func jsonTypeName(t flowdef.ValueType) string {
	if t == flowdef.TypeText {
		return "string"
	}
	return string(t)
}

// parseJSONFields extracts the declared fields from a reply. It takes the
// outermost {...} in the text, so a code fence or a sentence around the
// object is tolerated, and accepts a field value in its declared type or as
// a string that parses as that type. A missing, null or mistyped field fails
// the parse.
//
// parseJSONFields 从回复中取出声明的字段。它取文本中最外层的 {...}，因此容忍对象外的
// 代码块围栏或句子；字段值可以是声明的类型，也可以是能解析为该类型的字符串。字段缺失、
// 为 null 或类型不符时解析失败。
func parseJSONFields(text string, fields []flowdef.JSONField, maxBytes int) (map[string]string, bool) {
	i, j := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if i < 0 || j < i {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(text[i:j+1]), &obj) != nil {
		return nil, false
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		v, ok := fieldText(f.Type, obj[f.Name])
		if !ok {
			return nil, false
		}
		out[f.Name] = truncate(v, maxBytes)
	}
	return out, true
}

func fieldText(t flowdef.ValueType, raw json.RawMessage) (string, bool) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	isString := json.Unmarshal(raw, &s) == nil
	switch t {
	case flowdef.TypeText:
		if isString {
			return s, true
		}
		// A number or boolean where text was asked for is still usable text.
		// 要求文本却得到数字或布尔值时，它仍是可用的文本。
		var scalar any
		if json.Unmarshal(raw, &scalar) != nil {
			return "", false
		}
		switch scalar.(type) {
		case float64, bool:
			return string(raw), true
		}
		return "", false
	case flowdef.TypeNumber:
		if isString {
			raw = json.RawMessage(strings.TrimSpace(s))
		}
		f, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatFloat(f, 'f', -1, 64), true
	case flowdef.TypeBoolean:
		if isString {
			raw = json.RawMessage(strings.ToLower(strings.TrimSpace(s)))
		}
		switch string(raw) {
		case "true", "false":
			return string(raw), true
		}
		return "", false
	}
	return "", false
}

// truncate cuts s to at most max bytes on a rune boundary.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
