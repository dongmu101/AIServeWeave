package workflow

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// MaxInputNameInError bounds how much of a caller-supplied input name is
// repeated back in an error. An unknown-input error has to name the input to
// be actionable, but the name came from the request body and its length is
// the caller's choice, not ours.
//
// MaxInputNameInError 限制错误信息里回述的调用方输入名长度。未声明输入的报错必须
// 点名才有可操作性，但那个名字来自请求体，它的长度是调用方决定的，不是我们。
const MaxInputNameInError = 64

// InputError is a rejected input, carrying the input's name and a fixed
// reason phrase. Both are safe to return to the caller: the name is either
// one the template declared or a truncated echo of what they sent, and the
// reason is drawn from this file, never from a backend.
//
// InputError 是一个被拒绝的输入，携带该输入的名字与一句固定的原因。两者返回给调用方
// 都是安全的：名字要么是模板声明过的，要么是对方所发内容的截断回述；原因来自本文件，
// 从不来自后端。
type InputError struct {
	Name   string
	Reason string
}

func (e *InputError) Error() string {
	return fmt.Sprintf("workflow: input %q %s", e.Name, e.Reason)
}

// Bind substitutes values into a copy of the template's graph and returns the
// API-format workflow to submit. The stored template is never modified, so
// one registry entry serves concurrent requests.
//
// Values must be exactly the declared inputs: an undeclared name is an error
// rather than something to ignore, since silently dropping it would let a
// caller believe they had changed something they had not.
//
// Bind 把取值代入模板图的一份副本，返回可提交的 API Format 工作流。存储的模板从不被
// 修改，因此一个目录条目可以服务并发请求。
//
// values 必须正好是那些已声明的输入：未声明的名字是错误而不是可以忽略的东西——默默
// 丢掉它，会让调用方以为自己改动了实际上没改动的东西。
func (t *Template) Bind(values map[string]json.RawMessage) (json.RawMessage, error) {
	declared := make(map[string]Input, len(t.Inputs))
	for _, in := range t.Inputs {
		declared[in.Name] = in
	}
	// Sorted so a request with several unknown names always fails on the same
	// one; an error that varies run to run is harder to act on.
	//
	// 排序是为了让含多个未知名字的请求每次都在同一个上失败；每次都不一样的报错更难
	// 处理。
	for _, name := range sortedKeys(values) {
		if _, ok := declared[name]; !ok {
			return nil, &InputError{Name: truncate(name, MaxInputNameInError), Reason: "is not declared by this workflow"}
		}
	}

	g, err := parseGraph(t.Graph)
	if err != nil {
		// Validate already rejected this at load time, so reaching here means
		// the template was built in memory and never validated.
		//
		// Validate 在加载时已经拒绝过这种图，因此走到这里说明该模板是在内存里构造
		// 且从未校验过的。
		return nil, fmt.Errorf("workflow: template %q: %w", t.ID, err)
	}

	for _, in := range t.Inputs {
		raw, given := values[in.Name]
		if !given {
			if len(in.Default) > 0 {
				raw = in.Default
			} else if in.Required {
				return nil, &InputError{Name: in.Name, Reason: "is required"}
			} else {
				continue
			}
		}
		encoded, err := in.coerce(raw)
		if err != nil {
			return nil, &InputError{Name: in.Name, Reason: err.Error()}
		}
		g.inputs[in.Node][in.Field] = encoded
	}
	return g.marshal()
}

// coerce checks raw against the input's declared type and bounds, and returns
// the value to write into the graph. The returned bytes are re-encoded from
// the parsed value rather than passed through, so nothing a caller wrapped in
// whitespace or exotic number formatting reaches the backend verbatim.
//
// coerce 按输入声明的类型与边界检查 raw，并返回要写进图里的取值。返回的字节是由解析
// 后的值重新编码而来，而不是原样透传，因此调用方用空白或古怪数字格式包装的内容不会
// 原封不动地抵达后端。
func (in Input) coerce(raw json.RawMessage) (json.RawMessage, error) {
	switch in.Type {
	case InputString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("must be a string")
		}
		limit := in.MaxLength
		if limit <= 0 {
			limit = DefaultMaxStringLength
		}
		if len(s) > limit {
			return nil, fmt.Errorf("must be at most %d bytes", limit)
		}
		return json.Marshal(s)

	case InputInteger:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		if err := in.checkBounds(float64(n)); err != nil {
			return nil, err
		}
		return json.Marshal(n)

	case InputNumber:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("must be a number")
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("must be a finite number")
		}
		if err := in.checkBounds(n); err != nil {
			return nil, err
		}
		return json.Marshal(n)

	case InputBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("must be a boolean")
		}
		return json.Marshal(b)

	default:
		// Validate rejects unknown types at load time; this keeps an
		// unvalidated template from binding one anyway.
		//
		// Validate 在加载时就拒绝未知类型；这一支防止未经校验的模板仍然把它绑进去。
		return nil, fmt.Errorf("has unknown type %q", in.Type)
	}
}

// checkBounds applies Min and Max, both inclusive.
//
// checkBounds 应用 Min 与 Max，两端均为闭区间。
func (in Input) checkBounds(v float64) error {
	if in.Min != nil && v < *in.Min {
		return fmt.Errorf("must be at least %v", *in.Min)
	}
	if in.Max != nil && v > *in.Max {
		return fmt.Errorf("must be at most %v", *in.Max)
	}
	return nil
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
