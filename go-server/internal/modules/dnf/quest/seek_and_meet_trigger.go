package quest

import dnfrepo "longheng.io/server/internal/modules/dnf/repository"

// Old accepts put the first item quantity into a boolean material channel.
// Only compress an active server-created counter compatible with that shape.
// Zero/one already represent meaningful progress; the NPC channel is retained.
func repairLegacySeekAndMeetTrigger(state dnfrepo.QuestState, definition Definition) (int64, bool) {
	if normalizeQuestTag(definition.Type) != "seek n meet npc" ||
		state.Extra["pvf_path"] != definition.Path || definition.Path == "" ||
		normalizeQuestTag(state.Extra["quest_type"]) != "seek n meet npc" {
		return state.ProgressValue, false
	}
	values := definition.IntData
	if len(values) < 3 || len(values)%2 != 1 || values[len(values)-1] <= 0 {
		return state.ProgressValue, false
	}
	for i := 0; i+1 < len(values); i += 2 {
		if values[i] <= 0 || values[i+1] <= 0 {
			return state.ProgressValue, false
		}
	}
	if state.ProgressValue < 0 || state.ProgressValue > int64(^uint32(0)) {
		return state.ProgressValue, false
	}
	progress := uint32(state.ProgressValue)
	remaining := progress & 0x1ff
	if remaining <= 1 || remaining > boundedTriggerChannel(values[1]) || progress>>9 > 1 {
		return state.ProgressValue, false
	}
	return int64((progress &^ 0x1ff) | 1), true
}
