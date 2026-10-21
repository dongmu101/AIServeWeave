// Package browserlang defines the shared display-language negotiation contract.
// Package browserlang 定义共享的界面语言匹配契约。
package browserlang

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Locale is a supported browser display preference.
// Locale 是受支持的浏览器显示偏好。
type Locale string

// Default is used when no saved or browser preference matches.
// Default 用于已保存偏好与浏览器偏好都不匹配的情况。
const Default Locale = "zh-CN"

var locales = [...]Locale{"zh-CN", "zh-TW", "en-US", "ja-JP", "ko-KR", "de-DE", "fr-FR", "es-ES"}
var tagPattern = regexp.MustCompile(`(?i)^[a-z]{2,3}(-[a-z0-9]{2,8})*$`)
var qualityPattern = regexp.MustCompile(`(?i)^q=(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$`)

// All returns an independent copy of the supported locale list.
// All 返回支持语言清单的独立副本。
func All() []Locale { return append([]Locale(nil), locales[:]...) }

// Parse accepts only canonical preference-cookie values.
// Parse 只接受规范的偏好 Cookie 值。
func Parse(value string) (Locale, bool) {
	for _, locale := range locales {
		if string(locale) == value {
			return locale, true
		}
	}
	return "", false
}

func matchTag(tag string) (Locale, bool) {
	if !tagPattern.MatchString(tag) {
		return "", false
	}
	parts := strings.Split(strings.ToLower(tag), "-")
	if parts[0] == "zh" {
		for _, p := range parts[1:] {
			if p == "hant" {
				return "zh-TW", true
			}
		}
		for _, p := range parts[1:] {
			if p == "hans" {
				return "zh-CN", true
			}
		}
		for _, p := range parts[1:] {
			if p == "tw" || p == "hk" || p == "mo" {
				return "zh-TW", true
			}
		}
		return Default, true
	}
	m := map[string]Locale{"en": "en-US", "ja": "ja-JP", "ko": "ko-KR", "de": "de-DE", "fr": "fr-FR", "es": "es-ES"}
	value, ok := m[parts[0]]
	return value, ok
}

// Resolve selects a validated saved locale or the highest weighted browser tag.
// Resolve 选择有效的已保存语言，或浏览器权重最高的匹配语言。
func Resolve(saved, acceptLanguage string) Locale {
	if locale, ok := Parse(saved); ok {
		return locale
	}
	if len(acceptLanguage) > 4096 {
		acceptLanguage = acceptLanguage[:4096]
	}
	best, weight := Default, 0.0
	for i, item := range strings.Split(acceptLanguage, ",") {
		if i >= 64 {
			break
		}
		parts := strings.Split(strings.TrimSpace(item), ";")
		q := 1.0
		if len(parts) > 2 {
			continue
		}
		if len(parts) == 2 {
			raw := strings.TrimSpace(parts[1])
			if !qualityPattern.MatchString(raw) {
				continue
			}
			q, _ = strconv.ParseFloat(raw[2:], 64)
		}
		if locale, ok := matchTag(strings.TrimSpace(parts[0])); ok && q > weight {
			best, weight = locale, q
		}
	}
	return best
}

// PreferenceCookie creates a bounded, JS-readable display preference cookie.
// PreferenceCookie 构造受限且可由 JavaScript 读取的显示偏好 Cookie。
func PreferenceCookie(name string, locale Locale, secure bool) *http.Cookie {
	if _, ok := Parse(string(locale)); !ok {
		locale = Default
	}
	return &http.Cookie{Name: name, Value: string(locale), Path: "/", MaxAge: 31536000, SameSite: http.SameSiteLaxMode, Secure: secure}
}
