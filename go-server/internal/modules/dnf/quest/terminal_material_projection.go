package quest

import (
	"sort"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ProjectTerminalMaterialQuestClearProgress returns a detached activity
// snapshot in which a quest-clear parent exposes progress zero once its final
// PVF child is a no-reward material objective at active/0. The current client
// gates its parent op34 request on that parent trigger and otherwise sends no
// completion request. This projection intentionally changes no completed child,
// asset, or persisted state.
func (c *Catalog) ProjectTerminalMaterialQuestClearProgress(record dnfrepo.QuestRecord) dnfrepo.QuestRecord {
	projected := dnfrepo.CloneQuest(record)
	if c == nil {
		return projected
	}
	parentIDs := make(map[int64]struct{}, len(projected.States)+len(projected.Progress))
	for questID := range projected.States {
		parentIDs[questID] = struct{}{}
	}
	for questID := range projected.Progress {
		if _, shadowed := projected.States[questID]; !shadowed {
			parentIDs[questID] = struct{}{}
		}
	}
	ordered := make([]int64, 0, len(parentIDs))
	for questID := range parentIDs {
		ordered = append(ordered, questID)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })

	for _, parentID := range ordered {
		parent, known := c.Find(parentID)
		if !known || !questClearDefinition(parent) {
			continue
		}
		parentState, parentField, stateKnown := mutableQuestState(&projected, parentID)
		if !stateKnown || !isActiveQuestStatus(parentState.Status) || parentState.ProgressValue <= 0 {
			continue
		}

		children := make([]int64, 0, len(parent.IntData))
		for _, childID := range parent.IntData {
			if childID > 0 {
				children = append(children, childID)
			}
		}
		if len(children) == 0 {
			continue
		}
		terminalID := children[len(children)-1]
		allPriorCompleted := true
		for _, childID := range children[:len(children)-1] {
			childState, exists := questStateFor(projected, childID)
			if !exists || !isCompletedQuestStatus(childState.Status) {
				allPriorCompleted = false
				break
			}
		}
		if !allPriorCompleted {
			continue
		}

		terminal, terminalKnown := c.Find(terminalID)
		terminalState, terminalExists := questStateFor(projected, terminalID)
		if !terminalKnown || !terminalExists || terminal.MainQuestID != parentID ||
			!terminal.NoExperience || !shouldAutoCompleteNoRewardSubQuest(terminal) ||
			len(finishDefinitionSeekingRules(terminal)) == 0 ||
			!isActiveQuestStatus(terminalState.Status) || terminalState.ProgressValue != 0 {
			continue
		}

		parentState.ProgressValue = 0
		switch parentField {
		case dnfrepo.QuestFieldStates:
			projected.States[parentID] = parentState
		case dnfrepo.QuestFieldProgress:
			projected.Progress[parentID] = parentState
		}
	}
	return projected
}
