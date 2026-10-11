package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrDailyEntryLimit = errors.New("dungeon daily entry limit reached")

type DailyEntryCommand struct {
	CharacterID        string
	DungeonID          int64
	Limit              int64
	RequiredItemWeight float64
	Requirements       []EntryItemRequirement
	// FreePasses are ordered PVF alternatives with fixed counts, independent of
	// the normal ticket multiplier. They still consume one daily allowance.
	FreePasses []EntryItemRequirement
	UpdatedAt  time.Time
	Project    EntryItemProjector
}

type DailyEntryResult struct {
	Character dnfrepo.CharacterRecord
	Used      int64
	Consumed  []EntryItemConsumedStack
}

func dailyEntryKeys(dungeonID int64) (string, string) {
	return fmt.Sprintf("dungeon_daily_entry_%d_day", dungeonID), fmt.Sprintf("dungeon_daily_entry_%d_used", dungeonID)
}

// DailyEntryUsed follows the existing 06:00 UTC+8 service day. Reading a new
// day does not write the database; the first successful entry commits the reset.
func DailyEntryUsed(character dnfrepo.CharacterRecord, dungeonID int64, now time.Time) (int64, error) {
	dayKey, usedKey := dailyEntryKeys(dungeonID)
	if character.Stats[dayKey] != premium.ServiceDayID(now) {
		return 0, nil
	}
	used := character.Stats[usedKey]
	if used < 0 {
		return 0, ErrEntryItemConsumeInvalid
	}
	return used, nil
}

// Official 24.9 1FAB59C..1FAB5E5 multiplies the PVF required count by
// pow(used+1, [required item weight]); the live rift weight is exactly 1.
func DailyEntryRequirements(base []EntryItemRequirement, used int64, weight float64) ([]EntryItemRequirement, error) {
	if used < 0 || weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
		return nil, ErrEntryItemConsumeInvalid
	}
	multiplier := math.Pow(float64(used+1), weight)
	out := append([]EntryItemRequirement(nil), base...)
	for i := range out {
		count := float64(out[i].Count) * multiplier
		if out[i].ItemID <= 0 || out[i].Count <= 0 || count >= float64(math.MaxInt64) || math.IsInf(count, 0) {
			return nil, ErrEntryItemConsumeInvalid
		}
		out[i].Count = int64(count)
		if out[i].Count <= 0 {
			return nil, ErrEntryItemConsumeInvalid
		}
	}
	return out, nil
}

func DailyEntryAlternatives(base, passes []EntryItemRequirement, used int64, weight float64) ([][]EntryItemRequirement, error) {
	normal, err := DailyEntryRequirements(base, used, weight)
	if err != nil {
		return nil, err
	}
	out := make([][]EntryItemRequirement, 0, len(passes)+1)
	for _, pass := range passes {
		if pass.ItemID <= 0 || pass.Count <= 0 || !pass.ConsumeOnEntry {
			return nil, ErrEntryItemConsumeInvalid
		}
		out = append(out, []EntryItemRequirement{pass})
	}
	return append(out, normal), nil
}

// ConsumeDailyEntry commits the durable counter and all entry tickets in the
// same CharacterAssets transaction. Failed inventory projection or exhausted
// allowance rolls everything back. It never grants items or modifies quests.
func (o *Owner) ConsumeDailyEntry(ctx context.Context, cmd DailyEntryCommand) (DailyEntryResult, error) {
	if o == nil || o.assets == nil || strings.TrimSpace(cmd.CharacterID) == "" || cmd.DungeonID <= 0 || cmd.Limit <= 0 || cmd.UpdatedAt.IsZero() {
		return DailyEntryResult{}, ErrEntryItemConsumeInvalid
	}
	ctx = contextOrBackground(ctx)
	var result DailyEntryResult
	err := o.assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(chars dnfrepo.CharacterRepository, bags dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		character = dnfrepo.CloneCharacter(character)
		used, err := DailyEntryUsed(character, cmd.DungeonID, cmd.UpdatedAt)
		if err != nil {
			return err
		}
		if used >= cmd.Limit {
			return ErrDailyEntryLimit
		}
		alternatives, err := DailyEntryAlternatives(cmd.Requirements, cmd.FreePasses, used, cmd.RequiredItemWeight)
		if err != nil {
			return err
		}
		inventory, found, err := bags.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found && (len(cmd.Requirements) > 0 || len(cmd.FreePasses) > 0) {
			return ErrEntryItemConsumeInsufficient
		}
		inventory = dnfrepo.CloneInventory(inventory)
		var requirements []EntryItemRequirement
		selected := false
		for _, alternative := range alternatives {
			candidate := dnfrepo.CloneInventory(inventory)
			valid := true
			for _, requirement := range alternative {
				if !requirement.ConsumeOnEntry {
					continue
				}
				if _, err := consumeEntryItemRequirement(&candidate, requirement, nil); err != nil {
					valid = false
					break
				}
			}
			if valid {
				requirements, selected = alternative, true
				break
			}
		}
		if !selected {
			return ErrEntryItemConsumeInsufficient
		}
		for _, requirement := range requirements {
			if !requirement.ConsumeOnEntry {
				continue
			}
			slots, err := consumeEntryItemRequirement(&inventory, requirement, cmd.Project)
			if err != nil {
				return err
			}
			result.Consumed = append(result.Consumed, EntryItemConsumedStack{ItemID: requirement.ItemID, Count: requirement.Count, Slots: slots})
		}
		if len(result.Consumed) > 0 {
			inventory.UpdatedAt = cmd.UpdatedAt
			if err := dnfrepo.SaveInventoryFields(ctx, bags, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if character.Stats == nil {
			character.Stats = map[string]int64{}
		}
		dayKey, usedKey := dailyEntryKeys(cmd.DungeonID)
		character.Stats[dayKey] = premium.ServiceDayID(cmd.UpdatedAt)
		character.Stats[usedKey] = used + 1
		character.UpdatedAt = cmd.UpdatedAt
		if err := dnfrepo.SaveCharacterFields(ctx, chars, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		result.Character, result.Used = character, used+1
		return nil
	})
	if err != nil {
		return DailyEntryResult{}, err
	}
	return result, nil
}
