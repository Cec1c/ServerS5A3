package quest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrPVPTrainingRewardNotReady = errors.New("道场秘籍尚不满足领取条件")

const pvpTrainingRewardLedger = "pvp_training_sub_reward_2674"

// 现用PVF末步采用空[reward int data]的[tp]类型；本机任务窗口实测显示2TP。
// 仅绑定已核对的2674与三项/秘籍配置，不能把默认数量套给其它TP任务。
func pvpTrainingTPReward(d Definition) (int, bool) {
	valid := d.ID == 2674 && d.Path == "n_quest/pvp/pvp_tookuk_05.qst" && normalizeQuestTag(d.Type) == "custom multi condition" &&
		normalizeQuestTag(d.RewardType) == "tp" && len(d.RewardIntData) == 0 && len(d.IntData) == 4 &&
		d.IntData[0] == 1 && d.IntData[1] == 1 && d.IntData[2] == 1 && d.IntData[3] == 1 && len(d.SubReward) == 1 && d.SubReward[0] == 10158452
	return 2, valid
}

func applyPVPTrainingTPReward(character *repo.CharacterRecord, skill *repo.SkillRecord, inventory repo.InventoryRecord, d Definition, bonus int) error {
	want, valid := pvpTrainingTPReward(d)
	if !valid || bonus != want || character.Stats["pvp_training_tp_2674"] != 0 {
		return ErrQuestRewardMalformed
	}
	book := false
	for key, stack := range inventory.Slots {
		slot, main := parseFinishMainInventorySlotKey(key)
		if main && !repo.IsAccountSharedInventorySlot(repo.MainInventoryListType, int16(slot)) && stack.ItemID == d.SubReward[0] && stack.Count > 0 {
			book = true
			break
		}
	}
	if !book {
		return ErrFinishRequiredItemsMissing
	}
	if character.Stats["bonus_tp"] < 0 || character.Stats["bonus_tp"] > math.MaxUint16-int64(bonus) {
		return ErrQuestRewardMalformed
	}
	add := func(points *repo.SkillPointState) error {
		if points.TotalTP < 0 || points.RemainingTP < 0 || points.RemainingTP > points.TotalTP || points.TotalTP > math.MaxUint16-bonus {
			return ErrQuestRewardMalformed
		}
		points.TotalTP += bonus
		points.RemainingTP += bonus
		return nil
	}
	if err := add(&skill.Points); err != nil {
		return err
	}
	if skill.Trees.Expanded {
		if err := add(&skill.Trees.Other.Points); err != nil {
			return err
		}
	}
	character.Stats["bonus_tp"] += int64(bonus)
	character.Stats["pvp_training_tp_2674"] = int64(bonus)
	return nil
}

type PVPTrainingRewardResult struct {
	ItemID    int64
	Granted   bool
	Inventory repo.InventoryRecord
}

// 当前客户端12EAE00在2674第三条件完成、背包无秘籍时发送空1267。
// 此处只发PVF的[sub reward]，不代写第三条件、不提交任务、不发最终TP。
func (o *Owner) ApplyPVPTrainingReward(ctx context.Context, catalog *Catalog, accountID, characterID string, now time.Time, allocate FinishItemAllocator) (PVPTrainingRewardResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil || allocate == nil || accountID == "" || characterID == "" || now.IsZero() {
		return PVPTrainingRewardResult{}, ErrOwnerUnavailable
	}
	d, ok := catalog.Find(2674)
	if !ok || d.Path != "n_quest/pvp/pvp_tookuk_05.qst" || normalizeQuestTag(d.Type) != "custom multi condition" ||
		len(d.IntData) != 4 || d.IntData[0] != 1 || d.IntData[1] != 1 || d.IntData[2] != 1 || d.IntData[3] != 1 ||
		len(d.SubReward) != 1 || d.SubReward[0] <= 0 || d.SubReward[0] > math.MaxUint32 {
		return PVPTrainingRewardResult{}, ErrPVPTrainingRewardNotReady
	}
	var result PVPTrainingRewardResult
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		if tx.Character == nil || tx.Quest == nil || tx.Inventory == nil {
			return ErrOwnerUnavailable
		}
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return ErrCharacterNotFound
		}
		quests, found, err := tx.Quest.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || quests.CharacterID != characterID {
			return ErrPVPTrainingRewardNotReady
		}
		quests = repo.CloneQuest(quests)
		state, field, known := mutableQuestState(&quests, d.ID)
		if !known || !isActiveQuestStatus(state.Status) || state.Extra["pvf_path"] != d.Path || (state.ProgressValue != 1<<18 && state.ProgressValue != 0) {
			return ErrPVPTrainingRewardNotReady
		}
		inventory, found, err := tx.Inventory.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			inventory = repo.InventoryRecord{CharacterID: characterID}
		}
		if inventory.CharacterID != characterID {
			return ErrOwnerUnavailable
		}
		inventory = repo.CloneInventory(inventory)
		itemID := d.SubReward[0]
		for key, stack := range inventory.Slots {
			slot, main := parseFinishMainInventorySlotKey(key)
			if main && !repo.IsAccountSharedInventorySlot(repo.MainInventoryListType, int16(slot)) && stack.ItemID == itemID && stack.Count > 0 {
				result = PVPTrainingRewardResult{ItemID: itemID, Inventory: inventory}
				return nil
			}
		}
		// 角色级账本跨放弃/重接保留；物品转出或丢弃后不能靠重放领取刷物品。
		if character.Stats[pvpTrainingRewardLedger] != 0 || state.Extra["pvp_training_sub_reward"] != "" || state.ProgressValue != 1<<18 {
			return ErrPVPTrainingRewardNotReady
		}
		if err := repo.BindInventoryContainerState(ctx, o.repositories.Settings, &inventory); err != nil {
			return err
		}
		items, err := allocateFinishRewardItems(&inventory, []RewardItemRule{{ItemID: itemID, Count: 1}}, allocate, FinishItemGrantRequest{
			QuestID: d.ID, CompletionKey: fmt.Sprintf("pvp-training-sub-reward/%s/%d", characterID, d.ID), Source: "quest_pvf:" + d.Path + "[sub reward]",
		})
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].ItemID != itemID || items[0].Delta != 1 {
			return ErrQuestRewardMalformed
		}
		inventory.UpdatedAt = now
		if state.Extra == nil {
			state.Extra = map[string]string{}
		}
		state.Extra["pvp_training_sub_reward"] = "granted"
		state.UpdatedAt = now
		if field == repo.QuestFieldStates {
			quests.States[d.ID] = state
		} else {
			quests.Progress[d.ID] = state
		}
		quests.UpdatedAt = now
		character = repo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = map[string]int64{}
		}
		character.Stats[pvpTrainingRewardLedger] = itemID
		if err := repo.SaveInventoryFields(ctx, tx.Inventory, inventory, repo.InventoryFieldSlots); err != nil {
			return err
		}
		if err := repo.SaveQuestFields(ctx, tx.Quest, quests, field); err != nil {
			return err
		}
		if err := repo.SaveCharacterFields(ctx, tx.Character, character, repo.CharacterFieldStats); err != nil {
			return err
		}
		result = PVPTrainingRewardResult{ItemID: itemID, Granted: true, Inventory: repo.CloneInventory(inventory)}
		return nil
	})
	if err != nil {
		return PVPTrainingRewardResult{}, err
	}
	return result, nil
}
