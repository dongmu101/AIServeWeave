package gormstore

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// partitionDirective marks where a migration needs partition bounds that only
// exist at apply time: the oldest row and the current date. The migration's
// checksum covers the template, not the expansion, so the ledger stays stable.
//
//	/*aisw:partitions table=request_logs*/                 MySQL: inside PARTITION BY (...)
//	/*aisw:partitions table=request_logs source=old_name*/ PostgreSQL: a statement of its own
//
// source names the table holding the existing rows (PostgreSQL renames the old
// table before creating the partitioned one); it defaults to table.
//
// partitionDirective 标记迁移中只有执行时才能确定的分区边界（最旧一行与当前日期）。迁移
// 校验和覆盖的是模板而非展开结果，因此账本保持稳定。source 指存放既有数据的表（PostgreSQL
// 会先把旧表改名再建分区表），默认等于 table。
var partitionDirective = regexp.MustCompile(`/\*aisw:partitions ([^*]*)\*/`)

// partitionMigrationNow is the clock partition expansion reads; tests pin it.
// partitionMigrationNow 是分区展开所用的时钟，测试中可固定。
var partitionMigrationNow = func() time.Time { return time.Now().UTC() }

func isPartitionMigration(m migrationFile) bool {
	return partitionDirective.MatchString(m.sql)
}

type partitionDirectiveArgs struct {
	table  partitionedTable
	source string
}

func parsePartitionDirective(body string) (partitionDirectiveArgs, error) {
	args := map[string]string{}
	for _, field := range strings.Fields(body) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return partitionDirectiveArgs{}, fmt.Errorf("malformed partition directive %q", body)
		}
		args[key] = value
	}
	table, ok := lookupPartitionedTable(args["table"])
	if !ok {
		return partitionDirectiveArgs{}, fmt.Errorf("partition directive names unknown table %q", args["table"])
	}
	source := args["source"]
	if source == "" {
		source = table.name
	}
	return partitionDirectiveArgs{table: table, source: source}, nil
}

// executePartitionMigration runs a migration that converts a table into its
// partitioned form. On MySQL (no transactional DDL) a table that is already
// partitioned means an earlier attempt finished the ALTER, so the file is
// skipped and an explicit resume can complete the ledger.
//
// executePartitionMigration 执行把一张表转换为分区表的迁移。MySQL 的 DDL 不在事务内，
// 若表已分区，说明此前的尝试已完成 ALTER，于是跳过整个文件，由显式 resume 补完账本。
func executePartitionMigration(db *gorm.DB, m migrationFile) error {
	if db.Name() == "mysql" {
		match := partitionDirective.FindStringSubmatch(m.sql)
		args, err := parsePartitionDirective(match[1])
		if err != nil {
			return err
		}
		layout, err := readPartitionLayout(db, args.table.name)
		if err != nil {
			return err
		}
		if layout.partitioned {
			return nil
		}
	}
	for _, statement := range strings.Split(m.sql, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		expanded, err := expandPartitionStatement(db, statement)
		if err != nil {
			return err
		}
		for _, s := range expanded {
			if err := db.Exec(s).Error; err != nil {
				return errors.New("schema statement failed; inspect database schema and server diagnostics")
			}
		}
	}
	return nil
}

// expandPartitionStatement replaces a directive with bounds computed from the
// source table's oldest row and the current time.
//
// expandPartitionStatement 用源表最旧一行与当前时间算出的边界替换占位。
func expandPartitionStatement(db *gorm.DB, statement string) ([]string, error) {
	match := partitionDirective.FindStringSubmatchIndex(statement)
	if match == nil {
		return []string{statement}, nil
	}
	args, err := parsePartitionDirective(statement[match[2]:match[3]])
	if err != nil {
		return nil, err
	}
	oldest, err := oldestPartitionValue(db, args)
	if err != nil {
		return nil, err
	}
	logConversionSize(db, args)
	histUpper, ranges := args.table.planInitialPartitions(oldest, partitionMigrationNow())
	if db.Name() == "mysql" {
		return []string{statement[:match[0]] + mysqlInitialPartitionDefs(args.table, histUpper, ranges) + statement[match[1]:]}, nil
	}
	if strings.TrimSpace(statement[:match[0]]+statement[match[1]:]) != "" {
		return nil, errors.New("a PostgreSQL partition directive must be a statement of its own")
	}
	return postgresInitialPartitionStatements(args.table, histUpper, ranges), nil
}

func oldestPartitionValue(db *gorm.DB, args partitionDirectiveArgs) (*time.Time, error) {
	var oldest sql.NullTime
	if err := db.Raw("SELECT MIN(" + args.table.column + ") FROM " + args.source).Row().Scan(&oldest); err != nil {
		return nil, fmt.Errorf("reading the oldest %s.%s failed", args.source, args.table.column)
	}
	if !oldest.Valid {
		return nil, nil
	}
	t := oldest.Time.UTC()
	return &t, nil
}

// logConversionSize records the catalog's row estimate before a table copy,
// so an operator can tell why a migration is slow and size MigrationTimeout.
//
// logConversionSize 在整表复制前记录目录中的估算行数，便于运维判断迁移为何耗时，并据此
// 设置 MigrationTimeout。
func logConversionSize(db *gorm.DB, args partitionDirectiveArgs) {
	var estimate sql.NullInt64
	query := "SELECT TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?"
	if db.Name() != "mysql" {
		query = "SELECT reltuples::bigint FROM pg_class WHERE oid = to_regclass(?)"
	}
	if err := db.Raw(query, args.source).Row().Scan(&estimate); err != nil {
		return
	}
	slog.Info("converting table to time partitions",
		slog.String("table", args.table.name), slog.Int64("estimated_rows", estimate.Int64))
}
