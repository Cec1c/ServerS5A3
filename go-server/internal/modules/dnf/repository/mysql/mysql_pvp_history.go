package mysql

import (
	"context"
	"database/sql"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
	"strconv"
	"strings"
)

func (s *mysqlCharStore) LastDeletedProfession(ctx context.Context, accountID string, job, grow byte) (repository.CharacterRecord, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	if strings.TrimSpace(accountID) == "" {
		return repository.CharacterRecord{}, false, db.ErrRecordKeyRequired
	}
	var latest repository.CharacterRecord
	found := false
	for _, database := range s.router.readDBs {
		table := mysqlTable(database, s.router.tablePrefix, mysqlCharTable)
		query := s.router.selectQuery("SELECT character_id FROM " + table + " WHERE account_id = ? AND job = ? AND (grow_type & 15) = ? AND delete_flag = 1 ORDER BY updated_at DESC, character_id DESC LIMIT 1")
		var id string
		err := s.router.db.QueryRowContext(ctx, query, accountID, strconv.Itoa(int(job)), grow).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return repository.CharacterRecord{}, false, err
		}
		record, ok, err := s.Load(ctx, id)
		if err != nil {
			return repository.CharacterRecord{}, false, err
		}
		if ok && (!found || record.UpdatedAt.After(latest.UpdatedAt)) {
			latest, found = record, true
		}
	}
	return latest, found, nil
}
