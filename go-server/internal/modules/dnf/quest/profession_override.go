package quest

import "sort"

// professionQuestStageMax is the highest awakening stage the current PVF
// family packs into the grow_type high nibble (0 = none, 1 = first awakening,
// 2 = second awakening). character/*.chr growth blocks only declare
// [awakening 1] and [awakening 2].
const professionQuestStageMax = 2

// ProfessionProgressionQuestIDs returns every PVF class-change / awakening
// quest that a character with the given job and packed grow_type must show as
// completed for its quest state to agree with its advancement.
//
// The rule is derived from the runtime Script.pvf alone:
//
//   - Only quests carrying a [job change quest] marker of 1, 2 or 3 take part.
//     Marker 1 is the class-change chain, 2 the first-awakening chain and 3 the
//     second-awakening chain. Main-story, side, event and expert-job quests are
//     never returned, not even as prerequisites of a profession quest, because
//     marking them would fabricate story progress the caller did not request.
//   - A stage contributes quests only when the target grow_type contains that
//     stage. The class-change terminal is selected by its
//     [reward type] [grow type] value; the awakening chains are selected by
//     re-evaluating the shared PVF character predicate against the grow_type
//     the character held immediately before that stage
//     ([target character] tuples carry the pre-transition stage, see
//     definitionMatchesCharacter).
//   - The set is then closed forwards over [pre required quest] inside the
//     profession marker space, so a follow-up row such as the shared
//     "转职祝贺" quest is included once its class-change terminal is.
//
// The returned slice is sorted ascending and contains no duplicates. Job and
// grow_type are not range-checked here: the caller (GM panel) already rejects
// grow values that character/*.chr cannot resolve.
func (c *Catalog) ProfessionProgressionQuestIDs(job int, growType int) []int64 {
	if c == nil || growType < 0 || growType > 0xff {
		return nil
	}
	first := growType & 0x0f
	second := (growType >> 4) & 0x0f
	if second > professionQuestStageMax {
		second = professionQuestStageMax
	}
	if first == 0 && second == 0 {
		return nil
	}

	// stageStates are the packed grow_type values the character occupied on the
	// way to the requested state. The predicate below is level independent, so
	// no level floor is applied here; the GM entry point enforces the PVF level
	// gates (15/50/75) before it writes grow_type at all.
	stageStates := make([]int, 0, professionQuestStageMax+1)
	for stage := 0; stage <= second; stage++ {
		stageStates = append(stageStates, stage<<4|first)
	}
	matchesStage := func(definition Definition) bool {
		for _, state := range stageStates {
			if definitionMatchesCharacter(definition, CharacterEligibility{Job: job, GrowType: state}) {
				return true
			}
		}
		return false
	}
	isProfessionQuest := func(definition Definition) bool {
		return definition.JobChangeQuest >= 1 && definition.JobChangeQuest <= 3
	}

	chosen := make(map[int64]struct{}, 16)
	add := func(definition Definition) bool {
		if _, exists := chosen[definition.ID]; exists {
			return false
		}
		chosen[definition.ID] = struct{}{}
		return true
	}

	if first > 0 {
		// Class change: exactly the branch terminal whose reward carries this
		// branch number. Other branches' terminals stay untouched; the PVF
		// [collision quest] lists already suppress them once this one is
		// completed.
		for _, definition := range c.ordered {
			if definition.JobChangeQuest != 1 {
				continue
			}
			if normalizeQuestTag(definition.RewardType) != "grow type" || len(definition.RewardIntData) == 0 {
				continue
			}
			if int(definition.RewardIntData[0]) != first {
				continue
			}
			if !definitionMatchesCharacter(definition, CharacterEligibility{Job: job, GrowType: first}) {
				continue
			}
			add(definition)
		}
	}
	if second >= 1 {
		// First awakening: parameterised by the class branch only.
		for _, definition := range c.ordered {
			if definition.JobChangeQuest != 2 {
				continue
			}
			if !definitionMatchesCharacter(definition, CharacterEligibility{Job: job, GrowType: first}) {
				continue
			}
			add(definition)
		}
	}
	if second >= 2 {
		// Second awakening: the PVF target tuples require awakening stage 1.
		for _, definition := range c.ordered {
			if definition.JobChangeQuest != 3 {
				continue
			}
			if !definitionMatchesCharacter(definition, CharacterEligibility{Job: job, GrowType: 1<<4 | first}) {
				continue
			}
			add(definition)
		}
	}

	// Forward closure: include profession follow-ups whose [pre required quest]
	// group is already satisfied by the selected set. Loop until stable so a
	// chain such as 6926 -> 6927 is picked up in one call.
	for changed := true; changed; {
		changed = false
		for _, definition := range c.ordered {
			if !isProfessionQuest(definition) {
				continue
			}
			if _, exists := chosen[definition.ID]; exists {
				continue
			}
			if !matchesStage(definition) {
				continue
			}
			if !prerequisiteGroupChosen(definition, chosen) {
				continue
			}
			if add(definition) {
				changed = true
			}
		}
	}

	ids := make([]int64, 0, len(chosen))
	for id := range chosen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}

// prerequisiteGroupChosen reports whether any [pre required quest] group of the
// definition is fully contained in the selected quest set. Groups are OR-ed,
// each group is AND-ed.
func prerequisiteGroupChosen(definition Definition, chosen map[int64]struct{}) bool {
	for _, group := range definition.PreRequiredGroups {
		matched := false
		for _, required := range group {
			if required <= 0 {
				continue
			}
			if _, exists := chosen[required]; !exists {
				matched = false
				break
			}
			matched = true
		}
		if matched {
			return true
		}
	}
	return false
}
