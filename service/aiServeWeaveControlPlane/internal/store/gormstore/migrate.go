package gormstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

//go:embed migrations
var migrationsFS embed.FS

type migrationFile struct {
	id  string
	sql string
}

type migrationRecord struct {
	ID        string `gorm:"primaryKey"`
	Checksum  string
	Dirty     bool
	AppliedAt time.Time
}

// MigrationStatus describes one embedded version and its database state.
// MigrationStatus 描述一个内嵌版本及其数据库状态。
type MigrationStatus struct {
	// Namespace selects the independently versioned schema group. / Namespace 表示独立版本化的表结构分组。
	Namespace string `json:"namespace"`
	// ID is the immutable embedded SQL filename. / ID 是不可变的内嵌 SQL 文件名。
	ID string `json:"id"`
	// State is pending, legacy, dirty, or applied. / State 取 pending、legacy、dirty 或 applied。
	State string `json:"state"`
	// Checksum is the embedded SQL's SHA-256 digest. / Checksum 是内嵌 SQL 的 SHA-256 摘要。
	Checksum string `json:"checksum"`
	// AppliedAt is completion time, or attempt time for a dirty version. / AppliedAt 是完成时间；dirty 版本记录尝试时间。
	AppliedAt time.Time `json:"applied_at,omitempty"`
}

func migrationChecksum(body string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
}

func migrationNamespaces(dialect string) []string {
	groups := []string{"base", "routes", "workflow_templates", "metrics_history"}
	if dialect == "mysql" {
		groups = append(groups, "jobs")
	}
	return groups
}

func loadMigrations(namespace, dialect string) ([]migrationFile, error) {
	dir := "migrations/" + namespace + "/" + dialect
	switch namespace {
	case "jobs":
		dir = "migrations/jobs"
	case "workflow_templates":
		dir = "migrations/workflowtemplates/" + dialect
	}
	entries, err := migrationsFS.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	all := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		body, err := migrationsFS.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			return nil, err
		}
		all = append(all, migrationFile{id: entry.Name(), sql: string(body)})
	}
	return all, nil
}

func validateMigrationHistory(all []migrationFile, records []migrationRecord, resume bool) error {
	known := make(map[string]migrationFile, len(all))
	for _, m := range all {
		known[m.id] = m
	}
	applied := make(map[string]migrationRecord, len(records))
	for _, r := range records {
		m, ok := known[r.ID]
		if !ok {
			return errors.New("unknown database migration; use the matching application version")
		}
		if r.Checksum != migrationChecksum(m.sql) {
			return fmt.Errorf("migration %s checksum mismatch", m.id)
		}
		if r.Dirty && !resume {
			return fmt.Errorf("migration %s is dirty; inspect the database before explicit resume", m.id)
		}
		applied[r.ID] = r
	}
	gap := false
	for _, m := range all {
		r, ok := applied[m.id]
		if ok && gap {
			return errors.New("migration history is not a completed prefix")
		}
		if !ok || r.Dirty {
			gap = true
		}
	}
	return nil
}

// withMigrationLock pins the session lock to the connection executing DDL.
// withMigrationLock 将会话锁绑定到真正执行 DDL 的连接。
func (s *Store) withMigrationLock(ctx context.Context, run func(*gorm.DB) error) error {
	if name := s.db.Name(); name != "mysql" && name != "postgres" {
		return errors.New("gormstore: migrations require postgres or mysql")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return s.db.WithContext(ctx).Connection(func(db *gorm.DB) (result error) {
		db = db.Session(&gorm.Session{NewDB: true})
		var acquired bool
		lockSQL := "SELECT pg_try_advisory_lock(71407001)"
		unlockSQL := "SELECT pg_advisory_unlock(71407001)"
		if db.Name() == "mysql" {
			lockSQL = "SELECT GET_LOCK(CONCAT('aisw:', LEFT(SHA2(DATABASE(), 256), 48)), 0)"
			unlockSQL = "SELECT RELEASE_LOCK(CONCAT('aisw:', LEFT(SHA2(DATABASE(), 256), 48)))"
		}
		if err := db.Raw(lockSQL).Scan(&acquired).Error; err != nil || !acquired {
			return errors.New("database migration lock unavailable; retry after the other migrator finishes")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			var released bool
			if err := db.WithContext(cleanup).Raw(unlockSQL).Scan(&released).Error; err != nil || !released {
				// Discard an uncertain session instead of returning its lock to the pool.
				// 丢弃状态不明的会话，避免把仍持锁的连接交回连接池。
				if conn, ok := db.Statement.ConnPool.(*sql.Conn); ok {
					_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				}
				result = errors.Join(result, errors.New("releasing database migration lock failed"))
			}
		}()
		return run(db)
	})
}

func ensureMigrationTable(db *gorm.DB, table string) error {
	stamp, suffix := "TIMESTAMPTZ", ""
	if db.Name() == "mysql" {
		stamp, suffix = "DATETIME(6)", " ENGINE=InnoDB"
	}
	if err := db.Exec("CREATE TABLE IF NOT EXISTS " + table + " (id VARCHAR(255) PRIMARY KEY, applied_at " + stamp + " NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE)" + suffix).Error; err != nil {
		return errors.New("creating migration history failed")
	}
	// Old namespaces had only id/applied_at. These additive metadata steps are replayable.
	// 旧迁移账本只有 id/applied_at；以下元数据增量可重复执行。
	for _, column := range []struct{ name, definition string }{
		{"checksum", "VARCHAR(64) NOT NULL DEFAULT ''"}, {"dirty", "BOOLEAN NOT NULL DEFAULT FALSE"},
	} {
		if !db.Migrator().HasColumn(table, column.name) {
			if err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column.name + " " + column.definition).Error; err != nil {
				return errors.New("upgrading migration history failed")
			}
		}
	}
	return nil
}

func readMigrationRecords(db *gorm.DB, table string) ([]migrationRecord, error) {
	if !db.Migrator().HasTable(table) {
		return nil, nil
	}
	columns := "id, applied_at"
	if db.Migrator().HasColumn(table, "checksum") && db.Migrator().HasColumn(table, "dirty") {
		columns += ", checksum, dirty"
	}
	var records []migrationRecord
	err := db.Table(table).Select(columns).Order("id").Limit(1001).Find(&records).Error
	if err != nil {
		return nil, errors.New("reading migration history failed")
	}
	return records, nil
}

func migrateNamespace(db *gorm.DB, namespace string, resume bool) ([]string, error) {
	all, err := loadMigrations(namespace, db.Name())
	if err != nil {
		return nil, err
	}
	table := "schema_migrations_" + namespace
	legacy := namespace != "base" && db.Migrator().HasTable(table) &&
		(!db.Migrator().HasColumn(table, "checksum") || !db.Migrator().HasColumn(table, "dirty"))
	if err := ensureMigrationTable(db, table); err != nil {
		return nil, err
	}
	records, err := readMigrationRecords(db, table)
	if err != nil {
		return nil, err
	}
	validation := records
	if legacy || (resume && namespace != "base") {
		validation = adoptableMigrationRecords(all, records)
	}
	if err := validateMigrationHistory(all, validation, resume); err != nil {
		return nil, err
	}
	byID := make(map[string]migrationRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	var applied []string
	for _, m := range all {
		r, found := byID[m.id]
		if found && !r.Dirty {
			if r.Checksum == "" {
				if err := db.Table(table).Where("id = ?", m.id).Update("checksum", migrationChecksum(m.sql)).Error; err != nil {
					return applied, errors.New("adopting legacy migration history failed")
				}
			}
			continue
		}
		apply := func(tx *gorm.DB) error {
			record := migrationRecord{ID: m.id, Checksum: migrationChecksum(m.sql), Dirty: true, AppliedAt: time.Now().UTC()}
			if err := tx.Table(table).Clauses(clause.OnConflict{UpdateAll: true}).Create(&record).Error; err != nil {
				return errors.New("recording pending migration failed")
			}
			if err := executeMigration(tx, namespace, m); err != nil {
				return err
			}
			if err := tx.Table(table).Where("id = ?", m.id).Updates(map[string]any{"dirty": false, "applied_at": time.Now().UTC()}).Error; err != nil {
				return errors.New("recording completed migration failed")
			}
			return nil
		}
		if db.Name() == "postgres" {
			err = db.Transaction(apply)
		} else {
			err = apply(db)
		}
		if err != nil {
			return applied, fmt.Errorf("migration %s/%s failed: %w", namespace, m.id, err)
		}
		applied = append(applied, m.id)
	}
	if namespace == "routes" {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.RouteActive{ID: 1}).Error; err != nil {
			return applied, errors.New("seeding routing active pointer failed")
		}
	}
	return applied, nil
}

// executeMigration handles only the repository's fixed SQL, not user input.
// executeMigration 只执行仓库固定 SQL，不接受用户输入。
func executeMigration(db *gorm.DB, namespace string, m migrationFile) error {
	if namespace == "jobs" {
		switch m.id {
		case "0001_create_jobs.sql":
			if db.Migrator().HasTable("jobs") {
				return nil
			}
		case "0002_create_job_artifacts.sql":
			if db.Migrator().HasTable("job_artifacts") {
				return nil
			}
		case "0003_add_jobs_node_runtime_index.sql":
			if db.Migrator().HasIndex("jobs", "idx_jobs_node_runtime") {
				return nil
			}
		case "0004_add_job_artifacts_storage_fields.sql":
			for _, column := range []struct{ name, definition string }{
				{"sha256", "VARCHAR(64) NOT NULL DEFAULT ''"}, {"size_bytes", "BIGINT NOT NULL DEFAULT 0"},
				{"content_type", "VARCHAR(255) NOT NULL DEFAULT ''"}, {"storage_key", "VARCHAR(1024) NOT NULL DEFAULT ''"},
			} {
				if !db.Migrator().HasColumn("job_artifacts", column.name) {
					if err := db.Exec("ALTER TABLE job_artifacts ADD COLUMN " + column.name + " " + column.definition).Error; err != nil {
						return errors.New("adding artifact storage column failed")
					}
				}
			}
			return nil
		}
	}
	for _, statement := range strings.Split(m.sql, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		words := strings.Fields(statement)
		if namespace == "base" {
			if len(words) > 6 && words[0] == "ALTER" && words[3] == "ADD" && words[4] == "COLUMN" && db.Migrator().HasColumn(words[2], words[5]) {
				continue
			}
			if len(words) > 4 && words[0] == "CREATE" && (words[1] == "INDEX" || words[1] == "UNIQUE") {
				i := 2
				if words[1] == "UNIQUE" {
					i++
				}
				if db.Migrator().HasIndex(words[i+2], words[i]) {
					continue
				}
			}
		}
		if err := db.Exec(statement).Error; err != nil {
			return errors.New("schema statement failed; inspect database schema and server diagnostics")
		}
	}
	return nil
}

// MigrateAll applies every supported namespace under one exclusive session lock.
// Resume explicitly retries dirty MySQL DDL after operator inspection.
// MigrateAll 在同一独占会话锁下应用所有支持的迁移；resume 显式重试经运维检查的 MySQL dirty DDL。
func (s *Store) MigrateAll(ctx context.Context, resume bool) error {
	return s.withMigrationLock(ctx, func(db *gorm.DB) error {
		statuses, err := New(db).MigrationStatus(db.Statement.Context)
		if err != nil {
			return err
		}
		for _, status := range statuses {
			if status.State == "dirty" && !resume {
				return fmt.Errorf("migration %s/%s is dirty; inspect before explicit resume", status.Namespace, status.ID)
			}
		}
		for _, namespace := range migrationNamespaces(db.Name()) {
			if _, err := migrateNamespace(db, namespace, resume); err != nil {
				return err
			}
		}
		return checkSchemaObjects(db, true)
	})
}

func (s *Store) migrateOne(ctx context.Context, namespace string) (applied []string, err error) {
	err = s.withMigrationLock(ctx, func(db *gorm.DB) error {
		var err error
		applied, err = migrateNamespace(db, namespace, false)
		if err == nil && namespace == "base" {
			err = checkSchemaObjects(db, false)
		}
		return err
	})
	return
}

// MigrationStatus reads all version states without creating or changing tables.
// MigrationStatus 只读全部版本状态，不创建或修改任何表。
func (s *Store) MigrationStatus(ctx context.Context) ([]MigrationStatus, error) {
	db := s.db.WithContext(ctx)
	var out []MigrationStatus
	for _, namespace := range migrationNamespaces(db.Name()) {
		all, err := loadMigrations(namespace, db.Name())
		if err != nil {
			return nil, err
		}
		records, err := readMigrationRecords(db, "schema_migrations_"+namespace)
		if err != nil {
			return nil, err
		}
		if err := validateMigrationHistory(all, adoptableMigrationRecords(all, records), true); err != nil {
			return nil, err
		}
		byID := make(map[string]migrationRecord, len(records))
		for _, r := range records {
			byID[r.ID] = r
		}
		for _, m := range all {
			status := MigrationStatus{Namespace: namespace, ID: m.id, State: "pending", Checksum: migrationChecksum(m.sql)}
			if r, ok := byID[m.id]; ok {
				status.State, status.AppliedAt = "applied", r.AppliedAt
				if r.Checksum == "" {
					status.State = "legacy"
				}
				if r.Dirty {
					status.State = "dirty"
				}
			}
			out = append(out, status)
		}
	}
	return out, nil
}

func adoptableMigrationRecords(all []migrationFile, records []migrationRecord) []migrationRecord {
	checksums := make(map[string]string, len(all))
	for _, m := range all {
		checksums[m.id] = migrationChecksum(m.sql)
	}
	copyRecords := append([]migrationRecord(nil), records...)
	for i := range copyRecords {
		if copyRecords[i].Checksum == "" {
			copyRecords[i].Checksum = checksums[copyRecords[i].ID]
		}
	}
	return copyRecords
}

// CheckSchema refuses pending, legacy, dirty, unknown or incompatible schemas.
// CheckSchema 拒绝待迁移、旧账本、dirty、未知版本或结构不兼容的数据库。
func (s *Store) CheckSchema(ctx context.Context) error {
	statuses, err := s.MigrationStatus(ctx)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		if status.State != "applied" {
			return fmt.Errorf("schema %s/%s is %s; run the database migration command before serving", status.Namespace, status.ID, status.State)
		}
	}
	return checkSchemaObjects(s.db.WithContext(ctx), true)
}
