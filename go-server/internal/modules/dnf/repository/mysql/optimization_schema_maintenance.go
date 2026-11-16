package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const recoveryIndexName = "idx_stats_pending_level"

var ErrOptimizationSchema = errors.New("optimization schema does not match the explicit maintenance contract")

type OptimizationSchemaPlan struct {
	Format         string   `json:"format"`
	TablePrefix    string   `json:"tablePrefix"`
	ReadDatabases  []string `json:"readDatabases"`
	WriteDatabases []string `json:"writeDatabases"`
	Statements     []string `json:"statements"`
}

type OptimizationSchemaState struct {
	Database      string `json:"database"`
	Writable      bool   `json:"writable"`
	GuildTable    bool   `json:"guildTable"`
	GuildTriggers int    `json:"guildTriggers"`
	GuildReady    bool   `json:"guildReady"`
	RecoveryIndex bool   `json:"recoveryIndex"`
}

type OptimizationSchemaReport struct {
	PlanSHA256 string                    `json:"planSha256"`
	Ready      bool                      `json:"ready"`
	Databases  []OptimizationSchemaState `json:"databases"`
}

// PlanOptimizationSchema returns only the two selected additive maintenance
// operations. Read replicas are never promoted to installation targets.
func PlanOptimizationSchema(options SchemaOptions) (OptimizationSchemaPlan, error) {
	p := OptimizationSchemaPlan{Format: "guild-fingerprint-level-recovery-v1"}
	var err error
	p.TablePrefix, err = normTablePrefix(options.TablePrefix)
	if err != nil {
		return p, err
	}
	p.ReadDatabases, err = r.ValidateDatabases(options.DatabasePlan.ReadDatabases)
	if err != nil {
		return p, err
	}
	p.WriteDatabases, err = r.ValidateDatabases(options.DatabasePlan.WriteDatabases)
	if err != nil {
		return p, err
	}
	if len(p.ReadDatabases) == 0 || len(p.WriteDatabases) == 0 || len(p.ReadDatabases)+len(p.WriteDatabases) > 64 || options.CreateDatabases {
		return p, ErrSchemaConfigInvalid
	}
	for _, database := range append(append([]string{}, p.ReadDatabases...), p.WriteDatabases...) {
		if _, err := newGuildFingerprintSpec(database, p.TablePrefix); err != nil {
			return p, err
		}
	}
	p.Statements, err = GuildSnapshotFingerprintDDL(SchemaOptions{TablePrefix: p.TablePrefix, DatabasePlan: r.DatabasePlan{WriteDatabases: p.WriteDatabases}})
	if err != nil {
		return p, err
	}
	for _, database := range p.WriteDatabases {
		p.Statements = append(p.Statements, recoveryIndexDDL(database, p.TablePrefix))
	}
	return p, nil
}

func (p OptimizationSchemaPlan) SHA256() string {
	value, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func recoveryIndexDDL(database, prefix string) string {
	return "ALTER TABLE " + mysqlTable(database, prefix, mysqlCharacterStatsTable) + " ADD INDEX " + recoveryIndexName + " (stat_key,stat_value,character_id)"
}

// recoveryIndexState rejects wrong column order, prefix/unique/invisible or
// ignored indexes rather than treating a matching index name as sufficient.
func recoveryIndexState(ctx context.Context, db guildFingerprintReader, database, prefix string) (bool, error) {
	if ok, err := guildFingerprintInnoDB(ctx, db, database, prefix+"_"+mysqlCharacterStatsTable); err != nil || !ok {
		return false, errors.Join(ErrOptimizationSchema, err)
	}
	rows, err := db.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='information_schema' AND TABLE_NAME='STATISTICS' AND COLUMN_NAME IN ('IGNORED','IS_VISIBLE') ORDER BY COLUMN_NAME")
	if err != nil {
		return false, err
	}
	visibility := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return false, err
		}
		if name != "IGNORED" && name != "IS_VISIBLE" {
			rows.Close()
			return false, ErrOptimizationSchema
		}
		visibility = append(visibility, name)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return false, errors.Join(err, closeErr)
	}
	extra := ""
	for _, column := range visibility {
		extra += "," + column
	}
	rows, err = db.QueryContext(ctx, "SELECT COLUMN_NAME,NON_UNIQUE,SEQ_IN_INDEX,SUB_PART,INDEX_TYPE,COLLATION"+extra+" FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND INDEX_NAME=? ORDER BY SEQ_IN_INDEX", database, prefix+"_"+mysqlCharacterStatsTable, recoveryIndexName)
	if err != nil {
		return false, err
	}
	want := []string{"stat_key", "stat_value", "character_id"}
	n, valid := 0, true
	for rows.Next() {
		var name, kind string
		var collation sql.NullString
		var unique, sequence int
		var sub sql.NullInt64
		values := make([]string, len(visibility))
		args := []any{&name, &unique, &sequence, &sub, &kind, &collation}
		for i := range values {
			args = append(args, &values[i])
		}
		if err := rows.Scan(args...); err != nil {
			rows.Close()
			return false, err
		}
		valid = valid && n < len(want) && name == want[min(n, len(want)-1)] && sequence == n+1 && unique == 1 && !sub.Valid && kind == "BTREE" && collation.Valid && collation.String == "A"
		for i, column := range visibility {
			valid = valid && ((column == "IGNORED" && values[i] == "NO") || (column == "IS_VISIBLE" && values[i] == "YES"))
		}
		n++
	}
	err = rows.Err()
	closeErr = rows.Close()
	if err != nil || closeErr != nil {
		return false, errors.Join(err, closeErr)
	}
	if n == 0 {
		return false, nil
	}
	if !valid || n != len(want) {
		return false, ErrOptimizationSchema
	}
	return true, nil
}

// InspectOptimizationSchema is read-only and distinguishes a known partial
// installation from an unknown object. A missing base table is never created.
func InspectOptimizationSchema(ctx context.Context, db *sql.DB, options SchemaOptions) (OptimizationSchemaReport, error) {
	p, err := PlanOptimizationSchema(options)
	if err != nil {
		return OptimizationSchemaReport{}, err
	}
	if db == nil {
		return OptimizationSchemaReport{}, ErrMySQLDBRequired
	}
	ctx = ctxOrBackground(ctx)
	reader := SQLDBAdapter{DB: db}
	all, _ := r.ValidateDatabases(append(append([]string{}, p.ReadDatabases...), p.WriteDatabases...))
	out := OptimizationSchemaReport{PlanSHA256: p.SHA256()}
	allReady := true
	for _, database := range all {
		s, _ := newGuildFingerprintSpec(database, p.TablePrefix)
		state := OptimizationSchemaState{Database: database}
		for _, write := range p.WriteDatabases {
			state.Writable = state.Writable || write == database
		}
		for _, table := range []string{s.prefix + "_" + mysqlSettingsTable, s.prefix + "_" + mysqlSettingValuesTable} {
			if ok, err := guildFingerprintInnoDB(ctx, reader, database, table); err != nil || !ok {
				return out, errors.Join(ErrOptimizationSchema, err)
			}
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", database, p.TablePrefix+"_"+guildFingerprintTable).Scan(&count); err != nil {
			return out, err
		}
		state.GuildTable = count == 1
		if state.GuildTable {
			if ok, err := guildFingerprintTableValid(ctx, reader, s); err != nil || !ok {
				return out, errors.Join(ErrOptimizationSchema, err)
			}
		}
		triggers, valid, err := guildFingerprintTriggers(ctx, reader, s)
		if err != nil || !valid {
			return out, errors.Join(ErrOptimizationSchema, err)
		}
		state.GuildTriggers = len(triggers)
		if state.GuildTable && state.GuildTriggers == 3 {
			state.GuildReady, err = guildFingerprintReady(ctx, reader, s)
			if err != nil {
				return out, err
			}
		}
		state.RecoveryIndex, err = recoveryIndexState(ctx, reader, database, p.TablePrefix)
		if err != nil {
			return out, err
		}
		allReady = allReady && state.GuildReady && state.RecoveryIndex
		out.Databases = append(out.Databases, state)
	}
	out.Ready = allReady
	return out, nil
}

// ApplyOptimizationSchema requires a caller-owned maintenance window and
// explicit AutoCreate. It preflights every shard before the first write,
// reuses the original locked guild installer and never restores business data.
// Fully ready installations are a read-only no-op. Partial/unknown outcomes
// must be inspected by the publisher before any newly authorized repair task.
func ApplyOptimizationSchema(ctx context.Context, db *sql.DB, options SchemaOptions) (OptimizationSchemaReport, error) {
	if !options.AutoCreate {
		return OptimizationSchemaReport{}, ErrSchemaConfigInvalid
	}
	ctx, cancel := context.WithTimeout(ctxOrBackground(ctx), 2*time.Minute)
	defer cancel()
	before, err := InspectOptimizationSchema(ctx, db, options)
	if err != nil || before.Ready {
		return before, err
	}
	p, _ := PlanOptimizationSchema(options)
	for _, state := range before.Databases {
		if !state.Writable {
			continue
		}
		if !state.GuildReady {
			one := SchemaOptions{AutoCreate: true, TablePrefix: p.TablePrefix, DatabasePlan: r.DatabasePlan{WriteDatabases: []string{state.Database}}}
			if err := EnsureGuildSnapshotFingerprint(ctx, db, one); err != nil {
				return before, err
			}
		}
		if !state.RecoveryIndex {
			if err := installRecoveryIndex(ctx, db, state.Database, p.TablePrefix); err != nil {
				return before, err
			}
		}
	}
	return InspectOptimizationSchema(ctx, db, options)
}

func installRecoveryIndex(ctx context.Context, db *sql.DB, database, prefix string) (resultErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	hash := sha256.Sum256([]byte(database + "/" + prefix))
	lock := fmt.Sprintf("level-recovery-index-%x", hash[:16])
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", lock).Scan(&got); err != nil {
		return err
	}
	if !got.Valid || got.Int64 != 1 {
		return fmt.Errorf("%w: recovery index installer busy", ErrOptimizationSchema)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		err := conn.QueryRowContext(cleanup, "SELECT RELEASE_LOCK(?)", lock).Scan(&released)
		if err != nil || !released.Valid || released.Int64 != 1 {
			_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
			resultErr = errors.Join(resultErr, ErrOptimizationSchema, err)
		}
	}()
	reader := guildFingerprintConn{conn}
	if ready, err := recoveryIndexState(ctx, reader, database, prefix); ready || err != nil {
		return err
	}
	// Bound metadata-lock waiting separately from the actual index build.
	var oldWait int64
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.lock_wait_timeout").Scan(&oldWait); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION lock_wait_timeout=5"); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanup, "SET SESSION lock_wait_timeout=?", oldWait); err != nil {
			_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
			resultErr = errors.Join(resultErr, err)
		}
	}()
	_, err = conn.ExecContext(ctx, recoveryIndexDDL(database, prefix))
	if err != nil {
		return err
	}
	if ready, err := recoveryIndexState(ctx, reader, database, prefix); err != nil || !ready {
		return errors.Join(ErrOptimizationSchema, err)
	}
	return nil
}
