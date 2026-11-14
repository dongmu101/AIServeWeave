package gormstore

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// partitionUnit is the width of one regular time partition.
//
// partitionUnit 是一个常规时间分区的宽度。
type partitionUnit int

const (
	unitDay partitionUnit = iota
	unitMonth
)

// start truncates t (in UTC) to the beginning of its partition period.
//
// start 把 t（按 UTC）截断到所在分区周期的起点。
func (u partitionUnit) start(t time.Time) time.Time {
	t = t.UTC()
	if u == unitMonth {
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// add moves a period start n periods forward (or back when n < 0).
//
// add 把一个周期起点向后（n < 0 时向前）移动 n 个周期。
func (u partitionUnit) add(t time.Time, n int) time.Time {
	if u == unitMonth {
		return t.AddDate(0, n, 0)
	}
	return t.AddDate(0, 0, n)
}

// suffix names the period that starts at t.
//
// suffix 为起点为 t 的周期命名。
func (u partitionUnit) suffix(t time.Time) string {
	if u == unitMonth {
		return t.UTC().Format("200601")
	}
	return t.UTC().Format("20060102")
}

// partitionedTable describes one time-partitioned table. This list is the
// single source the migration expansion, maintenance, retention and the schema
// check all read, so the five tables can never disagree about their layout.
//
// partitionedTable 描述一张按时间分区的表。迁移展开、分区维护、保留期清理与 schema
// 检查都只读这一份清单，五张表的布局因此不会在各处出现分歧。
type partitionedTable struct {
	name   string
	column string
	unit   partitionUnit
	// ahead is how many periods past the current one stay pre-created.
	// ahead 是当前周期之后始终预建的周期数。
	ahead int
}

const (
	// maxRegularPartitions caps the regular partitions one expansion or
	// maintenance pass creates; older rows go to the history partition.
	// maxRegularPartitions 限制一次展开或维护最多创建的常规分区数，更早的行进入历史分区。
	maxRegularPartitions = 1000
	// partitionLockWait bounds how long partition DDL waits for a metadata
	// lock, so it never queues behind a long transaction and stalls queries.
	// partitionLockWait 限制分区 DDL 等待元数据锁的时长，避免排在长事务之后连带阻塞查询。
	partitionLockWait = 2 * time.Second

	mysqlHistPartition     = "p_hist"
	mysqlCatchAllPartition = "p_max"
)

var partitionedTables = []partitionedTable{
	{name: "audit_logs", column: "created_at", unit: unitMonth, ahead: 2},
	{name: "metrics_history_points", column: "bucket_at", unit: unitDay, ahead: 7},
	{name: "request_logs", column: "created_at", unit: unitDay, ahead: 7},
	{name: "response_turns", column: "created_at", unit: unitDay, ahead: 7},
	{name: "usage_records", column: "created_at", unit: unitMonth, ahead: 2},
}

func lookupPartitionedTable(name string) (partitionedTable, bool) {
	for _, t := range partitionedTables {
		if t.name == name {
			return t, true
		}
	}
	return partitionedTable{}, false
}

// partitionRange is one regular partition's half-open interval [from, to).
//
// partitionRange 是一个常规分区的半开区间 [from, to)。
type partitionRange struct {
	from, to time.Time
}

// horizon is the exclusive upper bound pre-created partitions must reach.
//
// horizon 是预建分区必须覆盖到的上界（不含）。
func (t partitionedTable) horizon(now time.Time) time.Time {
	return t.unit.add(t.unit.start(now), t.ahead+1)
}

// rangesBetween splits [from, to) at period boundaries. from need not be aligned.
//
// rangesBetween 按周期边界切分 [from, to)，from 不必对齐。
func (t partitionedTable) rangesBetween(from, to time.Time) []partitionRange {
	var out []partitionRange
	for cur := from; cur.Before(to); {
		next := t.unit.add(t.unit.start(cur), 1)
		out = append(out, partitionRange{from: cur, to: next})
		cur = next
	}
	return out
}

// planInitialPartitions lays out a table being converted: one regular partition
// per period from the oldest row's period (or now, for an empty table) to the
// horizon, capped at maxRegularPartitions. histUpper bounds the history
// partition, which holds everything older than the first regular partition.
//
// planInitialPartitions 为正在转换的表规划分区：从最旧一行所在周期（空表则为当前周期）
// 到预建上界，每个周期一个常规分区，最多 maxRegularPartitions 个。histUpper 是历史分区的
// 上界，早于第一个常规分区的数据都在历史分区里。
func (t partitionedTable) planInitialPartitions(oldest *time.Time, now time.Time) (histUpper time.Time, ranges []partitionRange) {
	end := t.horizon(now)
	first := t.unit.start(now)
	if oldest != nil && oldest.Before(first) {
		first = t.unit.start(*oldest)
	}
	if lower := t.unit.add(end, -maxRegularPartitions); first.Before(lower) {
		first = lower
	}
	return first, t.rangesBetween(first, end)
}

// existingPartition is one partition read back from the catalog.
//
// existingPartition 是从数据库目录读回的一个分区。
type existingPartition struct {
	name string
	// upper is the exclusive upper bound; zero for the catch-all partition.
	// upper 是上界（不含）；兜底分区为零值。
	upper    time.Time
	catchAll bool
}

// missingRanges returns the regular partitions maintenance must add so the
// table reaches its horizon, continuing from the highest existing bound.
//
// missingRanges 返回维护需要补建的常规分区，从现有最高上界接着建到预建上界。
func (t partitionedTable) missingRanges(parts []existingPartition, now time.Time) []partitionRange {
	end := t.horizon(now)
	from := t.unit.start(now)
	found := false
	for _, p := range parts {
		if p.catchAll {
			continue
		}
		if !found || p.upper.After(from) {
			from, found = p.upper, true
		}
	}
	if lower := t.unit.add(end, -maxRegularPartitions); from.Before(lower) {
		from = lower
	}
	return t.rangesBetween(from, end)
}

// expiredPartitions returns the regular (including history) partitions whose
// whole range lies before cutoff. The catch-all partition is never returned.
//
// expiredPartitions 返回整个区间都早于 cutoff 的常规分区（含历史分区），永不返回兜底分区。
func expiredPartitions(parts []existingPartition, cutoff time.Time) []string {
	var out []string
	for _, p := range parts {
		if !p.catchAll && !p.upper.After(cutoff) {
			out = append(out, p.name)
		}
	}
	return out
}

func mysqlBound(t time.Time) string {
	return "'" + t.UTC().Format("2006-01-02 15:04:05") + "'"
}

func postgresBound(t time.Time) string {
	return "'" + t.UTC().Format("2006-01-02 15:04:05") + "+00'"
}

func mysqlPartitionName(t partitionedTable, r partitionRange) string {
	return "p" + t.unit.suffix(r.from)
}

func postgresPartitionName(t partitionedTable, r partitionRange) string {
	return t.name + "_p" + t.unit.suffix(r.from)
}

func mysqlRangeDefs(t partitionedTable, ranges []partitionRange) []string {
	defs := make([]string, 0, len(ranges))
	for _, r := range ranges {
		defs = append(defs, "PARTITION "+mysqlPartitionName(t, r)+" VALUES LESS THAN ("+mysqlBound(r.to)+")")
	}
	return defs
}

const mysqlCatchAllDef = "PARTITION " + mysqlCatchAllPartition + " VALUES LESS THAN (MAXVALUE)"

// mysqlInitialPartitionDefs renders the body of PARTITION BY RANGE COLUMNS(...) (...).
//
// mysqlInitialPartitionDefs 生成 PARTITION BY RANGE COLUMNS(...) (...) 括号内的分区定义。
func mysqlInitialPartitionDefs(t partitionedTable, histUpper time.Time, ranges []partitionRange) string {
	defs := []string{"PARTITION " + mysqlHistPartition + " VALUES LESS THAN (" + mysqlBound(histUpper) + ")"}
	defs = append(defs, mysqlRangeDefs(t, ranges)...)
	defs = append(defs, mysqlCatchAllDef)
	return strings.Join(defs, ", ")
}

// postgresInitialPartitionStatements renders the child tables of a freshly
// created partitioned parent.
//
// postgresInitialPartitionStatements 生成新建分区父表的各个子表。
func postgresInitialPartitionStatements(t partitionedTable, histUpper time.Time, ranges []partitionRange) []string {
	out := []string{fmt.Sprintf("CREATE TABLE %s_hist PARTITION OF %s FOR VALUES FROM (MINVALUE) TO (%s)", t.name, t.name, postgresBound(histUpper))}
	for _, r := range ranges {
		out = append(out, postgresCreatePartition(t, r))
	}
	return append(out, fmt.Sprintf("CREATE TABLE %s_default PARTITION OF %s DEFAULT", t.name, t.name))
}

func postgresCreatePartition(t partitionedTable, r partitionRange) string {
	return fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		postgresPartitionName(t, r), t.name, postgresBound(r.from), postgresBound(r.to))
}

// partitionLayout is what the catalog says about one table's partitioning.
//
// partitionLayout 是数据库目录中关于一张表分区方式的信息。
type partitionLayout struct {
	partitioned bool
	// column is the single RANGE partitioning column, or "" if the method or
	// key differs from what this package creates.
	// column 是唯一的 RANGE 分区列；分区方式或分区键与本包创建的不一致时为空。
	column string
	parts  []existingPartition
}

var postgresBoundPattern = regexp.MustCompile(`TO \('([^']+)'\)`)

// readPartitionLayout reads one table's partitions from the catalog. On
// PostgreSQL it must run inside a transaction whose TimeZone is UTC, so bound
// literals come back in a single, parseable offset.
//
// readPartitionLayout 从数据库目录读取一张表的分区。在 PostgreSQL 上必须在 TimeZone 为
// UTC 的事务内调用，使分区边界以统一、可解析的偏移返回。
func readPartitionLayout(db *gorm.DB, table string) (partitionLayout, error) {
	if db.Name() == "mysql" {
		var rows []struct {
			PartitionName        *string `gorm:"column:PARTITION_NAME"`
			PartitionMethod      *string `gorm:"column:PARTITION_METHOD"`
			PartitionExpression  *string `gorm:"column:PARTITION_EXPRESSION"`
			PartitionDescription *string `gorm:"column:PARTITION_DESCRIPTION"`
		}
		err := db.Raw("SELECT PARTITION_NAME, PARTITION_METHOD, PARTITION_EXPRESSION, PARTITION_DESCRIPTION FROM information_schema.PARTITIONS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY PARTITION_ORDINAL_POSITION", table).Scan(&rows).Error
		if err != nil {
			return partitionLayout{}, fmt.Errorf("reading partitions of %s failed", table)
		}
		var layout partitionLayout
		for _, row := range rows {
			if row.PartitionName == nil {
				continue
			}
			layout.partitioned = true
			if row.PartitionMethod != nil && *row.PartitionMethod == "RANGE COLUMNS" && row.PartitionExpression != nil {
				layout.column = strings.Trim(*row.PartitionExpression, "`")
			}
			p := existingPartition{name: *row.PartitionName}
			desc := ""
			if row.PartitionDescription != nil {
				desc = strings.Trim(*row.PartitionDescription, "'")
			}
			if desc == "MAXVALUE" {
				p.catchAll = true
			} else {
				upper, err := time.ParseInLocation("2006-01-02 15:04:05", desc, time.UTC)
				if err != nil {
					return partitionLayout{}, fmt.Errorf("partition %s.%s has an unrecognised bound", table, p.name)
				}
				p.upper = upper
			}
			layout.parts = append(layout.parts, p)
		}
		return layout, nil
	}
	var keys []struct {
		Strategy  string
		KeyColumn string
	}
	err := db.Raw("SELECT p.partstrat::text AS strategy, a.attname AS key_column FROM pg_partitioned_table p "+
		"JOIN pg_attribute a ON a.attrelid = p.partrelid AND a.attnum = ANY(p.partattrs::int2[]) "+
		"WHERE p.partrelid = to_regclass(?)", table).Scan(&keys).Error
	if err != nil {
		return partitionLayout{}, fmt.Errorf("reading partition key of %s failed", table)
	}
	if len(keys) == 0 {
		return partitionLayout{}, nil
	}
	layout := partitionLayout{partitioned: true}
	if len(keys) == 1 && keys[0].Strategy == "r" {
		layout.column = keys[0].KeyColumn
	}
	var rows []struct {
		Name  string
		Bound string
	}
	err = db.Raw("SELECT c.relname AS name, pg_get_expr(c.relpartbound, c.oid) AS bound FROM pg_inherits i "+
		"JOIN pg_class c ON c.oid = i.inhrelid WHERE i.inhparent = to_regclass(?) ORDER BY c.relname", table).Scan(&rows).Error
	if err != nil {
		return partitionLayout{}, fmt.Errorf("reading partitions of %s failed", table)
	}
	for _, row := range rows {
		p := existingPartition{name: row.Name}
		if row.Bound == "DEFAULT" {
			p.catchAll = true
		} else {
			m := postgresBoundPattern.FindStringSubmatch(row.Bound)
			if m == nil {
				return partitionLayout{}, fmt.Errorf("partition %s.%s has an unrecognised bound", table, p.name)
			}
			upper, err := parsePostgresBound(m[1])
			if err != nil {
				return partitionLayout{}, fmt.Errorf("partition %s.%s has an unrecognised bound", table, p.name)
			}
			p.upper = upper
		}
		layout.parts = append(layout.parts, p)
	}
	return layout, nil
}

func parsePostgresBound(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05-07", "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unrecognised timestamp bound")
}

func catchAllPartition(parts []existingPartition) (string, bool) {
	for _, p := range parts {
		if p.catchAll {
			return p.name, true
		}
	}
	return "", false
}
