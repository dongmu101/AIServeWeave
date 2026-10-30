package cliprocess

import (
	"runtime"
	"strings"
)

// KeyEqual compares environment names using the host platform's rules.
// KeyEqual 按宿主平台规则比较环境变量名称。
func KeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// AllowedKey retains only explicit CLI variables and required Windows profile keys.
// AllowedKey 仅保留明确允许的 CLI 变量及必需的 Windows 用户环境。
func AllowedKey(key string, allowed map[string]bool) bool {
	if allowed[key] {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	for name := range allowed {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	switch strings.ToUpper(key) {
	case "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "SYSTEMROOT", "PATHEXT":
		return true
	}
	return false
}
