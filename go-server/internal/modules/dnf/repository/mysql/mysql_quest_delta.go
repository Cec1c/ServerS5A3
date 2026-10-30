package mysql

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

const questIDReadBatch = 128

func (s *mysqlQuestStore) LoadQuestPlanning(ctx context.Context, id string) (repository.QuestRecord, bool, error) {
	table, err := s.router.readTable(mysqlQuestTable, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	var r repository.QuestRecord
	var updated sql.NullTime
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT character_id, updated_at FROM "+table+" WHERE character_id = ?"), id).Scan(&r.CharacterID, &updated)
	if err != nil {
		ok, e := scanErr(err)
		return repository.QuestRecord{}, ok, e
	}
	r.UpdatedAt = scanTime(updated)
	r.PlanningPartial = true
	states, err := s.router.readTable(mysqlQuestStatesTable, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	extras, err := s.router.readTable(mysqlQuestExtraTable, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	r.States, err = loadQuestStatesWithActiveExtras(ctx, s.router, states, extras, id, "states", "", nil, true)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	r.Progress, err = loadQuestStatesWithActiveExtras(ctx, s.router, states, extras, id, "progress", "", nil, true)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	return r, true, nil
}

func (s *mysqlQuestStore) LoadQuestIDs(ctx context.Context, id string, ids []int64) (repository.QuestRecord, bool, error) {
	// Used to verify committed deltas: read the write shard, never a lagging
	// replica. Do not take locks outside the write transaction.
	table, err := s.router.writeTable(mysqlQuestTable, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	var record repository.QuestRecord
	var updated sql.NullTime
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT character_id, updated_at FROM "+table+" WHERE character_id = ?"), id).Scan(&record.CharacterID, &updated)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.QuestRecord{}, ok, scanErr
	}
	record.UpdatedAt = scanTime(updated)
	record.PlanningPartial = true
	states, err := s.router.writeTable(mysqlQuestStatesTable, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	extras, err := s.router.writeTable(mysqlQuestExtraTable, id)
	if err != nil {
		return repository.QuestRecord{}, false, err
	}
	for _, group := range []string{"states", "progress"} {
		selected, err := loadQuestIDGroup(ctx, s.router, states, extras, id, group, ids)
		if err != nil {
			return repository.QuestRecord{}, false, err
		}
		if group == "states" {
			record.States = selected
		} else {
			record.Progress = selected
		}
	}
	return record, true, nil
}

func loadQuestIDGroup(ctx context.Context, router mysqlRouter, states, extras, id, group string, ids []int64) (map[int64]repository.QuestState, error) {
	unique := make(map[int64]struct{}, len(ids))
	for _, questID := range ids {
		unique[questID] = struct{}{}
	}
	ordered := make([]int64, 0, len(unique))
	for questID := range unique {
		ordered = append(ordered, questID)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var result map[int64]repository.QuestState
	for start := 0; start < len(ordered); start += questIDReadBatch {
		end := min(start+questIDReadBatch, len(ordered))
		args := make([]any, end-start)
		for i, questID := range ordered[start:end] {
			args[i] = questID
		}
		suffix := questIDsSuffix(len(args))
		chunk, err := loadSelectedQuestStates(ctx, router, states, extras, id, group, suffix, args)
		if err != nil {
			return nil, err
		}
		if len(chunk) > 0 {
			if result == nil {
				result = make(map[int64]repository.QuestState)
			}
			maps.Copy(result, chunk)
		}
	}
	return result, nil
}

func questIDsSuffix(count int) string {
	return " AND quest_id IN (" + strings.TrimSuffix(strings.Repeat("?,", count), ",") + ")"
}

func (s *mysqlQuestStore) SaveQuestChanges(ctx context.Context, before, after repository.QuestRecord, fields ...repository.QuestField) error {
	id, err := requireRecordKey(repository.QuestKey, after, "quest")
	if err != nil {
		return err
	}
	if repository.QuestKey(before) != id {
		return repository.ErrQuestVersionConflict
	}
	fields = repository.QuestFields.Normalize(fields)
	type change struct {
		group         string
		before, after map[int64]repository.QuestState
		ids           []int64
	}
	changes := make([]change, 0, len(fields))
	for _, field := range fields {
		group, old, next := "states", before.States, after.States
		if field == repository.QuestFieldProgress {
			group, old, next = "progress", before.Progress, after.Progress
		}
		ids := repository.ChangedQuestIDs(old, next)
		if len(ids) > 0 {
			changes = append(changes, change{group, old, next, ids})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	// Refuse a non-transactional adapter: a partial delta must never escape.
	if _, tx := s.router.db.(sqlTransactionDB); !tx {
		if _, begins := s.router.db.(sqlTxBeginner); !begins {
			return errors.New("quest delta requires SQL transaction")
		}
	}
	table, err := s.router.writeTable(mysqlQuestTable, id)
	if err != nil {
		return err
	}
	states, err := s.router.writeTable(mysqlQuestStatesTable, id)
	if err != nil {
		return err
	}
	extras, err := s.router.writeTable(mysqlQuestExtraTable, id)
	if err != nil {
		return err
	}
	return withMySQLWriteExecutor(ctx, s.router.db, func(db SQLDB) error {
		router := s.router
		router.db = db
		router.lockReads = true
		// All ordinary saves upsert this same parent before touching children.
		// Locking it also serializes absence checks against add/give-up/finish.
		var lockedID string
		err := db.QueryRowContext(ctx, "SELECT character_id FROM "+table+" WHERE character_id = ? FOR UPDATE", id).Scan(&lockedID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return repository.ErrQuestVersionConflict
			}
			return err
		}
		if lockedID != id {
			return repository.ErrQuestVersionConflict
		}
		for _, change := range changes {
			current, err := loadQuestIDGroup(ctx, router, states, extras, id, change.group, change.ids)
			if err != nil {
				return err
			}
			for _, questID := range change.ids {
				actual, exists := current[questID]
				expected, had := change.before[questID]
				if exists != had || (had && !repository.QuestStatesEqual(actual, expected)) {
					return repository.ErrQuestVersionConflict
				}
			}
		}
		for _, change := range changes {
			for _, questID := range change.ids {
				old, next := change.before[questID], change.after[questID]
				_, exists := change.after[questID]
				if !exists {
					for _, t := range []string{extras, states} {
						if _, err := db.ExecContext(ctx, "DELETE FROM "+t+" WHERE character_id = ? AND state_group = ? AND quest_id = ?", id, change.group, questID); err != nil {
							return err
						}
					}
					continue
				}
				columns := []string{"character_id", "state_group", "quest_id", "status", "trigger_type", "progress_value", "reward_select_index", "multiplier", "state_updated_at"}
				updates := []string{}
				for _, col := range columns[3:] {
					updates = append(updates, updateValue(col))
				}
				if _, err := db.ExecContext(ctx, buildUpsert(states, columns, updates), id, change.group, questID, next.Status, next.TriggerType, next.ProgressValue, next.RewardSelectIndex, next.Multiplier, sqlTime(next.UpdatedAt)); err != nil {
					return err
				}
				for _, key := range sortedStringKeys(old.Extra) {
					if _, kept := next.Extra[key]; kept {
						continue
					}
					if _, err := db.ExecContext(ctx, "DELETE FROM "+extras+" WHERE character_id = ? AND state_group = ? AND quest_id = ? AND extra_key = ?", id, change.group, questID, key); err != nil {
						return err
					}
				}
				for _, key := range sortedStringKeys(next.Extra) {
					if value, had := old.Extra[key]; had && value == next.Extra[key] {
						continue
					}
					q := buildUpsert(extras, []string{"character_id", "state_group", "quest_id", "extra_key", "extra_value"}, []string{updateValue("extra_value")})
					if _, err := db.ExecContext(ctx, q, id, change.group, questID, key, next.Extra[key]); err != nil {
						return err
					}
				}
			}
		}
		_, err = db.ExecContext(ctx, "UPDATE "+table+" SET updated_at = ? WHERE character_id = ?", timeOrNow(after.UpdatedAt, s.router.now), id)
		return err
	})
}

func (r *characterSettlementScopedQuestRepository) LoadQuestIDs(ctx context.Context, id string, ids []int64) (repository.QuestRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.QuestRecord{}, false, err
	}
	return repository.LoadQuestIDs(ctx, r.base, id, ids)
}

func (r *characterSettlementScopedQuestRepository) LoadQuestPlanning(ctx context.Context, id string) (repository.QuestRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.QuestRecord{}, false, err
	}
	// A fallback repository can still return a complete record here.
	return repository.LoadQuestPlanning(ctx, r.base, id)
}

func (r *characterSettlementScopedQuestRepository) SaveQuestChanges(ctx context.Context, before, after repository.QuestRecord, fields ...repository.QuestField) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, before.CharacterID); err != nil {
		return err
	}
	if err := repository.TransactionKeyError(ctx, r.characterID, after.CharacterID); err != nil {
		return err
	}
	return repository.SaveQuestChanges(ctx, r.base, before, after, fields...)
}
