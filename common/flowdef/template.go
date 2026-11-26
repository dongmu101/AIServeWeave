package flowdef

import (
	"fmt"
	"strings"
)

// InputsScope is the reference scope that names a run-time input rather than
// a node: {{inputs.<name>}}. It is therefore never a valid node id.
//
// InputsScope 是指向运行时输入而不是节点的引用作用域：{{inputs.<name>}}。因此它
// 永远不能用作节点 id。
const InputsScope = "inputs"

// Ref is one variable reference, {{<Scope>.<Field>}}. Scope is InputsScope or
// a node id.
//
// Ref 是一个变量引用 {{<Scope>.<Field>}}。Scope 为 InputsScope 或某个节点 id。
type Ref struct {
	Scope string
	Field string
}

// String renders the reference the way an author writes it, without braces.
// It names only identifiers, never a value, so it is safe in error text.
//
// String 按作者书写的形式输出引用（不含花括号）。它只包含标识符、不含任何取值，
// 因此可以出现在错误文本里。
func (r Ref) String() string { return r.Scope + "." + r.Field }

// Template is a parsed text template: literal text with references in it.
// Substitution is plain replacement — there is no expression evaluation, no
// filter and no code execution.
//
// Template 是解析后的文本模板：带引用的字面文本。替换只是纯文本替换——没有表达式
// 求值、没有过滤器、也不执行任何代码。
type Template struct {
	segments []segment
}

type segment struct {
	literal string
	ref     *Ref
}

// ParseTemplate splits s into literal text and references.
//
// The grammar is deliberately forgiving of text that merely looks like a
// template language, because prompts often quote one: only "{{" followed by
// a dotted identifier path and "}}" (surrounding spaces allowed) is a
// reference candidate. Anything else — {{name}}, {{ a | b }}, JSON, an
// unclosed "{{" — stays literal text. A candidate must have exactly two
// parts; {{a.b.c}} is an error rather than silently literal, since nested
// field access is a plausible thing to try and is not supported.
//
// Whether a reference names something that exists is not checked here; that
// needs the whole definition and is Validate's job.
//
// ParseTemplate 把 s 拆成字面文本与引用。
//
// 语法有意宽容那些只是「看起来像模板语言」的文本，因为提示词里经常引用这类文本：
// 只有「{{ + 以点分隔的标识符路径 + }}」（两侧可有空格）才是候选引用。其他内容——
// {{name}}、{{ a | b }}、JSON、未闭合的「{{」——都按字面文本保留。候选引用必须恰好
// 两段；{{a.b.c}} 报错而不是静默当作字面文本，因为嵌套取字段是用户很可能尝试、
// 但不受支持的写法。
//
// 引用指向的东西是否存在不在这里检查；那需要整份定义，是 Validate 的事。
func ParseTemplate(s string) (Template, error) {
	var t Template
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			t.segments = append(t.segments, segment{literal: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		if !strings.HasPrefix(s[i:], "{{") {
			lit.WriteByte(s[i])
			i++
			continue
		}
		end := strings.Index(s[i+2:], "}}")
		if end < 0 {
			lit.WriteString(s[i:])
			break
		}
		parts, ok := identPath(strings.TrimSpace(s[i+2 : i+2+end]))
		if !ok || len(parts) < 2 {
			// Not a reference: keep one brace and rescan, so "{{{inputs.x}}}"
			// still finds the reference inside the extra braces.
			// 不是引用：保留一个花括号后继续扫描，这样「{{{inputs.x}}}」仍能找到
			// 多余花括号里面的引用。
			lit.WriteByte(s[i])
			i++
			continue
		}
		if len(parts) > 2 {
			return Template{}, fmt.Errorf("reference %q has more than one field; nested fields are not supported", strings.Join(parts, "."))
		}
		flush()
		t.segments = append(t.segments, segment{ref: &Ref{Scope: parts[0], Field: parts[1]}})
		i += 2 + end + 2
	}
	flush()
	return t, nil
}

// identPath splits s on "." and reports whether every part is an identifier.
func identPath(s string) ([]string, bool) {
	parts := strings.Split(s, ".")
	for _, p := range parts {
		if !isIdent(p) {
			return nil, false
		}
	}
	return parts, true
}

// Refs returns the template's references in order of appearance, duplicates
// included.
//
// Refs 按出现顺序返回模板中的引用，包含重复项。
func (t Template) Refs() []Ref {
	var refs []Ref
	for _, s := range t.segments {
		if s.ref != nil {
			refs = append(refs, *s.ref)
		}
	}
	return refs
}

// Render substitutes every reference with value(ref) and returns the result.
//
// Render 用 value(ref) 替换每个引用并返回结果。
func (t Template) Render(value func(Ref) string) string {
	var b strings.Builder
	for _, s := range t.segments {
		if s.ref != nil {
			b.WriteString(value(*s.ref))
			continue
		}
		b.WriteString(s.literal)
	}
	return b.String()
}

// isIdent reports whether s matches [A-Za-z_][A-Za-z0-9_]* within
// MaxIdentLength bytes.
func isIdent(s string) bool {
	if s == "" || len(s) > MaxIdentLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
