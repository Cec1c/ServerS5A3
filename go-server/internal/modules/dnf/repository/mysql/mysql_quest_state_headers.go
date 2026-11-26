package mysql

import (
	"context"
	"database/sql"
	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlQuestStore) LoadQuestStatesByIDs(ctx context.Context, id string, ids ...int64) (repository.QuestStateHeaders, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return repository.QuestStateHeaders{}, false, err
	}
	if s.router.lockReads {
		record, found, err := s.Load(ctx, id)
		return repository.SelectQuestStateHeaders(record, ids...), found, err
	}
	var result repository.QuestStateHeaders
	table, err := s.router.readTable(mysqlQuestTable, id)
	if err != nil {
		return result, false, err
	}
	if err = s.router.db.QueryRowContext(ctx, "SELECT character_id FROM "+table+" WHERE character_id = ?", id).Scan(&result.CharacterID); err != nil {
		found, e := scanErr(err)
		return result, found, e
	}
	if len(ids) == 0 {
		return result, true, nil
	}
	states, err := s.router.readTable(mysqlQuestStatesTable, id)
	if err != nil {
		return result, false, err
	}
	// Bound the IN list and parameter count even for generic callers.
	for start := 0; start < len(ids); start += questIDReadBatch {
		end := min(start+questIDReadBatch, len(ids))
		args := make([]any, end-start)
		for i, id := range ids[start:end] {
			args[i] = id
		}
		for _, group := range []string{"states", "progress"} {
			values, e := loadQuestHeaderProjection(ctx, s.router, states, id, group, questIDsSuffix(len(args)), args)
			if e != nil {
				return repository.QuestStateHeaders{}, false, e
			}
			if group == "states" {
				if result.States == nil {
					result.States = values
				} else {
					for k, v := range values {
						result.States[k] = v
					}
				}
			} else {
				if result.Progress == nil {
					result.Progress = values
				} else {
					for k, v := range values {
						result.Progress[k] = v
					}
				}
			}
		}
	}
	return result, true, nil
}

// loadQuestHeaderProjection reads only the quest header columns needed by the
// narrow verification path, without loading extra rows or full quest records.
func loadQuestHeaderProjection(
	ctx context.Context,
	router mysqlRouter,
	statesTable, characterID, stateGroup, suffix string,
	selectedArgs []any,
) (map[int64]repository.QuestState, error) {
	args := append([]any{characterID, stateGroup}, selectedArgs...)
	query := router.selectQuery("SELECT quest_id, status, trigger_type, progress_value, reward_select_index, multiplier, state_updated_at FROM " + statesTable + " WHERE character_id = ? AND state_group = ?" + suffix + " ORDER BY quest_id")
	rows, err := router.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states map[int64]repository.QuestState
	var questID int64
	var state repository.QuestState
	var updatedAt sql.NullTime
	dest := []any{&questID, &state.Status, &state.TriggerType, &state.ProgressValue, &state.RewardSelectIndex, &state.Multiplier, &updatedAt}
	for rows.Next() {
		state = repository.QuestState{}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		state.UpdatedAt = scanTime(updatedAt)
		if states == nil {
			states = make(map[int64]repository.QuestState)
		}
		states[questID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return states, nil
}
