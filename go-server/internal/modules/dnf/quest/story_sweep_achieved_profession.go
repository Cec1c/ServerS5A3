package quest

import "longheng.io/server/internal/modules/dnf/profession"

// A reset may retain awakening but clear its story row. The handbook may
// acknowledge that already-achieved reward while following the real story
// prerequisites. It must never perform a profession transition or choose a
// branch. Level-up tickets keep their existing ordinary-quest-only policy.
func storySweepAchievedAwakening(d Definition, ch CharacterEligibility) bool {
	if normalizeQuestTag(d.Grade) != "epic" || normalizeQuestTag(d.Type) == "question" ||
		d.IsEvent || d.HasCreatureRequirement || d.HasExpertRequirement ||
		len(d.RewardSelectionItems) != 0 || d.RewardParseError != "" ||
		ch.GrowType < 0 || ch.GrowType > 0xff || !definitionMatchesCharacter(d, ch) {
		return false
	}
	first, stage := profession.Decode(byte(ch.GrowType))
	if first == 0 || stage == 0 || stage > professionQuestStageMax ||
		!d.HasGrowType || d.GrowType != int(first) {
		return false
	}
	request, err := profession.ParseReward(d.JobChangeQuest, d.RewardType, d.RewardIntData)
	return err == nil && request.Kind == profession.KindAwakening &&
		d.JobChangeQuest == int(request.GrowNumber)+1 && request.GrowNumber <= stage
}
