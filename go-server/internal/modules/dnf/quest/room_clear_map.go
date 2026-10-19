package quest

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrRoomClearMapDungeonRequired        = errors.New("dnf room clear-map dungeon id is required")
	ErrRoomClearMapMapRequired            = errors.New("dnf room clear-map map id is required")
	ErrRoomClearMapCompletionKeyRequired  = errors.New("dnf room clear-map completion key is required")
	ErrRoomClearMapCompletionTimeRequired = errors.New("dnf room clear-map completion time is required")
)

const roomClearMapCompletionSource = "room_clear"

// RoomClearMapInput is one authoritative middle-room clear fact. LinkedQuestIDs
// must come from the selected maze binding; DungeonID is retained as event
// provenance and is never used to match the clear-map target.
type RoomClearMapInput struct {
	DungeonID      int64
	MapID          int64
	LinkedQuestIDs []int64
	CompletionKey  string
	CompletedAt    time.Time
}

type RoomClearMapParentProgress struct {
	QuestID         int64
	Field           dnfrepo.QuestField
	PreviousTrigger int64
	ProgressValue   int64
}

// RoomClearMapCompletionPlan is repository-independent. Record is a deep clone;
// only the requested maze-linked child and its active quest-clear parent may
// change.
type RoomClearMapCompletionPlan struct {
	Record         dnfrepo.QuestRecord
	Completions    []ClearMapCompletion
	ParentProgress []RoomClearMapParentProgress
	ChangedFields  []dnfrepo.QuestField
}

// PlanRoomClearMapCompletion archives an active, maze-linked, no-reward
// clear-map child and recomputes its active quest-clear parent. It never
// activates missing work, matches a dungeon id as a map, grants rewards, or
// marks the dungeon complete.
func (c *Catalog) PlanRoomClearMapCompletion(
	record dnfrepo.QuestRecord,
	input RoomClearMapInput,
) (RoomClearMapCompletionPlan, error) {
	if c == nil {
		return RoomClearMapCompletionPlan{}, ErrCatalogEmpty
	}
	if input.DungeonID <= 0 {
		return RoomClearMapCompletionPlan{}, ErrRoomClearMapDungeonRequired
	}
	if input.MapID <= 0 {
		return RoomClearMapCompletionPlan{}, ErrRoomClearMapMapRequired
	}
	input.CompletionKey = strings.TrimSpace(input.CompletionKey)
	if input.CompletionKey == "" {
		return RoomClearMapCompletionPlan{}, ErrRoomClearMapCompletionKeyRequired
	}
	if input.CompletedAt.IsZero() {
		return RoomClearMapCompletionPlan{}, ErrRoomClearMapCompletionTimeRequired
	}

	plan := RoomClearMapCompletionPlan{Record: dnfrepo.CloneQuest(record)}
	linkedQuestIDs := normalizedRoomClearMapQuestIDs(input.LinkedQuestIDs)
	affectedParents := make(map[int64]dnfrepo.QuestField)

	for _, questID := range linkedQuestIDs {
		definition, known := c.Find(questID)
		if !known || !roomClearMapChildMatches(definition, input.MapID) {
			continue
		}
		state, field, exists := mutableQuestState(&plan.Record, questID)
		if !exists {
			continue
		}
		completion := ClearMapCompletion{
			QuestID:         questID,
			Field:           field,
			PreviousTrigger: state.ProgressValue,
			PVFPath:         definition.Path,
		}
		if isCompletedQuestStatus(state.Status) {
			completion.Idempotent = true
			plan.Completions = append(plan.Completions, completion)
			continue
		}
		if !isActiveQuestStatus(state.Status) || state.ProgressValue < 0 {
			continue
		}

		parent, _, parentField, validParent := c.activeRoomClearMapParent(&plan.Record, definition)
		if !validParent {
			continue
		}

		state.Status = "completed"
		state.ProgressValue = 0
		state.UpdatedAt = input.CompletedAt
		if state.Extra == nil {
			state.Extra = make(map[string]string, 10)
		}
		state.Extra["completion_kind"] = clearMapCompletionKind
		state.Extra["completion_source"] = roomClearMapCompletionSource
		state.Extra["completion_key"] = input.CompletionKey
		state.Extra["completion_dungeon_id"] = strconv.FormatInt(input.DungeonID, 10)
		state.Extra["completion_map_id"] = strconv.FormatInt(input.MapID, 10)
		state.Extra["linked_objective_completion_source"] = roomClearMapCompletionSource
		state.Extra["pvf_path"] = definition.Path
		state.Extra["quest_type"] = definition.Type
		autoCompleteNoRewardSubQuestState(&state, definition, input.CompletedAt)
		switch field {
		case dnfrepo.QuestFieldStates:
			plan.Record.States[questID] = state
		case dnfrepo.QuestFieldProgress:
			plan.Record.Progress[questID] = state
		default:
			continue
		}
		plan.ChangedFields = mergeQuestChangedField(plan.ChangedFields, field)
		plan.Completions = append(plan.Completions, completion)
		affectedParents[parent.ID] = parentField
	}

	if len(plan.ChangedFields) > 0 {
		completed, _ := questStateSets(plan.Record)
		parentIDs := make([]int64, 0, len(affectedParents))
		for parentID := range affectedParents {
			parentIDs = append(parentIDs, parentID)
		}
		sort.Slice(parentIDs, func(i, j int) bool { return parentIDs[i] < parentIDs[j] })
		for _, parentID := range parentIDs {
			parent, known := c.Find(parentID)
			if !known || !questClearDefinition(parent) {
				continue
			}
			state, field, exists := mutableQuestState(&plan.Record, parentID)
			if !exists || !isActiveQuestStatus(state.Status) {
				continue
			}
			previous := state.ProgressValue
			next := questClearParentTrigger(parent.IntData, completed)
			plan.ParentProgress = append(plan.ParentProgress, RoomClearMapParentProgress{
				QuestID:         parentID,
				Field:           field,
				PreviousTrigger: previous,
				ProgressValue:   next,
			})
			if previous == next {
				continue
			}
			state.ProgressValue = next
			state.UpdatedAt = input.CompletedAt
			switch field {
			case dnfrepo.QuestFieldStates:
				plan.Record.States[parentID] = state
			case dnfrepo.QuestFieldProgress:
				plan.Record.Progress[parentID] = state
			default:
				continue
			}
			plan.ChangedFields = mergeQuestChangedField(plan.ChangedFields, field)
		}
	}

	if len(plan.ChangedFields) > 0 {
		plan.Record.UpdatedAt = input.CompletedAt
	}
	return plan, nil
}

func (c *Catalog) activeRoomClearMapParent(
	record *dnfrepo.QuestRecord,
	child Definition,
) (Definition, dnfrepo.QuestState, dnfrepo.QuestField, bool) {
	if c == nil || record == nil || child.MainQuestID <= 0 {
		return Definition{}, dnfrepo.QuestState{}, "", false
	}
	parent, known := c.Find(child.MainQuestID)
	if !known || !questClearDefinition(parent) || !roomClearMapIntContains(parent.IntData, child.ID) {
		return Definition{}, dnfrepo.QuestState{}, "", false
	}
	state, field, exists := mutableQuestState(record, parent.ID)
	if !exists || !isActiveQuestStatus(state.Status) {
		return Definition{}, dnfrepo.QuestState{}, "", false
	}
	return parent, state, field, true
}

func roomClearMapChildMatches(definition Definition, mapID int64) bool {
	return mapID > 0 &&
		normalizeQuestTag(definition.Type) == "clear map" &&
		linkedObjectiveQuest(definition) &&
		roomClearMapIntContains(definition.IntData, mapID)
}

func roomClearMapIntContains(values []int64, target int64) bool {
	if target <= 0 {
		return false
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizedRoomClearMapQuestIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	ids := make([]int64, 0, len(values))
	for _, questID := range values {
		if questID <= 0 {
			continue
		}
		if _, duplicate := seen[questID]; duplicate {
			continue
		}
		seen[questID] = struct{}{}
		ids = append(ids, questID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
