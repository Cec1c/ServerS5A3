package repository

import (
	"context"
	"maps"
	"reflect"
)

// Active planning includes both groups for every active ID, so an inactive
// States row still shadows an active Progress row. It requires delta writes.
type QuestActivePlanningReader interface {
	LoadQuestActivePlanning(context.Context, string) (QuestRecord, bool, error)
}

func LoadQuestActivePlanning(ctx context.Context, r QuestRepository, id string) (QuestRecord, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return QuestRecord{}, false, ctx.Err()
	}
	reader, ok := r.(QuestActivePlanningReader)
	_, delta := r.(QuestDeltaWriter)
	if !ok || !delta {
		return LoadQuestPlanning(ctx, r, id)
	}
	scope := planningScope(ctx, r, id)
	if scope != nil && scope.activeLoaded {
		return CloneQuest(scope.activeRecord), scope.activeFound, nil
	}
	record, found, err := reader.LoadQuestActivePlanning(ctx, id)
	if scope != nil && err == nil {
		scope.activeRecord, scope.activeFound, scope.activeLoaded = CloneQuest(record), found, true
	}
	return record, found, err
}

// key identifies the immutable catalog used for dependency expansion. The
// request scope retains at most one such view; all quest writes invalidate it.
func LoadQuestDependentPlanning(ctx context.Context, r QuestRepository, id string, key any, dependencies func(QuestRecord) []int64) (QuestRecord, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return QuestRecord{}, false, ctx.Err()
	}
	_, narrow := r.(QuestActivePlanningReader)
	_, delta := r.(QuestDeltaWriter)
	idsReader, selective := r.(QuestIDReader)
	if !narrow || !delta || !selective || dependencies == nil {
		return LoadQuestPlanning(ctx, r, id)
	}
	scope := planningScope(ctx, r, id)
	cacheable := key != nil && reflect.TypeOf(key).Comparable()
	if cacheable && scope != nil && scope.dependentLoaded && scope.dependentKey == key {
		return CloneQuest(scope.dependentRecord), scope.activeFound, nil
	}
	record, found, err := LoadQuestActivePlanning(ctx, r, id)
	if err != nil || !found {
		return record, found, err
	}
	var missing []int64
	seen := make(map[int64]struct{})
	for _, questID := range dependencies(record) {
		if questID <= 0 {
			continue
		}
		if _, ok := seen[questID]; ok {
			continue
		}
		seen[questID] = struct{}{}
		_, states := record.States[questID]
		_, progress := record.Progress[questID]
		if !states && !progress {
			missing = append(missing, questID)
		}
	}
	if len(missing) > 0 {
		extra, exists, err := idsReader.LoadQuestIDs(ctx, id, missing)
		if err != nil || !exists || extra.CharacterID != record.CharacterID {
			if scope != nil {
				scope.activeLoaded, scope.dependentLoaded = false, false
			}
		}
		if err != nil {
			return QuestRecord{}, false, err
		}
		if !exists || extra.CharacterID != record.CharacterID {
			return QuestRecord{}, false, ErrQuestVersionConflict
		}
		extra = CloneQuest(extra)
		if len(extra.States) > 0 {
			if record.States == nil {
				record.States = make(map[int64]QuestState)
			}
			maps.Copy(record.States, extra.States)
		}
		if len(extra.Progress) > 0 {
			if record.Progress == nil {
				record.Progress = make(map[int64]QuestState)
			}
			maps.Copy(record.Progress, extra.Progress)
		}
	}
	if cacheable && scope != nil {
		scope.dependentRecord, scope.dependentKey, scope.dependentLoaded = CloneQuest(record), key, true
	}
	return record, true, nil
}
