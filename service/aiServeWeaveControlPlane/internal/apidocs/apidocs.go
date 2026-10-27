// Package apidocs defines the tenant-facing API reference: the shape of one
// documented endpoint, the rules a document must satisfy before it is stored,
// and the seed set compiled into the binary.
//
// The documents live in the database so a new model or capability is a row
// edit, not a Console release. The seed set exists so a fresh database is not
// an empty reference, and so a developer who changes a Gateway endpoint can
// ship the matching documentation in the same commit: bump the seed's
// revision and every control plane that still holds the untouched seed picks
// it up on its next start. A document an operator has edited is never
// overwritten by a seed — see SyncAPIDocSeeds in the logic package.
//
// Package apidocs 定义面向租户的 API 参考文档：一个被文档化的端点长什么样、
// 一份文档入库前必须满足的规则，以及编译进二进制的种子集。
//
// 文档存放在数据库里，因此新增一个模型或能力只需改一行数据，而不必发布一次
// Console。种子集的存在，是为了让全新的数据库不至于得到一份空白参考，也让修改了
// Gateway 端点的开发者能在同一个提交里带上相应文档：提高种子的 revision，所有仍
// 持有未被改动过的该种子的控制面，会在下次启动时取到新版本。运维编辑过的文档
// 永远不会被种子覆盖——见 logic 包的 SyncAPIDocSeeds。
package apidocs

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxContentBytes bounds one document's encoded Content. The largest seeded
// page is a few tens of kilobytes; the bound leaves room for long examples
// without letting one row grow without limit.
//
// MaxContentBytes 限定单份文档编码后 Content 的大小。最大的种子页面只有几十 KB，
// 这个上限给长示例留足空间，又不让单行无限增长。
const MaxContentBytes = 256 << 10

// MaxRequestBytes bounds an operator's create/update request body: the
// content plus the metadata around it and JSON escaping overhead.
//
// MaxRequestBytes 限定运维创建/更新请求体的大小：内容本身，加上外围元数据与
// JSON 转义带来的膨胀。
const MaxRequestBytes = 2 * MaxContentBytes

// maxParamDepth bounds Param nesting, so a hostile or mistaken document
// cannot make a renderer recurse without limit.
//
// maxParamDepth 限定 Param 的嵌套深度，使恶意或有误的文档无法让渲染方无限递归。
const maxParamDepth = 6

// Document statuses. Only published documents are visible to tenants.
//
// 文档状态。只有已发布的文档对租户可见。
const (
	StatusPublished = "published"
	StatusDraft     = "draft"
)

// Document sources. A seed-sourced row may be refreshed by a newer seed
// revision; an operator-sourced row never is.
//
// 文档来源。来源为 seed 的行可以被更高 revision 的种子刷新；来源为 operator 的行
// 永远不会。
const (
	SourceSeed     = "seed"
	SourceOperator = "operator"
)

// ErrInvalid is returned for a document that breaks one of Validate's rules.
//
// ErrInvalid 在文档违反 Validate 任一规则时返回。
var ErrInvalid = errors.New("apidocs: invalid document")

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

var validMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
var validAuth = map[string]bool{"bearer": true, "none": true}
var validBodyTypes = map[string]bool{"json": true, "multipart": true, "none": true}

// Meta is the part of a document a navigation list needs: everything except
// the page body.
//
// Meta 是导航列表所需的那部分文档：除页面正文以外的一切。
type Meta struct {
	// Category is the navigation group's display name, e.g. "文本对话".
	// / Category 是导航分组的展示名，例如 "文本对话"。
	Category string `json:"category"`
	// CategoryOrder sorts groups; a group sorts by its smallest value.
	// / CategoryOrder 用于分组排序；一个分组按其中最小的值排序。
	CategoryOrder int64 `json:"category_order"`
	// SortOrder sorts documents within a group. / SortOrder 用于组内文档排序。
	SortOrder int64 `json:"sort_order"`
	// Title is the page heading. / Title 是页面标题。
	Title string `json:"title"`
	// Method is the HTTP method the endpoint takes. / Method 是端点接受的 HTTP 方法。
	Method string `json:"method"`
	// Path is the Gateway path, relative to the base URL. / Path 是相对于基地址的 Gateway 路径。
	Path string `json:"path"`
	// Summary is one sentence for the list. / Summary 是供列表展示的一句话概述。
	Summary string `json:"summary"`
}

// Content is one documented endpoint's page body. Code and example bodies may
// carry the placeholders {{BASE_URL}}, {{API_KEY}} and {{MODEL}}, which the
// Console substitutes at render time.
//
// Content 是一个被文档化端点的页面正文。代码与示例正文中可以带有占位符
// {{BASE_URL}}、{{API_KEY}} 与 {{MODEL}}，由 Console 在渲染时替换。
type Content struct {
	// Highlights are the bullet points under the title. / Highlights 是标题下方的要点列表。
	Highlights []string `json:"highlights"`
	// Notice is an optional call-out paragraph. / Notice 是可选的提示框段落。
	Notice string `json:"notice,omitempty"`
	// Auth is "bearer" or "none". / Auth 取 "bearer" 或 "none"。
	Auth string `json:"auth"`
	// Headers are request headers beyond Authorization. / Headers 是 Authorization 之外的请求头。
	Headers []Param `json:"headers"`
	// PathParams are the path's {placeholders}. / PathParams 是路径里的 {占位符}。
	PathParams []Param `json:"path_params"`
	// QueryParams are URL query parameters. / QueryParams 是 URL 查询参数。
	QueryParams []Param `json:"query_params"`
	// BodyType is "json", "multipart" or "none". / BodyType 取 "json"、"multipart" 或 "none"。
	BodyType string `json:"body_type"`
	// BodyParams are the request body's fields. / BodyParams 是请求体字段。
	BodyParams []Param `json:"body_params"`
	// ResponseFields are the success response's fields. / ResponseFields 是成功响应的字段。
	ResponseFields []Param `json:"response_fields"`
	// RequestExamples are code samples, one per language tab. / RequestExamples 是代码示例，每个语言一个标签页。
	RequestExamples []CodeExample `json:"request_examples"`
	// ResponseExamples are example responses, one per status tab. / ResponseExamples 是示例响应，每个状态码一个标签页。
	ResponseExamples []ResponseExample `json:"response_examples"`
	// Sections are free-form trailing sections. / Sections 是页面末尾的自由段落。
	Sections []Section `json:"sections"`
}

// Param documents one field, header or parameter. Children describe an
// object's or an array element's fields.
//
// Param 描述一个字段、请求头或参数。Children 描述对象或数组元素的下级字段。
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
	Children    []Param  `json:"children,omitempty"`
}

// CodeExample is one language's request sample. / CodeExample 是一种语言的请求示例。
type CodeExample struct {
	// Lang is a highlighter hint such as "curl" or "python". / Lang 是 "curl"、"python" 这类高亮提示。
	Lang string `json:"lang"`
	// Label is the tab title. / Label 是标签页标题。
	Label string `json:"label"`
	Code  string `json:"code"`
}

// ResponseExample is one status code's sample response.
//
// ResponseExample 是某个状态码的示例响应。
type ResponseExample struct {
	Status      int    `json:"status"`
	Label       string `json:"label"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body"`
}

// Section is a titled block of plain-text paragraphs (separated by a blank
// line) with an optional code block.
//
// Section 是一个带标题的纯文本段落块（段落之间以空行分隔），可附一个代码块。
type Section struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Code     string `json:"code,omitempty"`
	CodeLang string `json:"code_lang,omitempty"`
}

// ValidSlug reports whether slug is a well-formed document key: lowercase
// letters, digits and hyphens, starting with a letter or digit.
//
// ValidSlug 报告 slug 是否为合法的文档键：小写字母、数字与连字符，以字母或数字开头。
func ValidSlug(slug string) bool { return slugPattern.MatchString(slug) }

// ValidStatus reports whether status is one of the document statuses.
//
// ValidStatus 报告 status 是否为文档状态之一。
func ValidStatus(status string) bool { return status == StatusPublished || status == StatusDraft }

// Validate checks meta and content and returns content's canonical JSON
// encoding, the form it is stored in. It returns ErrInvalid for any broken
// rule.
//
// Validate 校验 meta 与 content，并返回 content 的规范 JSON 编码——也就是它的
// 存储形式。违反任一规则都返回 ErrInvalid。
func Validate(meta Meta, content Content) (string, error) {
	if !textOK(meta.Category, 1, 64) || !textOK(meta.Title, 1, 200) || !textOK(meta.Summary, 0, 500) ||
		!validMethods[meta.Method] || !strings.HasPrefix(meta.Path, "/") || !textOK(meta.Path, 1, 255) ||
		meta.CategoryOrder < -1_000_000 || meta.CategoryOrder > 1_000_000 ||
		meta.SortOrder < -1_000_000 || meta.SortOrder > 1_000_000 {
		return "", ErrInvalid
	}
	if !validAuth[content.Auth] || !validBodyTypes[content.BodyType] || !textOK(content.Notice, 0, 4000) {
		return "", ErrInvalid
	}
	for _, h := range content.Highlights {
		if !textOK(h, 1, 500) {
			return "", ErrInvalid
		}
	}
	for _, list := range [][]Param{content.Headers, content.PathParams, content.QueryParams, content.BodyParams, content.ResponseFields} {
		if !paramsOK(list, 1) {
			return "", ErrInvalid
		}
	}
	for _, ex := range content.RequestExamples {
		if !textOK(ex.Lang, 1, 32) || !textOK(ex.Label, 1, 64) || ex.Code == "" {
			return "", ErrInvalid
		}
	}
	for _, ex := range content.ResponseExamples {
		if ex.Status < 100 || ex.Status > 599 || !textOK(ex.Label, 0, 64) || !textOK(ex.ContentType, 0, 128) {
			return "", ErrInvalid
		}
	}
	for _, sec := range content.Sections {
		if !textOK(sec.Title, 1, 200) || (sec.Body == "" && sec.Code == "") || !textOK(sec.CodeLang, 0, 32) {
			return "", ErrInvalid
		}
	}
	encoded, err := json.Marshal(content)
	if err != nil || len(encoded) > MaxContentBytes {
		return "", ErrInvalid
	}
	return string(encoded), nil
}

func paramsOK(params []Param, depth int) bool {
	if depth > maxParamDepth {
		return false
	}
	for _, p := range params {
		if !textOK(p.Name, 1, 128) || !textOK(p.Type, 1, 64) || !textOK(p.Default, 0, 256) || !textOK(p.Description, 0, 4000) {
			return false
		}
		for _, e := range p.Enum {
			if !textOK(e, 1, 128) {
				return false
			}
		}
		if !paramsOK(p.Children, depth+1) {
			return false
		}
	}
	return true
}

// textOK reports whether s is valid UTF-8 with a rune count in [min, max]
// after trimming surrounding whitespace for the minimum check.
//
// textOK 报告 s 是否为合法 UTF-8，且字符数位于 [min, max]；下限检查前先去掉首尾空白。
func textOK(s string, min, max int) bool {
	if !utf8.ValidString(s) {
		return false
	}
	if min > 0 && strings.TrimSpace(s) == "" {
		return false
	}
	return utf8.RuneCountInString(s) <= max
}

// DecodeContent parses a stored content_json value.
//
// DecodeContent 解析一个已存储的 content_json 值。
func DecodeContent(raw string) (Content, error) {
	var c Content
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Content{}, err
	}
	return c, nil
}
