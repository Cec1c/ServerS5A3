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

	execDB := s.router.db
	var readTx *observedSQLTransaction
	if _, inTx := execDB.(sqlTransactionDB); !inTx {
		if beginner, ok := execDB.(sqlTxBeginner); ok {
			tx, txErr := beginObservedSQLTransaction(ctx, beginner, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if txErr != nil {
				return "", false, txErr
			}
			readTx = tx
			defer readTx.Rollback()
			execDB = newSQLTransactionDB(readTx.Tx, s.router.db)
		}
	}

	// Stage 1 checks source key existence via the primary key index without
	// touching off-page LONGTEXT chains on dnf_setting_values, while guarding
	// against orphan sidecar rows after a source DELETE/TRUNCATE.
	fastQuery := "SELECT EXISTS(SELECT 1 FROM " + values + " e WHERE e.scope=s.scope AND e.entry_key='state'), f.fingerprint FROM " + table + " s LEFT JOIN " + fingerprints + " f ON f.scope=s.scope WHERE s.scope = ?"
	var hasValue bool
	var fp sql.NullString
	err = execDB.QueryRowContext(ctx, fastQuery, r.GuildRegistryScope).Scan(&hasValue, &fp)
	if err != nil {
		if guildFingerprintOptionalSchemaError(err) {
			digest, found, fbErr := scanGuildSnapshotSourceDigest(ctx, execDB, sourceQuery)
			if fbErr != nil {
				return "", false, fbErr
			}
			if readTx != nil {
				if commitErr := readTx.Commit(); commitErr != nil {
					return "", false, commitErr
				}
			}
			return digest, found, nil
		}
		if err == sql.ErrNoRows {
			if readTx != nil {
				if commitErr := readTx.Commit(); commitErr != nil {
					return "", false, commitErr
				}
			}
			return "", false, nil
		}
		return "", false, err
	}

	var result string
	switch {
	case !hasValue:
		// Settings scope row exists without a 'state' value row: match the
		// legacy NULL digest semantics and never expose an orphan fingerprint.
		result = ""
	case fp.Valid:
		result = fp.String
	default:
		// Stage 2 shares the same REPEATABLE READ snapshot when a sidecar row
		// has not been populated yet.
		var found bool
		result, found, err = scanGuildSnapshotSourceDigest(ctx, execDB, sourceQuery)
		if err != nil {
			return "", false, err
		}
		if !found {
			if readTx != nil {
				if commitErr := readTx.Commit(); commitErr != nil {
					return "", false, commitErr
				}
			}
			return "", false, nil
		}
	}
	if readTx != nil {
		if commitErr := readTx.Commit(); commitErr != nil {
			return "", false, commitErr
		}
	}
	return result, true, nil
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
