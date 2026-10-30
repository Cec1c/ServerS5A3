package mysql

import (
	"context"
	"database/sql"
	"longheng.io/server/internal/modules/dnf/repository"
	"sort"
)

const mysqlQuestTable = "quests"

type mysqlQuestStore struct {
	mysqlStoreBase
}

func (s *mysqlQuestStore) Load(ctx context.Context, characterID string) (repository.QuestRecord, bool, error) {
	table, err := s.router.readTable(mysqlQuestTable, characterID)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	statesTable, err := s.router.readTable(mysqlQuestStatesTable, characterID)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	extraTable, err := s.router.readTable(mysqlQuestExtraTable, characterID)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	query := s.router.selectQuery("SELECT character_id, updated_at FROM " + table + " WHERE character_id = ?")
	var record repository.QuestRecord
	var updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, query, characterID).Scan(
		&record.CharacterID,
		&updatedAt,
	)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.QuestRecord{}, ok, scanErr
	}
	record.States, err = loadQuestStates(ctx, s.router, statesTable, extraTable, characterID, "states")
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	record.Progress, err = loadQuestStates(ctx, s.router, statesTable, extraTable, characterID, "progress")
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	// Every map above was allocated by this load; no store-owned snapshot escapes.
	return record, true, nil
}

func (s *mysqlQuestStore) Save(ctx context.Context, record repository.QuestRecord) error {
	return s.SaveFields(ctx, record, repository.AllQuestFields()...)
}

func (s *mysqlQuestStore) SaveFields(ctx context.Context, record repository.QuestRecord, fields ...repository.QuestField) error {
	if record.PlanningPartial {
		return repository.ErrQuestPartialSave
	}
	characterID, err := requireRecordKey(repository.QuestKey, record, "quest")
	if err != nil {
		return err
	}
	fields = repository.QuestFields.Normalize(fields)
	if len(fields) == 0 {
		return nil
	}
	table, err := s.router.writeTable(mysqlQuestTable, characterID)
	if err != nil {
		return err
	}
	statesTable, err := s.router.writeTable(mysqlQuestStatesTable, characterID)
	if err != nil {
		return err
	}
	extraTable, err := s.router.writeTable(mysqlQuestExtraTable, characterID)
	if err != nil {
		return err
	}
	columns := []string{"character_id", "updated_at"}
	args := []any{characterID, timeOrNow(record.UpdatedAt, s.router.now)}
	updates := []string{updateValue("updated_at")}
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if _, execErr := database.ExecContext(ctx, buildUpsert(table, columns, updates), args...); execErr != nil {
			return execErr
		}
		for _, field := range fields {
			switch field {
			case repository.QuestFieldStates:
				if err := replaceQuestStates(ctx, database, statesTable, extraTable, characterID, "states", record.States); err != nil {
					return err
				}
			case repository.QuestFieldProgress:
				if err := replaceQuestStates(ctx, database, statesTable, extraTable, characterID, "progress", record.Progress); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func loadQuestStates(
	ctx context.Context,
	router mysqlRouter,
	statesTable, extraTable, characterID, stateGroup string,
) (map[int64]repository.QuestState, error) {
	return loadSelectedQuestStates(ctx, router, statesTable, extraTable, characterID, stateGroup, "", nil)
}

// suffix and arguments are built internally; values never become SQL text.
func loadSelectedQuestStates(ctx context.Context, router mysqlRouter, statesTable, extraTable, characterID, stateGroup, suffix string, selectedArgs []any) (map[int64]repository.QuestState, error) {
	return loadQuestStatesWithActiveExtras(ctx, router, statesTable, extraTable, characterID, stateGroup, suffix, selectedArgs, false)
}

func loadQuestStatesWithActiveExtras(ctx context.Context, router mysqlRouter, statesTable, extraTable, characterID, stateGroup, suffix string, selectedArgs []any, activeExtras bool) (map[int64]repository.QuestState, error) {
	args := append([]any{characterID, stateGroup}, selectedArgs...)
	query := router.selectQuery("SELECT quest_id, status, trigger_type, progress_value, reward_select_index, multiplier, state_updated_at FROM " + statesTable + " WHERE character_id = ? AND state_group = ?" + suffix + " ORDER BY quest_id")
	rows, err := router.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var states map[int64]repository.QuestState
	for rows.Next() {
		var questID int64
		var state repository.QuestState
		var updatedAt sql.NullTime
		if err := rows.Scan(
			&questID,
			&state.Status,
			&state.TriggerType,
			&state.ProgressValue,
			&state.RewardSelectIndex,
			&state.Multiplier,
			&updatedAt,
		); err != nil {
			rows.Close()
			return nil, err
		}
		state.UpdatedAt = scanTime(updatedAt)
		if states == nil {
			states = make(map[int64]repository.QuestState)
		}
		states[questID] = state
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if activeExtras {
		active := make([]int64, 0)
		for id, state := range states {
			if repository.QuestStatusActive(state.Status) {
				active = append(active, id)
			}
		}
		if len(active) == 0 {
			return states, nil
		}
		sort.Slice(active, func(i, j int) bool { return active[i] < active[j] })
		// Headers were already loaded. Fetch only active extras, in bounded
		// batches, without another header query for each group.
		for start := 0; start < len(active); start += questIDReadBatch {
			end := min(start+questIDReadBatch, len(active))
			selectedArgs := make([]any, end-start)
			for i, id := range active[start:end] {
				selectedArgs[i] = id
			}
			if err := loadQuestExtrasInto(ctx, router, extraTable, characterID, stateGroup, questIDsSuffix(len(selectedArgs)), selectedArgs, states); err != nil {
				return nil, err
			}
		}
		return states, nil
	}
	if err := loadQuestExtrasInto(ctx, router, extraTable, characterID, stateGroup, suffix, selectedArgs, states); err != nil {
		return nil, err
	}
	return states, nil
}

func loadQuestExtrasInto(ctx context.Context, router mysqlRouter, extraTable, characterID, stateGroup, suffix string, selectedArgs []any, states map[int64]repository.QuestState) error {
	args := append([]any{characterID, stateGroup}, selectedArgs...)
	extras, err := router.db.QueryContext(
		ctx,
		router.selectQuery("SELECT quest_id, extra_key, extra_value FROM "+extraTable+" WHERE character_id = ? AND state_group = ?"+suffix+" ORDER BY quest_id, extra_key"),
		args...,
	)
	if err != nil {
		return err
	}
	defer extras.Close()
	for extras.Next() {
		var questID int64
		var key, value string
		if err := extras.Scan(&questID, &key, &value); err != nil {
			return err
		}
		state, ok := states[questID]
		if !ok {
			continue
		}
		if state.Extra == nil {
			state.Extra = make(map[string]string)
		}
		state.Extra[key] = value
		states[questID] = state
	}
	if err := extras.Err(); err != nil {
		return err
	}
	return nil
}

func replaceQuestStates(
	ctx context.Context,
	database SQLDB,
	statesTable, extraTable, characterID, stateGroup string,
	states map[int64]repository.QuestState,
) error {
	if _, err := database.ExecContext(ctx, "DELETE FROM "+extraTable+" WHERE character_id = ? AND state_group = ?", characterID, stateGroup); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM "+statesTable+" WHERE character_id = ? AND state_group = ?", characterID, stateGroup); err != nil {
		return err
	}
	// Quest writes used to ignore the existing INSERT batch setting. A character
	// with hundreds of archived quests therefore rewrote every state and extra
	// in a separate round trip on each NPC action. Reuse the bounded batcher
	// inside the same enclosing transaction; batch=1 preserves the old stream.
	stateBatch := newRowInsertBatcher(ctx, database, characterID, insertShape{
		table: statesTable, collection: stateGroup,
		prefix:       "INSERT INTO " + statesTable + " (character_id, state_group, quest_id, status, trigger_type, progress_value, reward_select_index, multiplier, state_updated_at) VALUES ",
		placeholders: "(?, ?, ?, ?, ?, ?, ?, ?, ?)", columns: 9,
	})
	extraBatch := newRowInsertBatcher(ctx, database, characterID, insertShape{
		table: extraTable, collection: stateGroup,
		prefix:       "INSERT INTO " + extraTable + " (character_id, state_group, quest_id, extra_key, extra_value) VALUES ",
		placeholders: "(?, ?, ?, ?, ?)", columns: 5,
	})
	questIDs := make([]int64, 0, len(states))
	for questID := range states {
		questIDs = append(questIDs, questID)
	}
	sort.Slice(questIDs, func(left, right int) bool { return questIDs[left] < questIDs[right] })
	for _, questID := range questIDs {
		state := states[questID]
		if err := stateBatch.add(
			characterID,
			stateGroup,
			questID,
			state.Status,
			state.TriggerType,
			state.ProgressValue,
			state.RewardSelectIndex,
			state.Multiplier,
			sqlTime(state.UpdatedAt),
		); err != nil {
			return err
		}
		for _, key := range sortedStringKeys(state.Extra) {
			if err := extraBatch.add(characterID, stateGroup, questID, key, state.Extra[key]); err != nil {
				return err
			}
		}
	}
	if err := stateBatch.flush(); err != nil {
		return err
	}
	return extraBatch.flush()
}
