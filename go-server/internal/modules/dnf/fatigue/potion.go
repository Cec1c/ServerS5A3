package fatigue

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type PotionDefinition struct {
	ItemID, Points, DailyLimit, CoolTimeMS int64
	MinimumLevel                           int64
	PVFPath, Group                         string
}

// PotionUsageItemKeyPrefix identifies item-to-group restore markers. The
// existing fatigue_potion_used_<group> value remains the only usage counter.
const PotionUsageItemKeyPrefix = "fatigue_potion_item_"

// PotionUsageItemKey is a durable item-to-group mapping marker. The existing
// fatigue_potion_used_<group> value remains the only usage counter.
func PotionUsageItemKey(itemID int64) string {
	return PotionUsageItemKeyPrefix + strconv.FormatInt(itemID, 10)
}

func (definition PotionDefinition) UsageGroup() string {
	group := definition.Group
	if group == "" {
		group = strconv.FormatInt(definition.ItemID, 10)
	}
	return group
}

type PotionResult struct {
	Remaining int64
	Character dnfrepo.CharacterRecord
}

// RestorePotion owns both the stack debit and durable character-day usage.
// Values originate in the active PVF, never in the client action arguments.
func RestorePotion(ctx context.Context, repos dnfrepo.Group, account dnfrepo.AccountRecord, characterID string, slot int16, definition PotionDefinition, now time.Time) (PotionResult, error) {
	if repos.CharacterAssets == nil {
		return PotionResult{}, dnfrepo.ErrCharacterAssetTransactionUnavailable
	}
	if slot < 0 || definition.ItemID <= 0 || definition.Points <= 0 || definition.PVFPath == "" {
		return PotionResult{}, fmt.Errorf("invalid fatigue potion contract")
	}
	var result PotionResult
	err := repos.CharacterAssets.WithinCharacterAssets(ctx, characterID, func(characters dnfrepo.CharacterRepository, items dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("fatigue character missing")
		}
		if character.AccountID != "" && character.AccountID != account.AccountID {
			return fmt.Errorf("fatigue potion account mismatch")
		}
		if int64(character.Level) < definition.MinimumLevel {
			return fmt.Errorf("fatigue potion minimum level not met")
		}
		inventory, found, err := items.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("fatigue inventory missing")
		}
		key := "0:" + strconv.Itoa(int(slot))
		stack, found := inventory.Slots[key]
		if !found || stack.ItemID != definition.ItemID || stack.Count <= 0 {
			return fmt.Errorf("fatigue potion source changed")
		}
		character = dnfrepo.CloneCharacter(character)
		inventory = dnfrepo.CloneInventory(inventory)
		Refresh(&character, account, now)
		if character.Stats["fatigue"] >= character.Stats["fatigue_limit"] {
			return fmt.Errorf("fatigue is already full")
		}
		group := definition.UsageGroup()
		dayKey, countKey, lastKey := "fatigue_potion_day_"+group, "fatigue_potion_used_"+group, "fatigue_potion_time_"+group
		day := premium.ServiceDayID(now)
		if character.Stats[dayKey] != day {
			character.Stats[dayKey] = day
			character.Stats[countKey] = 0
		}
		// Only the explicit PVF daily limit resets at 06:00. Cooldowns retain
		// elapsed-time semantics across service days, including 22-hour potions.
		limit := definition.DailyLimit
		if limit > 0 && character.Stats[countKey] >= limit {
			return fmt.Errorf("fatigue potion daily limit reached")
		}
		if definition.CoolTimeMS > 0 && character.Stats[lastKey] > 0 && now.UnixMilli()-character.Stats[lastKey] < definition.CoolTimeMS {
			return fmt.Errorf("fatigue potion cooldown")
		}
		character.Stats["fatigue"] += definition.Points
		if character.Stats["fatigue"] > character.Stats["fatigue_limit"] {
			character.Stats["fatigue"] = character.Stats["fatigue_limit"]
		}
		character.Stats["fatigue_used"] = character.Stats["fatigue_limit"] - character.Stats["fatigue"]
		character.Stats["fatigue_display_update"] = character.Stats["fatigue_used"]
		character.Stats[countKey]++
		character.Stats[PotionUsageItemKey(definition.ItemID)] = 1
		character.Stats[lastKey] = now.UnixMilli()
		stack.Count--
		if stack.Count == 0 {
			delete(inventory.Slots, key)
		} else {
			inventory.Slots[key] = stack
		}
		if err := dnfrepo.SaveInventoryFields(ctx, items, inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		result = PotionResult{Remaining: stack.Count, Character: character}
		return nil
	})
	return result, err
}
