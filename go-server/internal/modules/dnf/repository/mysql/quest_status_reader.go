package mysql

import (
	"context"
	"database/sql"
	"errors"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.QuestStatusReader = (*mysqlQuestStore)(nil)

func (s *mysqlQuestStore) LoadQuestStatus(ctx context.Context, characterID string, questID int64) (string, bool, error) {
	questsTable, err := s.router.readTable(mysqlQuestTable, characterID)
	if err != nil {
		return "", false, err
	}

	// A quest state is only meaningful when its parent character record exists.
	var parentCharacterID string
	err = s.router.db.QueryRowContext(
		ctx,
		s.router.selectQuery("SELECT character_id FROM "+questsTable+" WHERE character_id = ?"),
		characterID,
	).Scan(&parentCharacterID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}

	statesTable, err := s.router.readTable(mysqlQuestStatesTable, characterID)
	if err != nil {
		return "", false, err
	}
	status, found, err := s.loadQuestStatusGroup(ctx, statesTable, characterID, "states", questID)
	if err != nil || found {
		return status, found, err
	}
	return s.loadQuestStatusGroup(ctx, statesTable, characterID, "progress", questID)
}

func (s *mysqlQuestStore) loadQuestStatusGroup(
	ctx context.Context,
	table, characterID, stateGroup string,
	questID int64,
) (string, bool, error) {
	var status string
	err := s.router.db.QueryRowContext(
		ctx,
		s.router.selectQuery("SELECT status FROM "+table+" WHERE character_id = ? AND state_group = ? AND quest_id = ? LIMIT 1"),
		characterID,
		stateGroup,
		questID,
	).Scan(&status)
	if err == nil {
		return status, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return "", false, err
}
