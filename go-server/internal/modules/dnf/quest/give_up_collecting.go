package quest

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// CollectingGiveUpResult is returned only after the quest and reclaimed items
// commit together. It never represents a quest reward or completion.
type CollectingGiveUpResult struct {
	GiveUpResult
	Reclaimed []FinishConsumedItem
	Inventory dnfrepo.InventoryRecord
}

// ApplyCollectingGiveUp admits understood seeking quests, including quest-drop
// items. Ordinary materials stay in the bag. Quest items retain the maximum
// requirement of any other active quest, matching the existing recovery policy.
// Activation grants and unsupported quest shapes remain on the closed path.
func (o *Owner) ApplyCollectingGiveUp(ctx context.Context, catalog *Catalog, cmd Command, itemType func(int64) (string, error)) (CollectingGiveUpResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || itemType == nil {
		return CollectingGiveUpResult{}, ErrGiveUpNeedsAssets
	}
	if cmd.SelectedCharacterID == 0 {
		return CollectingGiveUpResult{}, ErrCharacterRequired
	}
	if cmd.QuestID == 0 {
		return CollectingGiveUpResult{}, ErrQuestIDRequired
	}
	if catalog == nil {
		return CollectingGiveUpResult{}, ErrQuestDefinitionMissing
	}
	def, ok := catalog.Find(int64(cmd.QuestID))
	if !ok {
		return CollectingGiveUpResult{}, ErrQuestDefinitionMissing
	}
	if def.CantGiveUp {
		return CollectingGiveUpResult{}, ErrQuestCannotGiveUp
	}
	if def.HasDependGiveItem || normalizeQuestTag(def.Type) != "seeking" {
		return CollectingGiveUpResult{}, ErrGiveUpNeedsAssets
	}
	required, err := giveUpStrictPairs(def.IntData)
	if err != nil || len(required) == 0 {
		return CollectingGiveUpResult{}, ErrGiveUpNeedsAssets
	}
	// Do not silently ignore a distinct drop which is not represented by the
	// understood collection requirement.
	for _, drop := range def.MonsterRewardItems {
		if _, ok := required[drop.ItemID]; !ok {
			return CollectingGiveUpResult{}, ErrGiveUpNeedsAssets
		}
	}
	for _, drop := range def.EnemyRewardItems {
		if _, ok := required[drop.ItemID]; !ok {
			return CollectingGiveUpResult{}, ErrGiveUpNeedsAssets
		}
	}
	reclaim := map[int64]int64{}
	for id := range required {
		kind, err := itemType(id)
		if err != nil {
			return CollectingGiveUpResult{}, err
		}
		switch strings.TrimSpace(kind) {
		case "[quest]":
			reclaim[id] = 0
		case "[material]":
		default:
			return CollectingGiveUpResult{}, ErrGiveUpNeedsAssets
		}
	}
	characterID := strconv.Itoa(int(cmd.SelectedCharacterID))
	var result CollectingGiveUpResult
	err = o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil || tx.Inventory == nil {
			return ErrGiveUpNeedsAssets
		}
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != strings.TrimSpace(cmd.AccountID) {
			return ErrCharacterNotFound
		}
		quests, found, err := tx.Quest.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || quests.CharacterID != characterID {
			return ErrQuestNotActive
		}
		state, found := questStateFor(quests, int64(cmd.QuestID))
		if !found || !isActiveQuestStatus(state.Status) {
			return ErrQuestNotActive
		}
		quests = dnfrepo.CloneQuest(quests)
		active := map[int64]bool{}
		for id, state := range quests.States {
			if isActiveQuestStatus(state.Status) {
				active[id] = true
			}
		}
		for id, state := range quests.Progress {
			if isActiveQuestStatus(state.Status) {
				active[id] = true
			}
		}
		for id := range active {
			if id == int64(cmd.QuestID) || len(reclaim) == 0 {
				continue
			}
			other, known := catalog.Find(id)
			if !known {
				return ErrQuestDefinitionMissing
			}
			// Unknown active definitions must not cause shared items to be lost.
			values := []int64(nil)
			switch normalizeQuestTag(other.Type) {
			case "seeking":
				values = other.IntData
			case "seek n meet npc":
				values = other.IntData[:len(other.IntData)/2*2]
			case "get item":
				return ErrGiveUpNeedsAssets
			}
			needs, err := giveUpStrictPairs(values)
			if err != nil {
				return err
			}
			grants, err := giveUpStrictPairs(other.DependGiveItemData)
			if err != nil {
				return err
			}
			if other.HasDependGiveItem && len(grants) == 0 {
				return ErrGiveUpNeedsAssets
			}
			for item := range reclaim {
				reclaim[item] = max(reclaim[item], needs[item], grants[item])
			}
		}
		inventory, found, err := tx.Inventory.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if found && inventory.CharacterID != characterID {
			return ErrGiveUpNeedsAssets
		}
		inventory = dnfrepo.CloneInventory(inventory)
		inventory.CharacterID = characterID
		held := map[int64]int64{}
		for key, stack := range inventory.Slots {
			if _, relevant := reclaim[stack.ItemID]; !relevant {
				continue
			}
			slot, valid := parseFinishMainInventorySlotKey(key)
			// Quest materials are character-local main-bag assets. Do not
			// reinterpret a corrupt/shared/warehouse slot as that inventory.
			if !valid || slot > math.MaxInt16 || finishSlotIsAccountShared(slot) {
				return ErrGiveUpNeedsAssets
			}
			count := finishRequiredItemAvailableCount(stack, false)
			if count < 0 || count > math.MaxUint32 || held[stack.ItemID] > math.MaxUint32-count {
				return ErrGiveUpNeedsAssets
			}
			held[stack.ItemID] += count
		}
		var remove []RewardItemRule
		for id, count := range held {
			if count > reclaim[id] {
				remove = append(remove, RewardItemRule{ItemID: id, Count: count - reclaim[id]})
			}
		}
		reclaimed, err := consumeFinishRequiredItems(&inventory, &dnfrepo.AccountInventoryRecord{}, remove)
		if err != nil {
			return err
		}
		if len(reclaimed) > math.MaxUint8 {
			return ErrGiveUpNeedsAssets
		}
		delete(quests.States, int64(cmd.QuestID))
		delete(quests.Progress, int64(cmd.QuestID))
		quests.UpdatedAt = time.Now()
		if len(reclaimed) != 0 {
			if err := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if err := dnfrepo.SaveQuestFields(ctx, tx.Quest, quests, dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress); err != nil {
			return err
		}
		result = CollectingGiveUpResult{GiveUpResult: GiveUpResult{AccountID: character.AccountID, CharacterID: characterID, QuestID: cmd.QuestID}, Reclaimed: reclaimed, Inventory: inventory}
		return nil
	})
	if err != nil {
		return CollectingGiveUpResult{}, err
	}
	return result, nil
}

func giveUpStrictPairs(values []int64) (map[int64]int64, error) {
	if len(values)%2 != 0 {
		return nil, ErrGiveUpNeedsAssets
	}
	out := map[int64]int64{}
	for i := 0; i < len(values); i += 2 {
		id, count := values[i], values[i+1]
		if id <= 0 || id > math.MaxUint32 || count <= 0 || count > math.MaxUint32 || out[id] > math.MaxUint32-count {
			return nil, ErrGiveUpNeedsAssets
		}
		out[id] += count
	}
	return out, nil
}
