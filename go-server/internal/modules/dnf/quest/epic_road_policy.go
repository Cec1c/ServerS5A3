package quest

import "fmt"

// WithEpicRoadEvent350Eligibility is an explicit server operating policy for
// the two real prerequisites of the enabled350 activity. It never changes
// catalog definitions, repository state, resource dates or other event quests.
func (c *Catalog) WithEpicRoadEvent350Eligibility(character CharacterEligibility, enabled bool) (CharacterEligibility, error) {
	character.epicRoadEvent350Enabled = false
	character.epicRoadEvent350Recognized = false
	invalid := func(id int64) (CharacterEligibility, error) {
		if !enabled {
			return character, nil
		}
		return character, fmt.Errorf("epic350 prerequisite resource mismatch: %d", id)
	}
	for i, id := range []int64{5578, 5579} {
		d, ok := c.Find(id)
		if !ok || d.Path != fmt.Sprintf("n_quest/event/18_epciroad2_%d.qst", i+1) || !d.IsEvent || d.ShowQuestOnEvent != 350 || d.Grade != "[special]" || d.Type != "[meet npc]" || d.LevelMin != 17 || d.LevelMax != 99 || d.ExposedByNPC != 1 || len(d.IntData) != 1 || d.IntData[0] != 3135 || d.HasDependGiveItem || d.HasCreatureRequirement || d.HasExpertRequirement {
			return invalid(id)
		}
		if i == 0 && len(d.PreRequiredGroups) != 0 || i == 1 && (len(d.PreRequiredGroups) != 1 || len(d.PreRequiredGroups[0]) != 1 || d.PreRequiredGroups[0][0] != 5578) {
			return invalid(id)
		}
	}
	character.epicRoadEvent350Recognized = true
	character.epicRoadEvent350Enabled = enabled
	return character, nil
}
