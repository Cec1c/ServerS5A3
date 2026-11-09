package mysql

import (
	"longheng.io/server/internal/platform/metrics"
	"strings"
	"sync/atomic"
	"time"
)

var sqlFamilyNames = [...]string{"other", "guild_snapshot", "character", "inventory", "equipment", "quest", "pet", "auction", "settings", "account"}

type sqlFamilySeries struct {
	count          atomic.Int64
	nanos          atomic.Int64
	rows           atomic.Int64
	statementBytes atomic.Int64
}
type sqlFamilyObservation struct {
	family int
	bytes  int
}

func sqlFamily(q string) sqlFamilyObservation {
	table := sqlFamilyPrimaryTable(q)
	family := 0
	switch {
	case strings.Contains(q, "SHA2(v.entry_value, 256)"):
		family = 1
	case strings.Contains(table, "character_") || strings.HasSuffix(table, "characters"):
		family = 2
	case strings.Contains(table, "inventory_") || strings.HasSuffix(table, "inventories"):
		family = 3
	case strings.Contains(table, "equipment"):
		family = 4
	case strings.Contains(table, "quest_") || strings.HasSuffix(table, "quests"):
		family = 5
	case strings.Contains(table, "pet_") || strings.HasSuffix(table, "pets"):
		family = 6
	case strings.Contains(table, "auction"):
		family = 7
	case strings.Contains(table, "setting"):
		family = 8
	case strings.Contains(table, "account"):
		family = 9
	}
	return sqlFamilyObservation{family: family, bytes: len(q)}
}

// Repository templates have a fixed uppercase verb and quoted table names.
// Classify their first table, never a column, schema name or joined table.
func sqlFamilyPrimaryTable(q string) string {
	start := -1
	if strings.HasPrefix(q, "UPDATE ") {
		start = len("UPDATE ")
	}
	for _, marker := range []string{" FROM ", " INTO "} {
		if index := strings.Index(q, marker); index >= 0 && (start < 0 || index+len(marker) < start) {
			start = index + len(marker)
		}
	}
	if start < 0 {
		return ""
	}
	table := q[start:]
	if index := strings.IndexAny(table, " \t\n("); index >= 0 {
		table = table[:index]
	}
	if index := strings.LastIndexByte(table, '.'); index >= 0 {
		table = table[index+1:]
	}
	return strings.Trim(table, "`")
}
func (o *SQLObservation) finishFamily(kind int, start time.Time, rows int64, details []sqlFamilyObservation) {
	if o == nil || start.IsZero() || len(details) == 0 {
		return
	}
	d := details[0]
	if d.family < 0 || d.family >= len(sqlFamilyNames) {
		return
	}
	s := &o.families[d.family][kind]
	s.count.Add(1)
	s.nanos.Add(int64(time.Since(start)))
	s.rows.Add(rows)
	s.statementBytes.Add(int64(d.bytes))
}
func (o *SQLObservation) observeFamilies(registry *metrics.Registry) {
	for family, name := range sqlFamilyNames {
		for kind, operation := range sqlObserveNames {
			s := &o.families[family][kind]
			labels := map[string]string{"family": name, "operation": operation}
			registry.SetCounter("dnf_mysql_family_calls_total", labels, s.count.Load())
			registry.SetCounter("dnf_mysql_family_nanoseconds_total", labels, s.nanos.Load())
			registry.SetCounter("dnf_mysql_family_rows_read_total", labels, s.rows.Load())
			registry.SetCounter("dnf_mysql_family_statement_bytes_total", labels, s.statementBytes.Load())
		}
	}
}
