package gormstore

import (
	"regexp"
	"strings"
	"testing"
)

// TestAgentInstallCodesMigrationCoversModelOnBothDialects checks 0013 exists
// for PostgreSQL and MySQL and declares every column the model maps, with the
// redemption timestamp nullable and the hash as the primary key.
//
// TestAgentInstallCodesMigrationCoversModelOnBothDialects 检查 0013 在 PostgreSQL 与 MySQL
// 上都存在，声明了模型映射的每一列，且兑换时间可空、哈希为主键。
func TestAgentInstallCodesMigrationCoversModelOnBothDialects(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			all, err := loadMigrations("base", dialect)
			if err != nil {
				t.Fatalf("loadMigrations(base, %s): %v", dialect, err)
			}
			var body string
			for _, m := range all {
				if m.id == "0013_agent_install_codes.sql" {
					body = m.sql
				}
			}
			if body == "" {
				t.Fatalf("base/%s has no 0013_agent_install_codes.sql", dialect)
			}
			for _, column := range []string{"code_hash", "node_id", "tenant_id", "registry_addr", "gateway_addr", "created_by", "created_at", "expires_at", "redeemed_at"} {
				if !strings.Contains(body, column) {
					t.Errorf("0013 for %s does not declare column %s", dialect, column)
				}
			}
			if !strings.Contains(body, "code_hash VARCHAR(64) PRIMARY KEY") {
				t.Errorf("0013 for %s must key on code_hash:\n%s", dialect, body)
			}
			if !regexp.MustCompile(`redeemed_at \S+ NULL`).MatchString(body) {
				t.Errorf("0013 for %s must declare redeemed_at as NULL:\n%s", dialect, body)
			}
		})
	}
}
