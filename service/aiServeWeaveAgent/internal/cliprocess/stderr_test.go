package cliprocess_test

import (
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// TestStderrTailCategory reduces the end of standard error to a fixed
// category and keeps only the most recent output.
// TestStderrTailCategory 把标准错误末尾归入固定分类，且只保留最近的输出。
func TestStderrTailCategory(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"empty", "", ""},
		{"whitespace", " \n\t", ""},
		{"login", "Error: Not logged in · Please run /login PRIVATE_MARKER", "login_required"},
		{"unknown feature", "Error: Unknown feature flag: multi_agent", "unknown_option"},
		{"disk", "ENOSPC: no space left on device, write", "disk_full"},
		{"locked", "error: database is locked", "database_locked"},
		{"unrecognized", "something PRIVATE_MARKER happened", "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tail cliprocess.StderrTail
			_, _ = tail.Write([]byte(tc.text))
			if got := tail.Category(); got != tc.want {
				t.Fatalf("category=%q, want %q", got, tc.want)
			}
		})
	}
	var tail cliprocess.StderrTail
	_, _ = tail.Write([]byte("Not logged in\n"))
	for i := 0; i < 100; i++ {
		_, _ = tail.Write([]byte(strings.Repeat("x", 1000) + "\n"))
	}
	if got := tail.Category(); got != "other" {
		t.Fatalf("category=%q, want old output dropped", got)
	}
	_, _ = tail.Write([]byte(strings.Repeat("y", 20000) + " database is locked"))
	if got := tail.Category(); got != "database_locked" {
		t.Fatalf("category=%q, want the end of one large write kept", got)
	}
}
