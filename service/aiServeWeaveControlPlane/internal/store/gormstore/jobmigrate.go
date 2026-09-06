package gormstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"time"
)

// execer is the one *sql.DB method applyJobMigration needs. It exists so the
// call site names what it depends on rather than the whole of *sql.DB.
//
// execer 是 applyJobMigration 需要的唯一一个 *sql.DB 方法。它的存在是为了让调用点
// 表明自己依赖的是什么，而不是整个 *sql.DB。
type execer interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// jobMigrationsFS holds the versioned SQL migrations for the jobs and
// job_artifacts tables, per STATUS.md's J03. They are separate from
// Store.Migrate's AutoMigrate call on purpose: AutoMigrate cannot express
// what J03 requires — a migration that runs exactly once, in a fixed order,
// with a durable record of what has already applied — and the service
// README already names the day AutoMigrate stops being enough as a
// scheduled decision rather than a surprise. For these two tables, that day
// is today: STATUS.md commits Job persistence to MySQL 9.7/InnoDB
// specifically, not to the dual PostgreSQL/MySQL support the first four
// tables get, so there is no cross-dialect scalar-columns argument keeping
// AutoMigrate adequate here the way the service README's 数据库 section
// still finds it adequate for those four.
//
// jobMigrationsFS 保存 jobs 与 job_artifacts 两张表的带版本 SQL 迁移，对应
// STATUS.md 的 J03。它们特意与 Store.Migrate 的 AutoMigrate 调用分开：
// AutoMigrate 无法表达 J03 所要求的东西——一次只会执行一次、顺序固定、且留有
// 「已执行过什么」持久记录的迁移——而服务 README 早已把 AutoMigrate 不再够用的
// 那一天定性为一个排上日程的决定，而不是一次意外。对这两张表而言，那一天就是
// 今天：STATUS.md 把 Job 持久化明确交给 MySQL 9.7/InnoDB，而不是前四张表享有的
// PostgreSQL/MySQL 双支持，因此不存在「跨方言标量列」那条让 AutoMigrate 在服务
// README「数据库」一节里对那四张表依然够用的理由。
//
//go:embed migrations/jobs/*.sql
var jobMigrationsFS embed.FS

// jobMigrationsDir is the embedded directory jobMigrationsFS exposes.
const jobMigrationsDir = "migrations/jobs"

// jobSchemaMigrationsTable records which migration ids have already run. It
// is created if missing every time MigrateJobs is called, the same way the
// migrations themselves are — this is what "可重复执行" means: a second call
// with nothing new to apply is a fast no-op, not an error.
const jobSchemaMigrationsTable = "schema_migrations_jobs"

// migrationFile is one embedded migration: its id (the filename, which
// sorts in the order it must run) and its SQL body.
//
// migrationFile 是一个内嵌的迁移：它的 id（文件名，其排序即执行顺序）与 SQL 正文。
type migrationFile struct {
	id  string
	sql string
}

// loadJobMigrations reads every embedded migration file, sorted by filename.
// Sorting by filename rather than by embed.FS's directory order is what
// makes "0001_..." before "0002_..." a property of the filenames themselves,
// not an accident of how the embedding tool happened to walk the directory.
//
// loadJobMigrations 读取每一个内嵌的迁移文件，按文件名排序。按文件名而不是按
// embed.FS 的目录顺序排序，才能让「0001_... 先于 0002_...」是文件名本身的属性，
// 而不是内嵌工具恰好如何遍历目录的偶然结果。
func loadJobMigrations() ([]migrationFile, error) {
	entries, err := jobMigrationsFS.ReadDir(jobMigrationsDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, k int) bool { return entries[i].Name() < entries[k].Name() })

	out := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		body, err := jobMigrationsFS.ReadFile(jobMigrationsDir + "/" + entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migrationFile{id: entry.Name(), sql: string(body)})
	}
	return out, nil
}

// pendingJobMigrations returns the migrations in all not present in applied,
// in their required run order. It is a pure function so the ordering and
// skip-already-applied rules — the essence of "可重复执行且有版本记录" — are
// checked without a database: see jobmigrate_test.go.
//
// pendingJobMigrations 返回 all 中不在 applied 里的迁移，按其必须的执行顺序排列。
// 它是一个纯函数，好让「可重复执行且有版本记录」的核心——顺序与「已执行过的跳过」
// 规则——不必借助数据库即可校验：见 jobmigrate_test.go。
func pendingJobMigrations(all []migrationFile, applied map[string]bool) []migrationFile {
	var pending []migrationFile
	for _, m := range all {
		if !applied[m.id] {
			pending = append(pending, m)
		}
	}
	return pending
}

// MigrateJobs applies every not-yet-applied migration under migrations/jobs,
// in filename order, each in its own transaction that also records the
// migration's id in jobSchemaMigrationsTable — so a failure partway through
// one file's statement never leaves that file recorded as applied, and a
// process restart resumes at the first migration not yet recorded rather
// than re-running or skipping one.
//
// It requires the MySQL dialect: STATUS.md commits Job persistence to
// MySQL 9.7/InnoDB, and the embedded SQL is written in MySQL's dialect, not
// a cross-engine subset — calling this against PostgreSQL fails fast with a
// clear error rather than with a syntax error partway through a migration.
//
// It returns the ids of the migrations this call actually applied, which is
// empty (and nil error) on a repeat call once the schema is up to date —
// that emptiness is what "可重复执行" is verified by by a caller.
//
// MigrateJobs 按文件名顺序应用每一个尚未应用过的 migrations/jobs 迁移，每一个都
// 在自己的事务里执行，同一事务也把该迁移的 id 记入 jobSchemaMigrationsTable——
// 这样一次失败若发生在某个文件语句执行到一半，那个文件绝不会被记录为已应用，
// 进程重启后会从第一个尚未记录的迁移续起，而不是重新执行或跳过某一个。
//
// 它要求 MySQL 方言：STATUS.md 把 Job 持久化交给 MySQL 9.7/InnoDB，内嵌的 SQL
// 写的是 MySQL 方言，不是跨引擎的公共子集——对着 PostgreSQL 调用它会带着一条
// 清楚的错误快速失败，而不是在某次迁移执行到一半时抛出语法错误。
//
// 它返回本次调用实际应用的迁移 id；一旦 schema 已是最新，重复调用会返回空切片
// （且 error 为 nil）——这份空，就是调用方用来验证「可重复执行」的凭据。
func (s *Store) MigrateJobs(ctx context.Context) ([]string, error) {
	if name := s.db.Name(); name != "mysql" {
		return nil, fmt.Errorf("gormstore: MigrateJobs requires the mysql dialect, got %q — Job persistence is MySQL-only per STATUS.md", name)
	}

	sqlDB, err := s.db.DB()
	if err != nil {
		return nil, err
	}

	createTracking := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (id VARCHAR(255) NOT NULL PRIMARY KEY, applied_at DATETIME(6) NOT NULL) ENGINE=InnoDB",
		jobSchemaMigrationsTable,
	)
	if _, err := sqlDB.ExecContext(ctx, createTracking); err != nil {
		return nil, errors.Join(errors.New("creating the job schema migration tracking table"), err)
	}

	all, err := loadJobMigrations()
	if err != nil {
		return nil, err
	}

	rows, err := sqlDB.QueryContext(ctx, fmt.Sprintf("SELECT id FROM %s", jobSchemaMigrationsTable))
	if err != nil {
		return nil, errors.Join(errors.New("reading already-applied job migrations"), err)
	}
	applied := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		applied[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var appliedNow []string
	for _, m := range pendingJobMigrations(all, applied) {
		if err := applyJobMigration(ctx, sqlDB, m); err != nil {
			return appliedNow, errors.Join(fmt.Errorf("applying job migration %s", m.id), err)
		}
		appliedNow = append(appliedNow, m.id)
	}
	return appliedNow, nil
}

// applyJobMigration runs one migration's SQL and records it as applied in a
// single transaction, so the two either both happen or neither does.
//
// applyJobMigration 在一个事务里执行一个迁移的 SQL 并记录其已应用，让两者要么
// 一起发生，要么都不发生。
func applyJobMigration(ctx context.Context, db execer, m migrationFile) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s (id, applied_at) VALUES (?, ?)", jobSchemaMigrationsTable),
		m.id, time.Now().UTC(),
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
