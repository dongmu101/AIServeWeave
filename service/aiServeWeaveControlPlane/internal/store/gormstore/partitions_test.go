package gormstore

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustTable(t *testing.T, name string) partitionedTable {
	t.Helper()
	table, ok := lookupPartitionedTable(name)
	if !ok {
		t.Fatalf("%s is not a partitioned table", name)
	}
	return table
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestPartitionedTablesMatchTheSpecLayout(t *testing.T) {
	want := map[string]struct {
		column string
		unit   partitionUnit
		ahead  int
	}{
		"request_logs":           {"created_at", unitDay, 7},
		"usage_records":          {"created_at", unitMonth, 2},
		"metrics_history_points": {"bucket_at", unitDay, 7},
		"response_turns":         {"created_at", unitDay, 7},
		"audit_logs":             {"created_at", unitMonth, 2},
	}
	if len(partitionedTables) != len(want) {
		t.Fatalf("partitionedTables has %d entries, want %d", len(partitionedTables), len(want))
	}
	for _, table := range partitionedTables {
		w, ok := want[table.name]
		if !ok || table.column != w.column || table.unit != w.unit || table.ahead != w.ahead {
			t.Errorf("%s = %+v, want %+v", table.name, table, w)
		}
	}
}

func TestPlanInitialPartitionsForAnEmptyTableCoversNowPlusAhead(t *testing.T) {
	table := mustTable(t, "request_logs")
	now := utc("2026-11-14T15:30:00Z")
	hist, ranges := table.planInitialPartitions(nil, now)
	if !hist.Equal(utc("2026-11-14T00:00:00Z")) {
		t.Errorf("hist upper = %v", hist)
	}
	if len(ranges) != 8 {
		t.Fatalf("got %d ranges, want today + 7", len(ranges))
	}
	if !ranges[0].from.Equal(utc("2026-11-14T00:00:00Z")) || !ranges[7].to.Equal(utc("2026-11-22T00:00:00Z")) {
		t.Errorf("ranges span %v .. %v", ranges[0].from, ranges[7].to)
	}
	for i := 1; i < len(ranges); i++ {
		if !ranges[i].from.Equal(ranges[i-1].to) {
			t.Errorf("gap between %v and %v", ranges[i-1].to, ranges[i].from)
		}
	}
}

func TestPlanInitialPartitionsStartsAtTheOldestRowsPeriod(t *testing.T) {
	table := mustTable(t, "usage_records")
	oldest := utc("2026-09-17T08:00:00Z")
	hist, ranges := table.planInitialPartitions(&oldest, utc("2026-11-14T00:00:00Z"))
	if !hist.Equal(utc("2026-09-01T00:00:00Z")) {
		t.Errorf("hist upper = %v, want the oldest row's month", hist)
	}
	var names []string
	for _, r := range ranges {
		names = append(names, mysqlPartitionName(table, r))
	}
	if want := []string{"p202609", "p202610", "p202611", "p202612", "p202701"}; !reflect.DeepEqual(names, want) {
		t.Errorf("partitions = %v, want %v (crossing the year boundary)", names, want)
	}
}

func TestPlanInitialPartitionsCapsRegularPartitionsAndKeepsOlderRowsInHistory(t *testing.T) {
	table := mustTable(t, "response_turns")
	oldest := utc("2019-01-01T00:00:00Z")
	now := utc("2026-11-14T00:00:00Z")
	hist, ranges := table.planInitialPartitions(&oldest, now)
	if len(ranges) != maxRegularPartitions {
		t.Fatalf("got %d ranges, want the %d cap", len(ranges), maxRegularPartitions)
	}
	if !hist.Equal(ranges[0].from) || !hist.After(oldest) {
		t.Errorf("hist upper %v should be the first regular partition's start and after the oldest row", hist)
	}
	if !ranges[len(ranges)-1].to.Equal(table.horizon(now)) {
		t.Errorf("last range should end at the horizon")
	}
}

func TestPlanInitialPartitionsIgnoresFutureOldestValue(t *testing.T) {
	table := mustTable(t, "request_logs")
	future := utc("2027-03-01T00:00:00Z")
	hist, _ := table.planInitialPartitions(&future, utc("2026-11-14T00:00:00Z"))
	if !hist.Equal(utc("2026-11-14T00:00:00Z")) {
		t.Errorf("hist upper = %v, want today", hist)
	}
}

func TestMissingRangesContinuesFromTheHighestBound(t *testing.T) {
	table := mustTable(t, "request_logs")
	parts := []existingPartition{
		{name: "p_hist", upper: utc("2026-11-10T00:00:00Z")},
		{name: "p20261110", upper: utc("2026-11-11T00:00:00Z")},
		{name: "p20261111", upper: utc("2026-11-12T00:00:00Z")},
		{name: "p_max", catchAll: true},
	}
	got := table.missingRanges(parts, utc("2026-11-14T09:00:00Z"))
	if len(got) != 10 || !got[0].from.Equal(utc("2026-11-12T00:00:00Z")) || !got[9].to.Equal(utc("2026-11-22T00:00:00Z")) {
		t.Fatalf("missing = %d ranges from %v", len(got), got)
	}
	parts = append(parts[:3], existingPartition{name: "p20261121", upper: utc("2026-11-22T00:00:00Z")}, parts[3])
	if again := table.missingRanges(parts, utc("2026-11-14T09:00:00Z")); len(again) != 0 {
		t.Errorf("a table already at its horizon needs %d ranges", len(again))
	}
}

func TestMissingRangesWithOnlyACatchAllStartsToday(t *testing.T) {
	table := mustTable(t, "audit_logs")
	got := table.missingRanges([]existingPartition{{name: "p_max", catchAll: true}}, utc("2026-11-14T09:00:00Z"))
	if len(got) != 3 || !got[0].from.Equal(utc("2026-11-01T00:00:00Z")) || !got[2].to.Equal(utc("2027-02-01T00:00:00Z")) {
		t.Fatalf("missing = %v", got)
	}
}

func TestMissingRangesAlignsAnUnalignedBound(t *testing.T) {
	table := mustTable(t, "request_logs")
	parts := []existingPartition{{name: "p_hist", upper: utc("2026-11-14T06:00:00Z")}, {name: "p_max", catchAll: true}}
	got := table.missingRanges(parts, utc("2026-11-14T09:00:00Z"))
	if !got[0].from.Equal(utc("2026-11-14T06:00:00Z")) || !got[0].to.Equal(utc("2026-11-15T00:00:00Z")) {
		t.Errorf("first range = %v, want [06:00, next midnight)", got[0])
	}
}

func TestExpiredPartitionsNeverIncludesTheCatchAll(t *testing.T) {
	parts := []existingPartition{
		{name: "p_hist", upper: utc("2026-10-01T00:00:00Z")},
		{name: "p20261001", upper: utc("2026-10-02T00:00:00Z")},
		{name: "p20261002", upper: utc("2026-10-03T00:00:00Z")},
		{name: "p_max", catchAll: true},
	}
	got := expiredPartitions(parts, utc("2026-10-02T12:00:00Z"))
	if want := []string{"p_hist", "p20261001"}; !reflect.DeepEqual(got, want) {
		t.Errorf("expired = %v, want %v (a partition straddling the cutoff stays)", got, want)
	}
	if got := expiredPartitions(parts, utc("2026-10-02T00:00:00Z")); !reflect.DeepEqual(got, []string{"p_hist", "p20261001"}) {
		t.Errorf("an upper bound equal to the cutoff is fully expired: %v", got)
	}
}

func TestPartitionDDLRendering(t *testing.T) {
	table := mustTable(t, "request_logs")
	hist, ranges := table.planInitialPartitions(nil, utc("2026-11-14T00:00:00Z"))
	mysqlDefs := mysqlInitialPartitionDefs(table, hist, ranges[:1])
	if want := "PARTITION p_hist VALUES LESS THAN ('2026-11-14 00:00:00'), PARTITION p20261114 VALUES LESS THAN ('2026-11-15 00:00:00'), PARTITION p_max VALUES LESS THAN (MAXVALUE)"; mysqlDefs != want {
		t.Errorf("mysql defs = %s", mysqlDefs)
	}
	pg := postgresInitialPartitionStatements(table, hist, ranges[:1])
	want := []string{
		"CREATE TABLE request_logs_hist PARTITION OF request_logs FOR VALUES FROM (MINVALUE) TO ('2026-11-14 00:00:00+00')",
		"CREATE TABLE request_logs_p20261114 PARTITION OF request_logs FOR VALUES FROM ('2026-11-14 00:00:00+00') TO ('2026-11-15 00:00:00+00')",
		"CREATE TABLE request_logs_default PARTITION OF request_logs DEFAULT",
	}
	if !reflect.DeepEqual(pg, want) {
		t.Errorf("postgres statements = %v", pg)
	}
}

func TestParsePartitionDirective(t *testing.T) {
	args, err := parsePartitionDirective("table=request_logs source=request_logs_unpartitioned")
	if err != nil || args.table.name != "request_logs" || args.source != "request_logs_unpartitioned" {
		t.Fatalf("args = %+v, err = %v", args, err)
	}
	if args, _ := parsePartitionDirective("table=audit_logs"); args.source != "audit_logs" {
		t.Errorf("source should default to the table, got %q", args.source)
	}
	for _, bad := range []string{"table=users", "table", ""} {
		if _, err := parsePartitionDirective(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// TestPartitionMigrationsAreTemplates asserts every partitioned table has one
// conversion migration per dialect and that none embeds concrete bounds — the
// checksum covers the template, so bounds must come from the directive.
//
// TestPartitionMigrationsAreTemplates 断言每张分区表在每种方言下各有一个转换迁移，且都不
// 内嵌具体边界：校验和覆盖的是模板，边界只能来自占位展开。
func TestPartitionMigrationsAreTemplates(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		found := 0
		for _, namespace := range migrationNamespaces(dialect) {
			all, err := loadMigrations(namespace, dialect)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range all {
				if !isPartitionMigration(m) {
					continue
				}
				found++
				for _, match := range partitionDirective.FindAllStringSubmatch(m.sql, -1) {
					if _, err := parsePartitionDirective(match[1]); err != nil {
						t.Errorf("%s/%s/%s: %v", namespace, dialect, m.id, err)
					}
				}
				if strings.Contains(m.sql, "VALUES LESS THAN ('") || strings.Contains(m.sql, "FOR VALUES FROM ('") {
					t.Errorf("%s/%s/%s embeds concrete partition bounds", namespace, dialect, m.id)
				}
			}
		}
		if found != len(partitionedTables) {
			t.Errorf("%s: %d partition migrations, want one per partitioned table (%d)", dialect, found, len(partitionedTables))
		}
	}
}

func TestParsePostgresBound(t *testing.T) {
	for _, s := range []string{"2026-11-14 00:00:00+00", "2026-11-14 08:00:00+08", "2026-11-14 05:30:00+05:30"} {
		got, err := parsePostgresBound(s)
		if err != nil || !got.Equal(utc("2026-11-14T00:00:00Z")) {
			t.Errorf("%s -> %v, %v", s, got, err)
		}
	}
}
