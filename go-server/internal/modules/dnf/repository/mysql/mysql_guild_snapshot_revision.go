package mysql

import (
	"context"
	"database/sql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

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
