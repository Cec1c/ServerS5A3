package dnfbridge

import (
	"context"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentActiveQuestSnapshotNeedsTerminalProjection(record dnfrepo.QuestRecord) bool {
	for questID, state := range record.States {
		if questID > 0 && currentTerminalMaterialParentCandidate(state) {
			return true
		}
	}
	for questID, state := range record.Progress {
		if _, shadowed := record.States[questID]; shadowed {
			continue
		}
		if questID > 0 && currentTerminalMaterialParentCandidate(state) {
			return true
		}
	}
	return false
}

// Accepted quest records retain their PVF type. Ordinary active objectives
// must not load the catalog or copy the quest history for this projection.
func currentTerminalMaterialParentCandidate(state dnfrepo.QuestState) bool {
	if state.ProgressValue <= 0 || !questStatusActive(state.Status) {
		return false
	}
	switch normalizeCurrentQuestType(state.Extra["quest_type"]) {
	case "quest clear", "clear quest":
		return true
	default:
		return false
	}
}

func questStatusActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "accepted", "inprogress", "progress":
		return true
	default:
		return false
	}
}

func (s *Service) projectCurrentActiveQuestSnapshot(
	ctx context.Context,
	session *gameSession,
	record dnfrepo.QuestRecord,
	source string,
) dnfrepo.QuestRecord {
	if s == nil || !currentActiveQuestSnapshotNeedsTerminalProjection(record) {
		return record
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		s.logGameEvent(session, "game-upper-current-active-quest-terminal-material-projection-skipped",
			"char_id", session.selectedCharacterID,
			"source", source,
			"reason", "quest_catalog_unavailable",
			"error", err)
		return record
	}
	projected := catalog.ProjectTerminalMaterialQuestClearProgress(record)
	beforeRows, afterRows := currentSelectAckQuestRows(record, true), currentSelectAckQuestRows(projected, true)
	if !sameCurrentActiveQuestRows(beforeRows, afterRows) {
		s.logGameEvent(session, "game-upper-current-active-quest-terminal-material-projected",
			"char_id", session.selectedCharacterID,
			"source", source,
			"reason", "terminal_material_child_active_zero")
	}
	return projected
}

func sameCurrentActiveQuestRows(left, right []currentSelectAckQuestRow) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
