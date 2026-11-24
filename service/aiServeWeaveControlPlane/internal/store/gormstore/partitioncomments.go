package gormstore

import (
	"strings"

	"gorm.io/gorm"
)

// PostgreSQL does not inherit comments when creating partitions. Match columns
// by name because an attached partition can have a different column order.
// PostgreSQL 创建分区时不继承注释。按名称匹配列，因为挂接的分区可能使用不同的列顺序。
func copyPostgresPartitionComments(db *gorm.DB, parent string) error {
	const query = `
WITH partitions AS (
    SELECT i.inhparent AS parent, c.oid AS child, n.nspname AS schema_name, c.relname AS table_name
    FROM pg_catalog.pg_inherits i
    JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
    WHERE i.inhparent = pg_catalog.to_regclass(?) AND c.relispartition
)
SELECT pg_catalog.format('COMMENT ON TABLE %I.%I IS %L', schema_name, table_name, description || '（时间分区）')
FROM partitions
CROSS JOIN LATERAL (SELECT pg_catalog.obj_description(parent, 'pg_class') AS description) AS parent_comment
WHERE description IS NOT NULL
  AND pg_catalog.obj_description(child, 'pg_class') IS DISTINCT FROM description || '（时间分区）'
UNION ALL
SELECT pg_catalog.format('COMMENT ON COLUMN %I.%I.%I IS %L', schema_name, table_name, child_column.attname, description)
FROM partitions
JOIN pg_catalog.pg_attribute parent_column ON parent_column.attrelid = parent
JOIN pg_catalog.pg_attribute child_column ON child_column.attrelid = child AND child_column.attname = parent_column.attname
CROSS JOIN LATERAL (SELECT pg_catalog.col_description(parent, parent_column.attnum) AS description) AS parent_comment
WHERE parent_column.attnum > 0 AND NOT parent_column.attisdropped
  AND child_column.attnum > 0 AND NOT child_column.attisdropped
  AND description IS NOT NULL
  AND pg_catalog.col_description(child, child_column.attnum) IS DISTINCT FROM description
`
	var statements []string
	if err := db.Raw(query, parent).Scan(&statements).Error; err != nil {
		return err
	}
	if len(statements) == 0 {
		return nil
	}
	return db.Exec(strings.Join(statements, ";\n")).Error
}
