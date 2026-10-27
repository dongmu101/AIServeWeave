package configui

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"AIServeWeave/common/browserlang"
	"AIServeWeave/common/runtime"
)

const localeCookie = "aisw_agent_locale"

//go:embed i18n/*.json assets/*
var pageAssets embed.FS

var catalogues = loadCatalogues()

// pageText binds an immutable embedded catalogue to one request.
// pageText 为单个请求绑定只读的内置词典。
type pageText struct{ Locale browserlang.Locale }

func loadCatalogues() map[browserlang.Locale]map[string]string {
	all := make(map[browserlang.Locale]map[string]string)
	for _, locale := range browserlang.All() {
		data, err := pageAssets.ReadFile("i18n/" + string(locale) + ".json")
		if err != nil {
			panic("configui: missing language resource")
		}
		var messages map[string]string
		if json.Unmarshal(data, &messages) != nil {
			panic("configui: invalid language resource")
		}
		all[locale] = messages
	}
	for _, messages := range all {
		if len(messages) != len(all[browserlang.Default]) {
			panic("configui: incomplete language resource")
		}
		for key := range all[browserlang.Default] {
			if strings.TrimSpace(messages[key]) == "" {
				panic("configui: empty language resource")
			}
		}
	}
	return all
}

func newPageText(locale browserlang.Locale) pageText {
	if _, ok := browserlang.Parse(string(locale)); !ok {
		locale = browserlang.Default
	}
	return pageText{Locale: locale}
}

// Text returns a fixed embedded UI phrase, rejecting unknown implementation keys.
// Text 返回固定的内置界面文案，拒绝实现中未知的键。
func (p pageText) Text(key string) string {
	locale := p.Locale
	if locale == "" {
		locale = browserlang.Default
	}
	value, ok := catalogues[locale][key]
	if !ok {
		panic("configui: unknown translation key")
	}
	return value
}

// Format substitutes named values as plain text; the template escapes the result.
// Format 以纯文本替换具名参数，模板负责转义结果。
func (p pageText) Format(key string, values map[string]any) string {
	text := p.Text(key)
	for name, value := range values {
		text = strings.ReplaceAll(text, "{"+name+"}", fmt.Sprint(value))
	}
	return text
}

type localeOption struct {
	Locale browserlang.Locale
	Label  string
}

// Options returns language names in their own writing systems.
// Options 返回各语言的自称。
func (p pageText) Options() []localeOption {
	return []localeOption{{"zh-CN", "简体中文"}, {"zh-TW", "繁體中文"}, {"en-US", "English"}, {"ja-JP", "日本語"}, {"ko-KR", "한국어"}, {"de-DE", "Deutsch"}, {"fr-FR", "Français"}, {"es-ES", "Español"}}
}

// Bootstrap contains only validated locale and static messages, never form values.
// Bootstrap 仅包含合法语言与静态词条，绝不包含表单值。
func (p pageText) Bootstrap() template.JS {
	locale := p.Locale
	if locale == "" {
		locale = browserlang.Default
	}
	data, _ := json.Marshal(struct {
		Locale   browserlang.Locale `json:"locale"`
		Messages map[string]string  `json:"messages"`
	}{locale, catalogues[locale]})
	return template.JS(data) // JSON encoding escapes HTML delimiters. / JSON 编码转义 HTML 分隔符。
}

func templateValues(pairs ...any) map[string]any {
	values := make(map[string]any, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		values[fmt.Sprint(pairs[i])] = pairs[i+1]
	}
	return values
}
func jsonText(value any) string { data, _ := json.Marshal(value); return string(data) }

func runtimeStateKey(state runtime.State) string {
	switch state {
	case runtime.StateRegistering:
		return "a0097"
	case runtime.StateUnknown:
		return "a0098"
	case runtime.StateHealthy:
		return "a0099"
	case runtime.StateUnhealthy:
		return "a0100"
	case runtime.StateClosed:
		return "a0101"
	}
	return ""
}
func runtimeStateText(text pageText, state runtime.State) string {
	if key := runtimeStateKey(state); key != "" {
		return text.Text(key)
	}
	return string(state)
}

func requestLocale(r *http.Request) browserlang.Locale {
	saved := ""
	if cookie, err := r.Cookie(localeCookie); err == nil {
		saved = cookie.Value
	}
	return browserlang.Resolve(saved, r.Header.Get("Accept-Language"))
}

func serveLanguageResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	value := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/i18n/"), ".json")
	locale, ok := browserlang.Parse(value)
	if !ok || r.URL.Path != "/i18n/"+value+".json" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Language", string(locale))
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(catalogues[locale])
	}
}
