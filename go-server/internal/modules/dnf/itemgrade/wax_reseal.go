package itemgrade

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrWaxReseal = errors.New("golden wax reseal rejected")

type WaxRangeWeight struct{ Minimum, Maximum, Weight int64 }
type WaxRules struct {
	Grades, Limits           []WaxRangeWeight
	Rarity, Extended, Growth [7]float64
}
type WaxDefinition struct {
	Equipment, Resealable, GoldenWax bool
	MinimumLevel, Rarity             int64
	Placement                        itemplacement.SlotPolicy
}
type WaxCatalog interface {
	ResolveWaxItem(uint32) (WaxDefinition, error)
}
type WaxCommand struct {
	AccountID, CharacterID   string
	TargetSlot, MaterialSlot int16
	TargetItemID             uint32
	UpdatedAt                time.Time
	// Current-client row projection and sealed-state interpretation are supplied
	// by the bridge, using the just-loaded authoritative instance, never C2S bytes.
	Project func(int16, dnfrepo.ItemStack) ([]byte, bool, error)
}
type WaxResult struct {
	TargetSlot, MaterialSlot int16
	TargetStack              dnfrepo.ItemStack
	MaterialRemaining, Cost  int64
	ResealCount              byte
}
type WaxOwner struct {
	assets   dnfrepo.CharacterAssetUnitOfWork
	settings dnfrepo.SettingsRepository
	catalog  WaxCatalog
	rules    WaxRules
}

func NewWaxOwner(group dnfrepo.Group, catalog WaxCatalog, rules WaxRules) (*WaxOwner, error) {
	if group.CharacterAssets == nil || catalog == nil {
		return nil, ErrOwnerUnavailable
	}
	return &WaxOwner{group.CharacterAssets, group.Settings, catalog, rules}, nil
}

// WaxCost uses the local reference implementation's PVF formula, with the
// runtime table supplied by the bridge. It rejects missing or overflowing rules.
func (r WaxRules) WaxCost(level, rarity int64, count byte) (int64, byte, error) {
	lookup := func(rows []WaxRangeWeight, value int64) int64 {
		for _, row := range rows {
			if value >= row.Minimum && value <= row.Maximum {
				return row.Weight
			}
		}
		return 0
	}
	limit := lookup(r.Limits, level)
	if limit > 7 {
		limit = 7
	}
	if rarity < 0 || rarity >= 7 || level <= 0 || limit <= 0 || int64(count) >= limit {
		return 0, 0, ErrWaxReseal
	}
	grade := lookup(r.Grades, level)
	base, extended, growth := r.Rarity[rarity], r.Extended[rarity], r.Growth[rarity]
	value := float64(grade) * base * extended * math.Pow(growth, float64(count))
	if grade <= 0 || base <= 0 || extended <= 0 || growth <= 0 || math.IsNaN(value) || math.IsInf(value, 0) || value < 1 || value >= float64(math.MaxInt64) {
		return 0, 0, ErrWaxReseal
	}
	return int64(value), byte(limit), nil
}

func (o *WaxOwner) Reseal(ctx context.Context, cmd WaxCommand) (WaxResult, error) {
	if o == nil || o.assets == nil || cmd.Project == nil || strings.TrimSpace(cmd.AccountID) == "" || strings.TrimSpace(cmd.CharacterID) == "" || cmd.TargetItemID == 0 || cmd.TargetSlot < 0 || cmd.MaterialSlot < 0 || cmd.TargetSlot == cmd.MaterialSlot {
		return WaxResult{}, ErrWaxReseal
	}
	definition, err := o.catalog.ResolveWaxItem(cmd.TargetItemID)
	if err != nil || !definition.Equipment || !definition.Resealable {
		return WaxResult{}, ErrWaxReseal
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WaxResult{}, err
	}
	now := cmd.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var result WaxResult
	err = o.assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != cmd.CharacterID || character.AccountID != cmd.AccountID {
			return ErrWaxReseal
		}
		inventory, found, err := inventories.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != cmd.CharacterID {
			return ErrWaxReseal
		}
		inventory = dnfrepo.CloneInventory(inventory)
		if err = dnfrepo.BindInventoryContainerState(ctx, o.settings, &inventory); err != nil {
			return err
		}
		targetKey, materialKey := inventoryKey(cmd.TargetSlot), inventoryKey(cmd.MaterialSlot)
		target, found := inventory.Slots[targetKey]
		if !found || target.ItemID != int64(cmd.TargetItemID) || target.Count != 1 || !waxUsable(target, now, true) || !definition.Placement.ForInventory(&inventory).Contains(0, cmd.TargetSlot) {
			return ErrWaxReseal
		}
		material, found := inventory.Slots[materialKey]
		if !found || material.ItemID <= 0 || material.ItemID > math.MaxUint32 || material.Count <= 0 || material.Count > math.MaxUint32 || !waxUsable(material, now, false) {
			return ErrMaterialMissing
		}
		wax, err := o.catalog.ResolveWaxItem(uint32(material.ItemID))
		if err != nil || !wax.GoldenWax || wax.Equipment || !wax.Placement.ForInventory(&inventory).Contains(0, cmd.MaterialSlot) {
			return ErrMaterialTypeInvalid
		}
		raw, sealed, err := cmd.Project(cmd.TargetSlot, target)
		if err != nil || sealed || len(raw) != 0x77 {
			return ErrWaxReseal
		}
		oldCount := raw[0x0a] >> 5
		if recorded := strings.TrimSpace(target.Extra["reseal_count"]); recorded != "" {
			count, parseErr := strconv.ParseUint(recorded, 10, 8)
			if parseErr != nil || count > 7 || byte(count) != oldCount {
				return ErrWaxReseal
			}
		}
		cost, _, err := o.rules.WaxCost(definition.MinimumLevel, definition.Rarity, oldCount)
		if err != nil {
			return err
		}
		if material.Count < cost {
			return ErrMaterialMissing
		}
		// Only the packed reseal bits and sealed byte change in the projected
		// instance. The lower five reinforcement bits and quality seed stay intact.
		raw = append([]byte(nil), raw...)
		raw[0x0a] = (raw[0x0a] & 0x1f) | ((oldCount + 1) << 5)
		raw[0x0d] = 1
		target.RawEntry = raw
		if target.Extra == nil {
			target.Extra = map[string]string{}
		}
		target.Extra["raw_entry_hex"] = hex.EncodeToString(raw)
		for _, key := range []string{"ext_data0", "ext0", "packed_flag_byte", "packed_flag", "packed"} {
			if key == "ext_data0" || key == "packed_flag_byte" {
				target.Extra[key] = strconv.Itoa(int(raw[0x0a]))
			} else {
				delete(target.Extra, key)
			}
		}
		target.Extra["reseal_count"] = strconv.Itoa(int(oldCount + 1))
		target.Extra["seal_flag"] = "1"
		target.Extra["seal"] = "1"
		delete(target.Extra, "seal_removed_by_first_equip")
		delete(target.Extra, "trade_locked_by_first_equip")
		// Bind is the independent character-bound flag used by trade guards.
		// Sealing is represented by the explicit seal extras and row +0x0D.
		// Keeping Bind=true would display a seal but still reject all trading.
		target.Bind = false
		inventory.Slots[targetKey] = target
		material.Count -= cost
		if material.Count == 0 {
			delete(inventory.Slots, materialKey)
		} else {
			inventory.Slots[materialKey] = material
		}
		inventory.UpdatedAt = now
		if err = dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		result = WaxResult{TargetSlot: cmd.TargetSlot, MaterialSlot: cmd.MaterialSlot, TargetStack: target, MaterialRemaining: material.Count, Cost: cost, ResealCount: oldCount + 1}
		return nil
	})
	if err != nil {
		return WaxResult{}, err
	}
	return result, nil
}

func waxUsable(stack dnfrepo.ItemStack, now time.Time, permanent bool) bool {
	if !stack.ExpireAt.IsZero() && (permanent || !now.Before(stack.ExpireAt)) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(stack.Extra["equipment_lock_state"])) {
	case "1", "2", "active", "locked", "unlocking", "pending_unlock":
		return false
	}
	for _, key := range []string{"expire_time", "expire_unix"} {
		if text := strings.TrimSpace(stack.Extra[key]); text != "" {
			value, err := strconv.ParseInt(text, 10, 64)
			if err != nil || value < 0 || value > 0 && (permanent || now.Unix() >= value) {
				return false
			}
		}
	}
	return true
}
