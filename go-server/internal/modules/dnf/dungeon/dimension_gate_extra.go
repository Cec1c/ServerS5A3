package dungeon

import (
	"context"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const dimensionGateExtraLastUseKey = "dimension_gate_extra_last_use_ms"

// DimensionGateExtraEntryCommand is an internal, PVF-resolved command, not a
// network request. The bridge must resolve the item action, expiration, level,
// cooldown and usable place before dispatch; opcode/ACK wiring remains pending.
// A ticket is consumed from the exact requested main-inventory slot.
type DimensionGateExtraEntryCommand struct {
	CharacterID          string
	AccountID            string
	Slot                 int16
	ItemID               int64
	Count                int64
	BaseLimit            int64
	ExtraLimit           int64
	MinimumLevel         int
	Cooldown             time.Duration
	ExpiresAt            time.Time
	RequireBaseExhausted bool
	UpdatedAt            time.Time
	Project              EntryItemProjector
}

type DimensionGateExtraEntryResult struct {
	Character dnfrepo.CharacterRecord
	Granted   int64
	Total     int64
	Slot      EntryItemConsumedSlot
}

// GrantDimensionGateExtraEntry commits one ticket and its cumulative daily
// grant in the same character asset transaction. It grants the whole amount or
// rejects without consuming the ticket; it never silently truncates a grant.
func (o *Owner) GrantDimensionGateExtraEntry(ctx context.Context, cmd DimensionGateExtraEntryCommand) (DimensionGateExtraEntryResult, error) {
	if o == nil || o.assets == nil || strings.TrimSpace(cmd.CharacterID) == "" || strings.TrimSpace(cmd.AccountID) == "" || cmd.Slot < 0 || cmd.ItemID <= 0 || cmd.Count <= 0 || cmd.MinimumLevel < 0 || cmd.Cooldown < 0 || cmd.UpdatedAt.IsZero() {
		return DimensionGateExtraEntryResult{}, ErrEntryItemConsumeInvalid
	}
	if _, _, _, err := DimensionGateDailyAllowance(dnfrepo.CharacterRecord{}, cmd.UpdatedAt, cmd.BaseLimit, cmd.ExtraLimit); err != nil || cmd.Count > cmd.ExtraLimit {
		return DimensionGateExtraEntryResult{}, ErrEntryItemConsumeInvalid
	}
	if !cmd.ExpiresAt.IsZero() && !cmd.UpdatedAt.Before(cmd.ExpiresAt) {
		return DimensionGateExtraEntryResult{}, ErrEntryItemConsumeInvalid
	}
	ctx = contextOrBackground(ctx)
	var result DimensionGateExtraEntryResult
	err := o.assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(chars dnfrepo.CharacterRepository, bags dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		if character.AccountID != cmd.AccountID || character.Level < cmd.MinimumLevel || character.Location.DungeonID != 0 {
			return ErrEntryItemConsumeInvalid
		}
		character = dnfrepo.CloneCharacter(character)
		used, extra, total, err := DimensionGateDailyAllowance(character, cmd.UpdatedAt, cmd.BaseLimit, cmd.ExtraLimit)
		if err != nil {
			return err
		}
		if cmd.Count > cmd.ExtraLimit-extra {
			return ErrDailyEntryLimit
		}
		if cmd.RequireBaseExhausted && used < cmd.BaseLimit {
			return ErrDailyEntryLimit
		}
		last := character.Stats[dimensionGateExtraLastUseKey]
		if last < 0 || (last != 0 && cmd.UpdatedAt.Before(time.UnixMilli(last).Add(cmd.Cooldown))) {
			return ErrEntryItemConsumeInvalid
		}
		inventory, found, err := bags.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrEntryItemConsumeInsufficient
		}
		inventory = dnfrepo.CloneInventory(inventory)
		key := "0:" + strconv.Itoa(int(cmd.Slot))
		stack, found := inventory.Slots[key]
		if !found || stack.ItemID != cmd.ItemID || stack.Count <= 0 {
			return ErrEntryItemConsumeInsufficient
		}
		if !stack.ExpireAt.IsZero() && !cmd.UpdatedAt.Before(stack.ExpireAt) {
			return ErrEntryItemConsumeInvalid
		}
		// Isolate the selected slot so another stack cannot be chosen instead.
		selected := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{key: stack}}
		consumed, err := consumeEntryItemRequirement(&selected, EntryItemRequirement{ItemID: cmd.ItemID, Count: 1, ConsumeOnEntry: true}, cmd.Project)
		if err != nil {
			return err
		}
		if remaining, ok := selected.Slots[key]; ok {
			inventory.Slots[key] = remaining
		} else {
			delete(inventory.Slots, key)
		}
		inventory.UpdatedAt = cmd.UpdatedAt
		if character.Stats == nil {
			character.Stats = map[string]int64{}
		}
		character.Stats[dimensionGateDayKey] = premium.ServiceDayID(cmd.UpdatedAt)
		character.Stats[dimensionGateUsedKey] = used
		character.Stats[dimensionGateExtraKey] = extra + cmd.Count
		character.Stats[dimensionGateExtraLastUseKey] = cmd.UpdatedAt.UnixMilli()
		character.UpdatedAt = cmd.UpdatedAt
		if err := dnfrepo.SaveInventoryFields(ctx, bags, inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		if err := dnfrepo.SaveCharacterFields(ctx, chars, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		result = DimensionGateExtraEntryResult{Character: character, Granted: extra + cmd.Count, Total: total + cmd.Count, Slot: consumed[0]}
		return nil
	})
	if err != nil {
		return DimensionGateExtraEntryResult{}, err
	}
	return result, nil
}
