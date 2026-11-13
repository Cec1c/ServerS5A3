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
	table, err := s.router.readTable(mysqlSettingsTable, r.GuildRegistryScope)
	if err != nil {
		return "", false, err
	}
	values, err := s.router.readTable(mysqlSettingValuesTable, r.GuildRegistryScope)
	if err != nil {
		return "", false, err
	}
	var digest sql.NullString
	// Fingerprinting the exact source also observes GM/legacy writers and
	// equal timestamps. Only the fixed-size digest crosses the SQL connection.
	query := "SELECT SHA2(v.entry_value, 256) FROM " + table + " s LEFT JOIN " + values + " v ON v.scope=s.scope AND v.entry_key='state' WHERE s.scope = ?"
	err = s.router.db.QueryRowContext(ctx, query, r.GuildRegistryScope).Scan(&digest)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return digest.String, true, nil
}
