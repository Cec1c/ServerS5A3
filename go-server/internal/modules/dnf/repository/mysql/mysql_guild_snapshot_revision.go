package mysql

import (
	"context"
	"database/sql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// guildReadSettings memoizes the registry fingerprint for exactly one READ
// ONLY REPEATABLE READ transaction. InnoDB's consistent read cannot observe a
// different registry row for the life of that snapshot, so repeated calls from
// one business request may reuse the first successful read. The memo is never
// attached to ordinary stores, write transactions or ReadCommitted views.
type guildReadSettings struct {
	*mysqlSettingsStore
	revisionLoaded bool
	revision       string
	revisionFound  bool
}

var (
	_ r.SettingsRepository           = (*guildReadSettings)(nil)
	_ r.GuildSnapshotRevisionReader  = (*guildReadSettings)(nil)
	_ r.GuildSettlementReceiptReader = (*guildReadSettings)(nil)
)

func newGuildReadSettings(store *mysqlSettingsStore) *guildReadSettings {
	return &guildReadSettings{mysqlSettingsStore: store}
}

// LoadGuildSnapshotRevision reuses only a successful result. Errors leave the
// memo unset so a later call retries instead of turning a transient failure
// into a permanent empty registry.
func (s *guildReadSettings) LoadGuildSnapshotRevision(ctx context.Context) (string, bool, error) {
	if s.revisionLoaded {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return "", false, err
			}
		}
		return s.revision, s.revisionFound, nil
	}
	revision, found, err := s.mysqlSettingsStore.LoadGuildSnapshotRevision(ctx)
	if err != nil {
		return "", false, err
	}
	s.revision, s.revisionFound, s.revisionLoaded = revision, found, true
	return revision, found, nil
}

// reset runs when WithinGuildRead leaves its transaction scope. A view that
// escapes the callback must not serve a stale revision into a later request.
func (s *guildReadSettings) reset() {
	if s == nil {
		return
	}
	s.revisionLoaded = false
	s.revision = ""
	s.revisionFound = false
}

func (s *mysqlSettingsStore) GuildSnapshotNamespace() *r.GuildSnapshotNamespace {
	return s.router.guildSnapshotNamespace
}

func (s *mysqlSettingsStore) LoadGuildSnapshotRevision(ctx context.Context) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	table, err := s.router.readTable(mysqlSettingsTable, r.GuildRegistryScope)
	if err != nil {
		return "", false, err
	}
	values, err := s.router.readTable(mysqlSettingValuesTable, r.GuildRegistryScope)
	if err != nil {
		return "", false, err
	}
	// Fingerprinting the exact source also observes GM/legacy writers and
	// equal timestamps. Only the fixed-size digest crosses the SQL connection.
	sourceQuery := "SELECT SHA2(v.entry_value, 256) FROM " + table + " s LEFT JOIN " + values + " v ON v.scope=s.scope AND v.entry_key='state' WHERE s.scope = ?"
	if !s.router.storedGuildFingerprint {
		return scanGuildSnapshotSourceDigest(ctx, s.router.db, sourceQuery)
	}
	fingerprints, tableErr := s.router.readTable(guildFingerprintTable, r.GuildRegistryScope)
	if tableErr != nil {
		return "", false, tableErr
	}

	// Stage 1 checks source key existence via the primary key index without
	// touching off-page LONGTEXT chains on dnf_setting_values, while guarding
	// against orphan sidecar rows after a source DELETE/TRUNCATE.
	fastQuery := "SELECT EXISTS(SELECT 1 FROM " + values + " e WHERE e.scope=s.scope AND e.entry_key='state'), f.fingerprint FROM " + table + " s LEFT JOIN " + fingerprints + " f ON f.scope=s.scope WHERE s.scope = ?"
	execDB := s.router.db
	if guildRevisionHasTransaction(execDB) {
		return loadGuildSnapshotRevisionSnapshot(ctx, execDB, sourceQuery, fastQuery)
	}

	// The common case is one self-consistent SELECT. InnoDB gives that single
	// statement its own read view, so a sidecar hit no longer needs an explicit
	// REPEATABLE READ transaction, BEGIN or COMMIT.
	var hasValue bool
	var fp sql.NullString
	hasValue, fp, err = scanGuildSnapshotFingerprint(ctx, execDB, fastQuery)
	if err != nil {
		if guildFingerprintOptionalSchemaError(err) {
			return scanGuildSnapshotSourceDigest(ctx, execDB, sourceQuery)
		}
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if !hasValue {
		// Settings scope row exists without a 'state' value row: match the
		// legacy NULL digest semantics and never expose an orphan fingerprint.
		return "", true, nil
	}
	if fp.Valid {
		return fp.String, true, nil
	}

	// A missing sidecar fingerprint is the rare path. Re-enter a read-only
	// REPEATABLE READ snapshot and repeat Stage 1 there so the existence/orphan
	// guard and SHA2 fallback observe one snapshot, including after TRUNCATE.
	if beginner, ok := execDB.(sqlTxBeginner); ok {
		tx, txErr := beginObservedSQLTransaction(ctx, beginner, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if txErr != nil {
			return "", false, txErr
		}
		defer tx.Rollback()
		scoped := newSQLTransactionDB(tx.Tx, execDB)
		result, found, loadErr := loadGuildSnapshotRevisionSnapshot(ctx, scoped, sourceQuery, fastQuery)
		if loadErr != nil {
			return "", false, loadErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", false, commitErr
		}
		return result, found, nil
	}
	// Minimal read-only adapters that cannot begin a transaction retain the
	// previous two-statement capability. Production adapters all expose BeginTx.
	return scanGuildSnapshotSourceDigest(ctx, execDB, sourceQuery)
}

func loadGuildSnapshotRevisionSnapshot(ctx context.Context, database SQLDB, sourceQuery, fastQuery string) (string, bool, error) {
	hasValue, fp, err := scanGuildSnapshotFingerprint(ctx, database, fastQuery)
	if err != nil {
		if guildFingerprintOptionalSchemaError(err) {
			return scanGuildSnapshotSourceDigest(ctx, database, sourceQuery)
		}
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if !hasValue {
		return "", true, nil
	}
	if fp.Valid {
		return fp.String, true, nil
	}
	// Stage 2 shares the caller-owned snapshot with Stage 1.
	return scanGuildSnapshotSourceDigest(ctx, database, sourceQuery)
}

func scanGuildSnapshotFingerprint(ctx context.Context, database SQLDB, query string) (bool, sql.NullString, error) {
	var hasValue bool
	var fp sql.NullString
	err := database.QueryRowContext(ctx, query, r.GuildRegistryScope).Scan(&hasValue, &fp)
	return hasValue, fp, err
}

func scanGuildSnapshotSourceDigest(ctx context.Context, db SQLDB, query string) (string, bool, error) {
	var digest sql.NullString
	err := db.QueryRowContext(ctx, query, r.GuildRegistryScope).Scan(&digest)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return digest.String, true, nil
}

// Inspect wrappers without starting a nested transaction.
func guildRevisionHasTransaction(database SQLDB) bool {
	switch db := database.(type) {
	case sqlTransactionDB, *sqlTransactionDB:
		return true
	case *ObservedSQLDB:
		return db != nil && guildRevisionHasTransaction(db.SQLDB)
	default:
		return false
	}
}
