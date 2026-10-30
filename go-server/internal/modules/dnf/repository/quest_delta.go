package repository

import (
	"context"
	"errors"
	"maps"
	"sort"
	"strings"
)

var ErrQuestVersionConflict = errors.New("quest changed since planning")
var ErrQuestPartialSave = errors.New("partial quest planning record requires delta writer")

// QuestDeltaWriter compares each affected quest with before in the same
// transaction that writes after. Unchanged quests are never replaced.
type QuestDeltaWriter interface {
	SaveQuestChanges(context.Context, QuestRecord, QuestRecord, ...QuestField) error
}

type QuestIDReader interface {
	LoadQuestIDs(context.Context, string, []int64) (QuestRecord, bool, error)
}

// QuestPlanningReader retains every state header for linked-quest dependencies
// and duplicate precedence, but loads Extra only for active quests. The partial
// history must only be saved through a delta writer, never SaveFields.
type QuestPlanningReader interface {
	LoadQuestPlanning(context.Context, string) (QuestRecord, bool, error)
}

func LoadQuestPlanning(ctx context.Context, r QuestRepository, id string) (QuestRecord, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return QuestRecord{}, false, ctx.Err()
	}
	scope := planningScope(ctx, r, id)
	if scope != nil && scope.loaded {
		return CloneQuest(scope.record), scope.found, nil
	}
	record, found, err := loadQuestPlanningUncached(ctx, r, id)
	if scope != nil && err == nil {
		scope.record, scope.found, scope.loaded = CloneQuest(record), found, true
	}
	return record, found, err
}

func loadQuestPlanningUncached(ctx context.Context, r QuestRepository, id string) (QuestRecord, bool, error) {
	_, delta := r.(QuestDeltaWriter)
	if reader, ok := r.(QuestPlanningReader); ok && delta {
		return reader.LoadQuestPlanning(ctx, id)
	}
	return r.Load(ctx, id)
}

func QuestStatusActive(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	status = strings.NewReplacer("_", "", "-", "", " ", "").Replace(status)
	switch status {
	case "active", "accepted", "inprogress", "progress":
		return true
	}
	return false
}

func SaveQuestChanges(ctx context.Context, r QuestRepository, before, after QuestRecord, fields ...QuestField) error {
	// Even a failed write can mean the before-image became stale. The next
	// stage must reload. Success also reloads because unrelated quests can
	// change concurrently without violating this delta's preconditions.
	scope := planningScope(ctx, r, before.CharacterID)
	if scope != nil {
		scope.loaded = false
		scope.activeLoaded = false
		scope.dependentLoaded = false
	}
	var err error
	if writer, ok := r.(QuestDeltaWriter); ok {
		err = writer.SaveQuestChanges(ctx, before, after, fields...)
	} else {
		err = SaveQuestFields(ctx, r, after, fields...)
	}
	return err
}

func LoadQuestIDs(ctx context.Context, r QuestRepository, id string, ids []int64) (QuestRecord, bool, error) {
	if reader, ok := r.(QuestIDReader); ok {
		return reader.LoadQuestIDs(ctx, id, ids)
	}
	return r.Load(ctx, id)
}

func QuestStatesEqual(a, b QuestState) bool {
	return a.Status == b.Status && a.TriggerType == b.TriggerType && a.ProgressValue == b.ProgressValue &&
		a.RewardSelectIndex == b.RewardSelectIndex && a.Multiplier == b.Multiplier && a.UpdatedAt.Equal(b.UpdatedAt) && maps.Equal(a.Extra, b.Extra)
}

// ChangedQuestIDs includes additions and removals in stable order.
func ChangedQuestIDs(before, after map[int64]QuestState) []int64 {
	ids := make([]int64, 0)
	for id, old := range before {
		next, ok := after[id]
		if !ok || !QuestStatesEqual(old, next) {
			ids = append(ids, id)
		}
	}
	for id := range after {
		if _, ok := before[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
