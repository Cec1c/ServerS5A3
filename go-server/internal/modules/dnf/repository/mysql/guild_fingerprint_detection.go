package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type guildFingerprintReader interface {
	QueryContext(context.Context, string, ...any) (SQLRows, error)
	QueryRowContext(context.Context, string, ...any) SQLRow
}

func guildFingerprintInnoDB(ctx context.Context, db guildFingerprintReader, database, table string) (bool, error) {
	var engine string
	err := db.QueryRowContext(ctx, "SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", database, table).Scan(&engine)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(engine, "InnoDB"), nil
}
func guildFingerprintTableValid(ctx context.Context, db guildFingerprintReader, s guildFingerprintSpec) (bool, error) {
	ok, err := guildFingerprintInnoDB(ctx, db, s.database, s.prefix+"_"+guildFingerprintTable)
	if !ok || err != nil {
		return ok, err
	}
	rows, err := db.QueryContext(ctx, "SELECT COLUMN_NAME,DATA_TYPE,CHARACTER_MAXIMUM_LENGTH,IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", s.database, s.prefix+"_"+guildFingerprintTable)
	if err != nil {
		return false, err
	}
	valid := true
	n := 0
	for rows.Next() {
		var name, kind, nullable string
		var size int64
		if err := rows.Scan(&name, &kind, &size, &nullable); err != nil {
			rows.Close()
			return false, err
		}
		if n == 0 {
			valid = valid && name == "scope" && kind == "varchar" && size == 128
		} else if n == 1 {
			valid = valid && name == "fingerprint" && kind == "varbinary" && size == 64
		} else {
			valid = false
		}
		valid = valid && nullable == "NO"
		n++
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if !valid || n != 2 {
		return false, nil
	}
	rows, err = db.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND INDEX_NAME='PRIMARY' ORDER BY SEQ_IN_INDEX", s.database, s.prefix+"_"+guildFingerprintTable)
	if err != nil {
		return false, err
	}
	n = 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return false, err
		}
		valid = valid && name == "scope"
		n++
	}
	err = rows.Err()
	closeErr = rows.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	return valid && n == 1, nil
}
func guildFingerprintTriggers(ctx context.Context, db guildFingerprintReader, s guildFingerprintSpec) (map[string]bool, bool, error) {
	expected := map[string]guildFingerprintTrigger{}
	for _, t := range s.triggers {
		expected[t.name] = t
	}
	rows, err := db.QueryContext(ctx, "SELECT TRIGGER_NAME,EVENT_MANIPULATION,ACTION_TIMING,EVENT_OBJECT_TABLE,ACTION_STATEMENT FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=? AND TRIGGER_NAME IN (?,?,?)", s.database, s.triggers[0].name, s.triggers[1].name, s.triggers[2].name)
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	valid := true
	for rows.Next() {
		var name, event, timing, table string
		var body sql.NullString
		if err := rows.Scan(&name, &event, &timing, &table, &body); err != nil {
			rows.Close()
			return nil, false, err
		}
		want, found := expected[name]
		valid = valid && found && !seen[name] && event == want.event && timing == "AFTER" && table == s.prefix+"_"+mysqlSettingValuesTable && body.Valid && compactGuildFingerprintSQL(body.String) == compactGuildFingerprintSQL(want.body)
		seen[name] = true
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, false, err
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	return seen, valid, nil
}
func guildFingerprintReady(ctx context.Context, db guildFingerprintReader, s guildFingerprintSpec) (bool, error) {
	for _, table := range []string{s.prefix + "_" + mysqlSettingsTable, s.prefix + "_" + mysqlSettingValuesTable} {
		ok, err := guildFingerprintInnoDB(ctx, db, s.database, table)
		if !ok || err != nil {
			return ok, err
		}
	}
	if ok, err := guildFingerprintTableValid(ctx, db, s); !ok || err != nil {
		return ok, err
	}
	seen, ok, err := guildFingerprintTriggers(ctx, db, s)
	if !ok || err != nil {
		return ok, err
	}
	if len(seen) != 3 {
		return false, nil
	}
	var source string
	var stored sql.NullString
	err = db.QueryRowContext(ctx, "SELECT SHA2(v.entry_value, 256),f.fingerprint FROM "+s.values+" v LEFT JOIN "+s.table+" f ON f.scope=v.scope WHERE v.scope=? AND v.entry_key='state'", r.GuildRegistryScope).Scan(&source, &stored)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return stored.Valid && source == stored.String, nil
}

// GuildSnapshotFingerprintReady is read-only startup/preflight validation.
// Every read and write shard needs the exact known schema and triggers plus
// an initial fact matching the current source. Otherwise retain source SHA.
func GuildSnapshotFingerprintReady(ctx context.Context, db SQLDB, options MySQLGroupOptions) (bool, error) {
	if db == nil {
		return false, ErrMySQLDBRequired
	}
	ctx = ctxOrBackground(ctx)
	dbs, err := r.ValidateDatabases(append(append([]string{}, options.DatabasePlan.SchemaDatabases()...), options.DatabasePlan.ReadDatabases...))
	if err != nil {
		return false, err
	}
	if len(dbs) == 0 {
		return false, ErrMySQLConfigInvalid
	}
	for _, database := range dbs {
		s, err := newGuildFingerprintSpec(database, options.TablePrefix)
		if err != nil {
			return false, err
		}
		ok, err := guildFingerprintReady(ctx, db, s)
		if guildFingerprintOptionalSchemaError(err) {
			return false, nil
		}
		if !ok || err != nil {
			return ok, err
		}
	}
	return true, nil
}

// Optional derived metadata must not require extra runtime grants. Connection,
// cancellation and genuine source-read errors still fail normally; the legacy
// reader remains responsible for reporting failures of the original schema.
func guildFingerprintOptionalSchemaError(err error) bool {
	var sqlErr *driver.MySQLError
	if !errors.As(err, &sqlErr) {
		return false
	}
	switch sqlErr.Number {
	case 1044, 1054, 1142, 1143, 1146, 1227, 1267:
		return true
	}
	return false
}
