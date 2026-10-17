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
// A ticket is consumed from the exact requested main-inventory slot. When the
// ticket declares `[need material]`, MaterialItemID/MaterialCount carry the
// first positive pair from that section and are charged in the same character
// asset transaction as the ticket and the cumulative grant.
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
	// MaterialItemID/MaterialCount are the explicit PVF `[need material]` pair.
	// Both zero means this ticket has no material fee. A partial or negative
	// pair is invalid and is rejected before the transaction starts.
	MaterialItemID int64
	MaterialCount  int64
}

type DimensionGateExtraEntryResult struct {
	Character     dnfrepo.CharacterRecord
	Granted       int64
	Total         int64
	Slot          EntryItemConsumedSlot
	MaterialSlots []EntryItemConsumedSlot
}

// DimensionGateExtraMaterialRequirement applies the vendor ticket resolver to
// a PVF `[need material]` integer list: use the first positive id/count pair,
// and treat a missing or wholly invalid list as no material fee. The vendor
// (`ItemMetadataResolver.ParseNeedMaterials`) walks pairs and skips invalid
// ones; the ticket service then takes only the first valid material.
func DimensionGateExtraMaterialRequirement(values []int64) (itemID, count int64) {
	for index := 0; index+1 < len(values); index += 2 {
		itemID, count = values[index], values[index+1]
		if itemID > 0 && count > 0 {
			return itemID, count
		}
	}
	return 0, 0
}

// GrantDimensionGateExtraEntry commits one ticket, its optional material fee
// and its cumulative daily grant in the same character asset transaction. It
// grants the whole amount or rejects without consuming the ticket or material;
// it never silently truncates a grant.
func (o *Owner) GrantDimensionGateExtraEntry(ctx context.Context, cmd DimensionGateExtraEntryCommand) (DimensionGateExtraEntryResult, error) {
	if o == nil || o.assets == nil || strings.TrimSpace(cmd.CharacterID) == "" || strings.TrimSpace(cmd.AccountID) == "" || cmd.Slot < 0 || cmd.ItemID <= 0 || cmd.Count <= 0 || cmd.MinimumLevel < 0 || cmd.Cooldown < 0 || cmd.UpdatedAt.IsZero() {
		return DimensionGateExtraEntryResult{}, ErrEntryItemConsumeInvalid
	}
	if cmd.MaterialItemID < 0 || cmd.MaterialCount < 0 || (cmd.MaterialItemID > 0) != (cmd.MaterialCount > 0) {
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
		// An identical replay inside the PVF cooldown is rejected before the bag
		// is loaded, so it cannot consume the ticket or material a second time.
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
		// Charge the explicit material fee after the ticket is known to exist.
		// Both changes stay on the cloned inventory and are saved together.
		// Reuse consumeEntryItemRequirement unchanged: material selection keeps
		// the same main-inventory and stack rules as other entry consumers,
		// without adding a material-specific lock or expiration exception.
		var materialSlots []EntryItemConsumedSlot
		if cmd.MaterialItemID > 0 {
			materialSlots, err = consumeEntryItemRequirement(&inventory, EntryItemRequirement{
				ItemID:         cmd.MaterialItemID,
				Count:          cmd.MaterialCount,
				ConsumeOnEntry: true,
			}, cmd.Project)
			if err != nil {
				return err
			}
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
		result = DimensionGateExtraEntryResult{
			Character:     character,
			Granted:       extra + cmd.Count,
			Total:         total + cmd.Count,
			Slot:          consumed[0],
			MaterialSlots: materialSlots,
		}
		return nil
	})
	if err != nil {
		return DimensionGateExtraEntryResult{}, err
	}
	return result, nil
}
