package quest

import (
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// Named coupons authorize only their PVF list. Broad coupons retain character,
// level, prerequisite, collision and player-choice gates. They never grant
// gold/experience/material rewards or silently change a profession.
func (c *Catalog) applySelectedCouponQuests(result *UseClearCouponResult, inv *repo.InventoryRecord, ch CharacterEligibility, in UseClearCouponInput) ([]repo.QuestField, error) {
	selected := map[int64]bool{}
	for _, id := range in.Coupon.QuestIDs {
		if _, ok := c.byID[id]; !ok || id <= 0 {
			return nil, fmt.Errorf("%w: coupon quest %d", ErrQuestDefinitionMissing, id)
		}
		selected[id] = true
	}
	if in.Coupon.Kind == CouponNamedQuests && len(selected) == 0 {
		return nil, ErrCouponClearUnavailable
	}
	if result.Quests.States == nil {
		result.Quests.States = map[int64]repo.QuestState{}
	}
	if result.Character.Stats == nil {
		result.Character.Stats = map[string]int64{}
	}
	completed, active, damaged := c.storySweepEvidence(result.Quests)
	result.DamagedQuestionIDs = damaged
	var fields []repo.QuestField
	for pass := 0; pass <= len(c.ordered); pass++ {
		changed := false
		for _, d := range c.ordered {
			if !selectedCouponDefinition(d, ch, in.Coupon, selected) {
				continue
			}
			state, field, known := mutableQuestState(&result.Quests, d.ID)
			done := known && isCompletedQuestStatus(state.Status)
			if known && !done && !isActiveQuestStatus(state.Status) {
				continue
			}
			if !done && !c.storySweepDependencies(d, result.Quests, completed, active) {
				continue
			}
			reward := normalizeQuestTag(d.RewardType)
			if reward == "slot expansion" {
				if len(d.RewardIntData) != 1 || d.RewardIntData[0] < 0 || d.RewardIntData[0] > 2 {
					return nil, ErrQuestRewardMalformed
				}
				bit, ok := ExEquipSlotBitForPVFIndex(uint32(d.RewardIntData[0]))
				if !ok {
					return nil, ErrQuestRewardMalformed
				}
				before := result.Character.Stats["ex_equip_slot_stat"]
				result.Character.Stats["ex_equip_slot_stat"] = before | int64(bit)
				result.SlotsExpanded = result.SlotsExpanded || before != result.Character.Stats["ex_equip_slot_stat"]
			}
			if done {
				continue
			}
			if reward == "title" {
				if len(d.RewardIntData) != 2 || d.RewardIntData[0] <= 0 || d.RewardIntData[1] != 1 || in.AllocateItem == nil {
					return nil, ErrQuestRewardMalformed
				}
				items, err := in.AllocateItem(inv, FinishItemGrantRequest{QuestID: d.ID, CompletionKey: fmt.Sprintf("coupon:%s:%d", in.CharacterID, d.ID), Source: "quest_coupon_title", ItemID: d.RewardIntData[0], Count: 1})
				if err != nil {
					return nil, err
				}
				result.Items = append(result.Items, items...)
			}
			if !known {
				field = repo.QuestFieldStates
			}
			state = couponMainQuestClearState(state, d, ch.Level, in.Now)
			state.Extra["auto_complete_reason"] = in.Coupon.Kind + "_coupon"
			if field == repo.QuestFieldProgress {
				result.Quests.Progress[d.ID] = state
			} else {
				result.Quests.States[d.ID] = state
			}
			fields = mergeQuestChangedField(fields, field)
			result.ChangedQuestIDs = append(result.ChangedQuestIDs, d.ID)
			completed[d.ID] = struct{}{}
			delete(active, d.ID)
			changed = true
		}
		if !changed {
			break
		}
	}
	return fields, nil
}

func selectedCouponDefinition(d Definition, ch CharacterEligibility, coupon ClearCoupon, ids map[int64]bool) bool {
	if d.LevelMin > ch.Level || !definitionMatchesCharacter(d, ch) || d.JobChangeQuest != 0 || normalizeQuestTag(d.Type) == "question" || d.DisableQuestClear {
		return false
	}
	reward := normalizeQuestTag(d.RewardType)
	switch coupon.Kind {
	case CouponNamedQuests:
		if !ids[d.ID] {
			return false
		}
		switch reward {
		case "", "item", "title", "slot expansion", "hell challenge", "inferno challenge":
			return true
		}
		return false
	case CouponAchievement:
		if normalizeQuestTag(d.Grade) != "achievement" || d.IsEvent || d.HasCreatureRequirement || d.HasExpertRequirement {
			return false
		}
		// Native ticket descriptions explicitly preserve these special unlock and
		// equipment chains. Their name prefixes are shared by all job variants.
		for _, name := range []string{"向阿尔伯特学技能", "死神的气息", "穿戴辅助装备", "佩戴魔法石", "梅尔文的高科技", "班图族的角质项链", "深海项链", "狙击镜的威力", "不灭之王波罗丁", "龙跃武器", "铁鳞海贼团", "开启远古地下城之门", "镇魂曲", "异界初体验", "勇者", "异次元裂缝", "深渊派对", "盾牌", "贪食之巨兽"} {
			if strings.Contains(d.Name, name) {
				return false
			}
		}
		return reward == "" || reward == "item" || reward == "title"
	case CouponSideQuests:
		return normalizeQuestTag(d.Grade) == "side" && !d.IsEvent && !d.HasCreatureRequirement && !d.HasExpertRequirement && (reward == "" || reward == "item" || reward == "title")
	}
	return false
}
