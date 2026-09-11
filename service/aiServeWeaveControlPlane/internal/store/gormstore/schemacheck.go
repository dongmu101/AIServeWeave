package gormstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// checkSchemaObjects validates the mapped columns and declared indexes without DDL.
// checkSchemaObjects 校验映射列与声明索引，不执行 DDL。
func checkSchemaObjects(db *gorm.DB, all bool) error {
	models := []any{&model.Tenant{}, &model.User{}, &model.PlatformOperator{}, &model.APIKey{}, &model.AuditLog{}, &revocationOutbox{}}
	if all {
		models = append(models, &model.RouteRevision{}, &model.RouteActive{}, &model.WorkflowTemplateRevision{}, &model.WorkflowTemplateActive{})
		if db.Name() == "mysql" {
			models = append(models, &model.Job{}, &model.JobArtifact{})
		}
	}
	for _, value := range models {
		if err := checkModelSchema(db, value); err != nil {
			return err
		}
	}
	var row struct {
		Generation          sql.NullInt64
		DeliveredGeneration sql.NullInt64
	}
	if err := db.Table("key_revocation_outbox").Where("id = 1").Take(&row).Error; err != nil {
		return errors.New("revocation outbox singleton is missing or unreadable")
	}
	if !row.Generation.Valid || !row.DeliveredGeneration.Valid || row.Generation.Int64 < 0 || row.DeliveredGeneration.Int64 < 0 || row.DeliveredGeneration.Int64 > row.Generation.Int64 {
		return errors.New("revocation outbox generations are inconsistent")
	}
	return nil
}

func checkModelSchema(db *gorm.DB, value any) error {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(value); err != nil {
		return err
	}
	table := statement.Schema.Table
	if db.Name() == "mysql" {
		var engine string
		if err := db.Raw("SELECT engine FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&engine).Error; err != nil || engine != "InnoDB" {
			return fmt.Errorf("schema table %s must use InnoDB", table)
		}
	}
	columns, err := db.Migrator().ColumnTypes(value)
	if err != nil {
		return fmt.Errorf("schema table %s is missing or unreadable", table)
	}
	byName := make(map[string]gorm.ColumnType, len(columns))
	primaryColumns := make(map[string]bool)
	for _, column := range columns {
		byName[column.Name()] = column
		if primary, known := column.PrimaryKey(); known && primary {
			primaryColumns[column.Name()] = true
		}
	}
	if len(primaryColumns) != len(statement.Schema.PrimaryFields) {
		return fmt.Errorf("schema primary key for %s is incompatible", table)
	}
	for _, field := range statement.Schema.Fields {
		if field.DBName == "" {
			continue
		}
		column, found := byName[field.DBName]
		compatible := found
		if found {
			kind := strings.ToLower(column.DatabaseTypeName())
			switch field.DataType {
			case schema.String:
				compatible = strings.Contains(kind, "char") || strings.Contains(kind, "text")
				size := field.Size
				if size == 0 {
					switch field.DBName {
					case "digest":
						size = 64
					case "actor_id":
						size = 32
					case "description":
						compatible = kind == "text" || kind == "mediumtext" || kind == "longtext"
					default:
						if strings.HasSuffix(field.DBName, "_json") {
							compatible = kind == "text"
							if db.Name() == "mysql" {
								compatible = kind == "mediumtext" || kind == "longtext"
							}
						}
					}
				}
				if length, ok := column.Length(); ok && size > 0 && length < int64(size) {
					compatible = false
				}
			case schema.Int, schema.Uint:
				compatible = kind == "bigint" || kind == "int8"
				if table == "route_actives" && field.DBName == "id" {
					compatible = strings.Contains(kind, "int")
				}
			case schema.Time:
				compatible = strings.Contains(kind, "timestamp") || kind == "timestamptz" || kind == "datetime"
			}
			// These SQL-owned models intentionally omit most GORM DDL tags.
			// 这些由固定 SQL 管理的模型刻意省略了大部分 GORM DDL 标签。
			required := field.NotNull || field.PrimaryKey || table == "key_revocation_outbox" ||
				strings.HasPrefix(table, "route_") || strings.HasPrefix(table, "workflow_template_") ||
				(table == "jobs" && field.DBName != "terminal_at") || table == "job_artifacts"
			if nullable, ok := column.Nullable(); ok && nullable && required {
				compatible = false
			}
			if field.PrimaryKey {
				if !primaryColumns[field.DBName] {
					compatible = false
				}
			}
		}
		if !compatible {
			return fmt.Errorf("schema column %s.%s is missing or incompatible; inspect before migration or restore", table, field.DBName)
		}
	}
	indexes, err := db.Migrator().GetIndexes(value)
	if err != nil {
		return fmt.Errorf("reading indexes for %s failed", table)
	}
	for _, want := range statement.Schema.ParseIndexes() {
		found := false
		for _, index := range indexes {
			if index.Name() != want.Name || len(index.Columns()) < len(want.Fields) || (want.Class == "UNIQUE" && len(index.Columns()) != len(want.Fields)) {
				continue
			}
			found = true
			for i, field := range want.Fields {
				found = found && index.Columns()[i] == field.DBName
			}
			unique, known := index.Unique()
			if want.Class == "UNIQUE" && (!known || !unique) {
				found = false
			}
			break
		}
		if !found {
			return fmt.Errorf("schema index %s.%s is missing or incompatible", table, want.Name)
		}
	}
	if table == "jobs" {
		for _, want := range []struct {
			name    string
			columns []string
		}{
			{"idx_jobs_tenant_created", []string{"tenant_id", "created_at", "id"}},
			{"idx_jobs_node_runtime", []string{"node_id", "runtime_id", "state"}},
		} {
			found := false
			for _, index := range indexes {
				if index.Name() == want.name && strings.Join(index.Columns(), ",") == strings.Join(want.columns, ",") {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("schema index jobs.%s is missing or incompatible", want.name)
			}
		}
	}
	return nil
}
