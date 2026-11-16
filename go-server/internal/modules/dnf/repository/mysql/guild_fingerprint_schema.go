package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

const guildFingerprintTable = "guild_registry_fingerprints"

var ErrGuildFingerprintSchema = errors.New("guild fingerprint schema is not the expected additive schema")

type guildFingerprintTrigger struct{ name, event, body string }
type guildFingerprintSpec struct {
	database, prefix, settings, values, table string
	triggers                                  []guildFingerprintTrigger
}

func newGuildFingerprintSpec(database, prefix string) (guildFingerprintSpec, error) {
	if !r.IsSQLIdentifier(database) {
		return guildFingerprintSpec{}, ErrSchemaConfigInvalid
	}
	prefix, err := normTablePrefix(prefix)
	if err != nil {
		return guildFingerprintSpec{}, err
	}
	for _, suffix := range []string{guildFingerprintTable, "guild_fingerprint_i", "guild_fingerprint_u", "guild_fingerprint_d"} {
		if len(prefix+"_"+suffix) > 64 {
			return guildFingerprintSpec{}, ErrSchemaConfigInvalid
		}
	}
	s := guildFingerprintSpec{database: database, prefix: prefix, settings: mysqlTable(database, prefix, mysqlSettingsTable), values: mysqlTable(database, prefix, mysqlSettingValuesTable), table: mysqlTable(database, prefix, guildFingerprintTable)}
	insert := "IF NEW.scope='" + r.GuildRegistryScope + "' AND NEW.entry_key='state' THEN INSERT INTO " + s.table + " (scope,fingerprint) VALUES (NEW.scope,SHA2(NEW.entry_value,256)) ON DUPLICATE KEY UPDATE fingerprint=VALUES(fingerprint); END IF;"
	remove := "IF OLD.scope='" + r.GuildRegistryScope + "' AND OLD.entry_key='state' THEN DELETE FROM " + s.table + " WHERE scope=OLD.scope; END IF;"
	for _, x := range []struct{ suffix, event, body string }{{"i", "INSERT", insert}, {"u", "UPDATE", remove + " " + insert}, {"d", "DELETE", remove}} {
		s.triggers = append(s.triggers, guildFingerprintTrigger{prefix + "_guild_fingerprint_" + x.suffix, x.event, "BEGIN " + x.body + " END"})
	}
	return s, nil
}
func (s guildFingerprintSpec) tableDDL() string {
	return "CREATE TABLE IF NOT EXISTS " + s.table + " (scope VARCHAR(128) NOT NULL,fingerprint VARBINARY(64) NOT NULL,PRIMARY KEY (scope)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
}
func (s guildFingerprintSpec) triggerDDL(t guildFingerprintTrigger) string {
	return "CREATE TRIGGER " + quoteSQLIdentifier(s.database) + "." + quoteSQLIdentifier(t.name) + " AFTER " + t.event + " ON " + s.values + " FOR EACH ROW " + t.body
}

// GuildSnapshotFingerprintDDL is a reviewable additive DDL plan. Applying it
// alone is not installation: the installer also validates existing definitions
// and backfills while holding the source row lock.
func GuildSnapshotFingerprintDDL(options SchemaOptions) ([]string, error) {
	dbs, err := r.ValidateDatabases(options.DatabasePlan.SchemaDatabases())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, database := range dbs {
		s, err := newGuildFingerprintSpec(database, options.TablePrefix)
		if err != nil {
			return nil, err
		}
		out = append(out, s.tableDDL())
		for _, t := range s.triggers {
			out = append(out, s.triggerDDL(t))
		}
	}
	return out, nil
}

// EnsureGuildSnapshotFingerprint is explicit maintenance, never called by
// ordinary schema initialization, repository construction or player requests.
// AutoCreate must be explicitly enabled. No guild JSON, settings columns,
// timestamps or durable receipts are modified.
func EnsureGuildSnapshotFingerprint(ctx context.Context, db *sql.DB, options SchemaOptions) error {
	if !options.AutoCreate {
		return nil
	}
	if db == nil {
		return ErrMySQLDBRequired
	}
	ctx = ctxOrBackground(ctx)
	// DDL metadata-lock waits must remain bounded even if the maintenance
	// caller supplied a background context. A retry is a new explicit task.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := GuildSnapshotFingerprintDDL(options); err != nil {
		return err
	}
	dbs, err := r.ValidateDatabases(options.DatabasePlan.SchemaDatabases())
	if err != nil {
		return err
	}
	for _, database := range dbs {
		s, err := newGuildFingerprintSpec(database, options.TablePrefix)
		if err != nil {
			return err
		}
		if err = installGuildFingerprint(ctx, db, s); err != nil {
			return err
		}
	}
	return nil
}

type guildFingerprintConn struct{ *sql.Conn }

func (c guildFingerprintConn) QueryContext(ctx context.Context, q string, args ...any) (SQLRows, error) {
	return c.Conn.QueryContext(ctx, q, args...)
}
func (c guildFingerprintConn) QueryRowContext(ctx context.Context, q string, args ...any) SQLRow {
	return c.Conn.QueryRowContext(ctx, q, args...)
}

func installGuildFingerprint(ctx context.Context, db *sql.DB, s guildFingerprintSpec) (resultErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	lockHash := sha256.Sum256([]byte(s.database + "/" + s.prefix))
	lockName := fmt.Sprintf("guild-fingerprint-%x", lockHash[:16])
	var locked sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", lockName).Scan(&locked); err != nil {
		return err
	}
	if !locked.Valid || locked.Int64 != 1 {
		return fmt.Errorf("%w: installer is busy", ErrGuildFingerprintSchema)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		err := conn.QueryRowContext(cleanup, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
		if err != nil || !released.Valid || released.Int64 != 1 {
			_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: installer lock release failed", ErrGuildFingerprintSchema))
		}
	}()
	reader := guildFingerprintConn{conn}
	for _, table := range []string{s.prefix + "_" + mysqlSettingsTable, s.prefix + "_" + mysqlSettingValuesTable} {
		ok, err := guildFingerprintInnoDB(ctx, reader, s.database, table)
		if err != nil {
			return err
		}
		if !ok {
			return ErrGuildFingerprintSchema
		}
	}
	existing, ok, err := guildFingerprintTriggers(ctx, reader, s)
	if err != nil {
		return err
	}
	if !ok {
		return ErrGuildFingerprintSchema
	}
	if _, err = conn.ExecContext(ctx, s.tableDDL()); err != nil {
		return err
	}
	if ok, err = guildFingerprintTableValid(ctx, reader, s); err != nil {
		return err
	} else if !ok {
		return ErrGuildFingerprintSchema
	}
	for _, t := range s.triggers {
		if !existing[t.name] {
			if _, err = conn.ExecContext(ctx, s.triggerDDL(t)); err != nil {
				return err
			}
		}
	}
	// Triggers precede backfill; both lock source -> sidecar. Locking reads
	// cannot overwrite a newer committed writer with an old snapshot. The
	// missing-row case takes the source key gap lock.
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var digest string
	err = tx.QueryRowContext(ctx, "SELECT SHA2(entry_value,256) FROM "+s.values+" WHERE scope=? AND entry_key='state' FOR UPDATE", r.GuildRegistryScope).Scan(&digest)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, "DELETE FROM "+s.table+" WHERE scope=?", r.GuildRegistryScope)
	} else if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO "+s.table+" (scope,fingerprint) VALUES (?,?) ON DUPLICATE KEY UPDATE fingerprint=VALUES(fingerprint)", r.GuildRegistryScope, digest)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	ok, err = guildFingerprintReady(ctx, reader, s)
	if err != nil {
		return err
	}
	if !ok {
		return ErrGuildFingerprintSchema
	}
	return nil
}

// Preserve string literals while normalizing insignificant SQL formatting.
// A changed scope/key literal must never be accepted as the known trigger.
func compactGuildFingerprintSQL(s string) string {
	var out strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		b := s[i]
		if quoted {
			out.WriteByte(b)
			if b == '\\' && i+1 < len(s) {
				i++
				out.WriteByte(s[i])
			} else if b == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					out.WriteByte(s[i])
				} else {
					quoted = false
				}
			}
			continue
		}
		if b == '\'' {
			quoted = true
			out.WriteByte(b)
			continue
		}
		if b == '`' || b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			continue
		}
		out.WriteByte(b)
	}
	return out.String()
}
