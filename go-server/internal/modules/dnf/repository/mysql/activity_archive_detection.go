package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type activityArchiveColumn struct {
	name, kind, collation string
	size                  int64
	unsigned, zero        bool
	precision             int64
}

var activityArchiveEventColumns = []activityArchiveColumn{
	{name: "account_id", kind: "varchar", size: 128, collation: "utf8mb4_unicode_ci"},
	{name: "event_id", kind: "varchar", size: 128, collation: "ascii_bin"},
	{name: "character_id", kind: "varchar", size: 128, collation: "utf8mb4_unicode_ci"},
	{name: "character_name", kind: "varchar", size: 64, collation: "utf8mb4_unicode_ci"},
	{name: "metric", kind: "varchar", size: 64, collation: "utf8mb4_unicode_ci"},
	{name: "amount", kind: "bigint"}, {name: "occurred_at", kind: "datetime", precision: 6},
	{name: "service_day", kind: "char", size: 10, collation: "utf8mb4_unicode_ci"},
	{name: "dungeon_id", kind: "bigint"}, {name: "item_id", kind: "bigint", unsigned: true, zero: true},
	{name: "rarity", kind: "smallint", zero: true},
	{name: "source", kind: "varchar", size: 32, collation: "utf8mb4_unicode_ci"},
}

func activityArchiveColumns(ctx context.Context, db SQLDB, database, table string, expected []activityArchiveColumn) error {
	rows, err := db.QueryContext(ctx, "SELECT COLUMN_NAME,DATA_TYPE,CHARACTER_MAXIMUM_LENGTH,COLLATION_NAME,IS_NULLABLE,COLUMN_TYPE,DATETIME_PRECISION,COLUMN_DEFAULT,EXTRA,GENERATION_EXPRESSION FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", database, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var name, kind, nullable, columnType, extra string
		var size, precision sql.NullInt64
		var collation, defaultValue, generated sql.NullString
		if err := rows.Scan(&name, &kind, &size, &collation, &nullable, &columnType, &precision, &defaultValue, &extra, &generated); err != nil {
			return err
		}
		if i >= len(expected) {
			return fmt.Errorf("%w: extra column in %s.%s", ErrActivityArchiveSchema, database, table)
		}
		want := expected[i]
		if name != want.name || kind != want.kind || nullable != "NO" || extra != "" || generated.String != "" ||
			(want.size == 0 && size.Valid) || (want.size != 0 && (!size.Valid || size.Int64 != want.size)) ||
			collation.String != want.collation || strings.Contains(strings.ToLower(columnType), "unsigned") != want.unsigned ||
			(want.precision != 0 && (!precision.Valid || precision.Int64 != want.precision)) ||
			(want.precision == 0 && precision.Valid) ||
			(want.zero && (!defaultValue.Valid || strings.Trim(defaultValue.String, "'") != "0")) ||
			(!want.zero && defaultValue.Valid) {
			return fmt.Errorf("%w: column %s.%s/%s kind=%s size=%v collation=%s nullable=%s unsigned=%t precision=%v defaultPresent=%t defaultZero=%t extraLength=%d generatedLength=%d", ErrActivityArchiveSchema, database, table, name, kind, size, collation.String, nullable, strings.Contains(strings.ToLower(columnType), "unsigned"), precision, defaultValue.Valid, strings.Trim(defaultValue.String, "'") == "0", len(extra), len(generated.String))
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if i != len(expected) {
		return fmt.Errorf("%w: column count in %s.%s", ErrActivityArchiveSchema, database, table)
	}
	return rows.Close()
}

func activityArchiveIndices(ctx context.Context, db SQLDB, database, table string, expected map[string][]string) error {
	rows, err := db.QueryContext(ctx, "SELECT INDEX_NAME,NON_UNIQUE,SEQ_IN_INDEX,COLUMN_NAME,SUB_PART,INDEX_TYPE,COLLATION FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY INDEX_NAME,SEQ_IN_INDEX", database, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var name, kind string
		var column, order sql.NullString
		var prefix sql.NullInt64
		var nonunique, index int
		if err := rows.Scan(&name, &nonunique, &index, &column, &prefix, &kind, &order); err != nil {
			return err
		}
		want, ok := expected[name]
		position := seen[name]
		if !ok || position >= len(want) || index != position+1 || !column.Valid || column.String != want[position] || prefix.Valid || kind != "BTREE" || order.String != "A" ||
			(name == "PRIMARY" && nonunique != 0) || (name != "PRIMARY" && nonunique != 1) {
			return fmt.Errorf("%w: index %s.%s/%s position=%d", ErrActivityArchiveSchema, database, table, name, index)
		}
		seen[name]++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, columns := range expected {
		if seen[name] != len(columns) {
			return fmt.Errorf("%w: incomplete index %s.%s/%s", ErrActivityArchiveSchema, database, table, name)
		}
	}
	return rows.Close()
}

func activityArchiveTriggers(ctx context.Context, db SQLDB, s ActivityArchiveLayout, active bool) error {
	rows, err := db.QueryContext(ctx, "SELECT TRIGGER_NAME,EVENT_MANIPULATION,ACTION_TIMING,EVENT_OBJECT_TABLE,ACTION_STATEMENT FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=? AND (EVENT_OBJECT_TABLE IN (?,?,?,?) OR TRIGGER_NAME LIKE ?)", s.Database,
		s.Prefix+"_"+activityEventsTable, s.Prefix+"_"+activityArchiveCold, s.Prefix+"_"+activityArchiveReceipts, s.Prefix+"_"+activityArchiveState, strings.ReplaceAll(s.Prefix, "_", "\\_")+"\\_activity\\_arc\\_%")
	if err != nil {
		return err
	}
	defer rows.Close()
	expected := map[string]ActivityArchiveTrigger{}
	if active {
		for _, trigger := range s.Triggers {
			expected[trigger.Name] = trigger
		}
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name, event, timing, table string
		var body sql.NullString
		if err := rows.Scan(&name, &event, &timing, &table, &body); err != nil {
			return err
		}
		want, ok := expected[name]
		if !ok || seen[name] || !body.Valid || event != want.Event || timing != want.Timing || table != want.Table || compactGuildFingerprintSQL(body.String) != compactGuildFingerprintSQL(want.Body) {
			return fmt.Errorf("%w: trigger %s.%s definition differs or is not visible", ErrActivityArchiveSchema, s.Database, name)
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%w: trigger count %s got%d want%d", ErrActivityArchiveSchema, s.Database, len(seen), len(expected))
	}
	return rows.Close()
}

func activityArchiveShard(ctx context.Context, db SQLDB, s ActivityArchiveLayout) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT TABLE_NAME,ENGINE,TABLE_TYPE,TABLE_COLLATION,TABLE_COMMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME IN (?,?,?,?)", s.Database,
		s.Prefix+"_"+activityEventsTable, s.Prefix+"_"+activityArchiveCold, s.Prefix+"_"+activityArchiveReceipts, s.Prefix+"_"+activityArchiveState)
	if err != nil {
		return false, err
	}
	comments := map[string]string{}
	for rows.Next() {
		var name, tableType, comment string
		var engine, collation sql.NullString
		if err := rows.Scan(&name, &engine, &tableType, &collation, &comment); err != nil {
			rows.Close()
			return false, err
		}
		if _, duplicate := comments[name]; duplicate || tableType != "BASE TABLE" || engine.String != "InnoDB" || collation.String != "utf8mb4_unicode_ci" {
			rows.Close()
			return false, ErrActivityArchiveSchema
		}
		comments[name] = comment
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	hot := s.Prefix + "_" + activityEventsTable
	state := s.Prefix + "_" + activityArchiveState
	if _, exists := comments[state]; !exists {
		if len(comments) > 1 || strings.HasPrefix(comments[hot], "activity-archive/") {
			return false, ErrActivityArchiveSchema
		}
		return false, activityArchiveTriggers(ctx, db, s, false)
	}
	stateColumns := []activityArchiveColumn{{name: "slot", kind: "tinyint", unsigned: true},
		{name: "format", kind: "varchar", size: 64, collation: "ascii_bin"}, {name: "phase", kind: "varchar", size: 16, collation: "ascii_bin"},
		{name: "contract_sha", kind: "binary", size: 32}}
	if err := activityArchiveColumns(ctx, db, s.Database, state, stateColumns); err != nil {
		return false, err
	}
	if err := activityArchiveIndices(ctx, db, s.Database, state, map[string][]string{"PRIMARY": {"slot"}}); err != nil {
		return false, err
	}
	rows, err = db.QueryContext(ctx, "SELECT slot,format,phase,contract_sha FROM "+s.State+" ORDER BY slot LIMIT 2")
	if err != nil {
		return false, err
	}
	var slot int
	var format, phase string
	var digest []byte
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		return false, ErrActivityArchiveSchema
	}
	if err = rows.Scan(&slot, &format, &phase, &digest); err != nil {
		rows.Close()
		return false, err
	}
	extra := rows.Next()
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if extra || slot != 1 || format != ActivityArchiveFormat || hex.EncodeToString(digest) != s.ContractSHA256 || (phase != "active" && phase != "inactive") {
		return false, ErrActivityArchiveSchema
	}
	if phase == "inactive" {
		if _, exists := comments[s.Prefix+"_"+activityArchiveCold]; exists || len(comments) != 3 || strings.HasPrefix(comments[hot], "activity-archive/") {
			return false, ErrActivityArchiveSchema
		}
		if _, exists := comments[hot]; !exists {
			return false, ErrActivityArchiveSchema
		}
		if err := activityArchiveColumns(ctx, db, s.Database, hot, activityArchiveEventColumns); err != nil {
			return false, err
		}
		return false, activityArchiveTriggers(ctx, db, s, false)
	}
	if len(comments) != 4 || comments[hot] != activityArchiveHotComment {
		return false, ErrActivityArchiveSchema
	}
	indices := map[string][]string{"PRIMARY": {"account_id", "event_id"}, "idx_activity_account_time": {"account_id", "occurred_at", "event_id"}, "idx_activity_character_time": {"account_id", "character_id", "occurred_at", "event_id"}, "idx_activity_day": {"account_id", "service_day"}}
	for _, table := range []string{hot, s.Prefix + "_" + activityArchiveCold} {
		if err := activityArchiveColumns(ctx, db, s.Database, table, activityArchiveEventColumns); err != nil {
			return false, err
		}
		if err := activityArchiveIndices(ctx, db, s.Database, table, indices); err != nil {
			return false, err
		}
	}
	receiptColumns := []activityArchiveColumn{{name: "account_id", kind: "varchar", size: 128, collation: "utf8mb4_unicode_ci"}, {name: "event_id", kind: "varchar", size: 128, collation: "ascii_bin"}, {name: "payload", kind: "binary", size: 32}}
	if err := activityArchiveColumns(ctx, db, s.Database, s.Prefix+"_"+activityArchiveReceipts, receiptColumns); err != nil {
		return false, err
	}
	if err := activityArchiveIndices(ctx, db, s.Database, s.Prefix+"_"+activityArchiveReceipts, map[string][]string{"PRIMARY": {"account_id", "event_id"}}); err != nil {
		return false, err
	}
	return true, activityArchiveTriggers(ctx, db, s, true)
}

// QualifyActivityArchive performs bounded, read-only validation before publishing
// a repository group. Absence keeps the original schema. Partial or mixed shard
// states fail closed, instead of silently ignoring retained cold history.
// Low-level NewMySQLGroup remains side-effect-free; production Component.Start
// and the qualified constructor below always call this gate.
func QualifyActivityArchive(ctx context.Context, db SQLDB, options MySQLGroupOptions) (MySQLGroupOptions, error) {
	if db == nil {
		return options, ErrMySQLDBRequired
	}
	ctx, cancel := context.WithTimeout(ctxOrBackground(ctx), 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return options, err
	}
	dbs, err := r.ValidateDatabases(append(append([]string{}, options.DatabasePlan.SchemaDatabases()...), options.DatabasePlan.ReadDatabases...))
	if err != nil {
		return options, err
	}
	if len(dbs) == 0 {
		return options, ErrMySQLConfigInvalid
	}
	var mode bool
	for i, database := range dbs {
		if err := ctx.Err(); err != nil {
			return options, err
		}
		layout, err := ActivityArchivePlan(database, options.TablePrefix)
		if err != nil {
			return options, err
		}
		active, err := activityArchiveShard(ctx, db, layout)
		if err != nil {
			return options, fmt.Errorf("%w: %s: %w", ErrActivityArchiveSchema, database, err)
		}
		if i > 0 && active != mode {
			return options, ErrActivityArchiveSchema
		}
		mode = active
	}
	options.activityArchive = mode
	return options, nil
}

func NewQualifiedMySQLGroupFromDB(ctx context.Context, db *sql.DB, options MySQLGroupOptions) (r.Group, error) {
	options, err := QualifyActivityArchive(ctx, SQLDBAdapter{DB: db}, options)
	if err != nil {
		return r.Group{}, err
	}
	return NewMySQLGroupFromDB(db, options)
}
