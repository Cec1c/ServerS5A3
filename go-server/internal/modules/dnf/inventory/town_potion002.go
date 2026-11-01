package inventory

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const townPotion002CooldownKey = "town_potion002_10007343_next_ms"

// A specialized effect owner runs before the generic provenance gate. The gate
// deliberately continues to reject the family, including metadata-only rows.
func (o *Owner) tryTownPotion002(ctx context.Context, cmd Command) (UseStackableResult, bool, error) {
	// Leave unrelated owners and their resolver/error order untouched.
	if cmd.ItemTemplateID != 10007343 {
		return UseStackableResult{}, false, nil
	}
	if cmd.StackableUseProvenanceResolver == nil {
		return UseStackableResult{}, true, ErrUseStackableContractRequired
	}
	r, err := cmd.StackableUseProvenanceResolver(int64(cmd.ItemTemplateID))
	if err != nil {
		return UseStackableResult{}, true, err
	}
	d := r.TownPotion002
	if !r.Found || r.ItemID != int64(cmd.ItemTemplateID) || stackableTypeToken(r.StackableType) != "[expert town potion]" || r.ActionType != "" || d == nil ||
		d.ItemID != 10007343 || d.ItemID != r.ItemID || d.PVFPath != r.PVFPath || d.MinimumLevel != 1 || d.CooldownMillis != 60000 ||
		d.Buff.Mode != "reinforce" || d.Buff.BonusWeight != 10000 || d.Buff.DurationMillis != 30000 || d.Buff.PVFPath != d.PVFPath ||
		cmd.Operation != "use_stackable" || cmd.SourceListType != listTypeMain || cmd.SourceInstanceValue == 0 || cmd.ReservedValue != 0 {
		return UseStackableResult{}, true, ErrUseStackableContractRequired
	}
	if o.assets == nil || o.characters == nil || o.inItemTransaction {
		return UseStackableResult{}, true, ErrCharacterAssetTxnRequired
	}
	now := cmd.ItemUsageNow.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	id := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	var result UseStackableResult
	err = o.withinCharacterAssetTransaction(ctx, id, func(tx *Owner) error {
		c, found, e := tx.characters.Load(ctx, id)
		if e != nil {
			return e
		}
		if !found || c.CharacterID != id || cmd.AccountID == "" || c.AccountID != cmd.AccountID {
			return ErrCharacterRequired
		}
		if int64(c.Level) < d.MinimumLevel {
			return ErrUseStackableContractRequired
		}
		inv, found, e := tx.loadInventoryWithContainerState(ctx, id)
		if e != nil {
			return e
		}
		if !found {
			return ErrInventoryNotFound
		}
		policy := itemplacement.SlotPolicy{ListType: listTypeMain, Start: 65, End: 120, AllowQuickSlots: true, ContainerState: inv.ContainerState}
		if !policy.Contains(listTypeMain, cmd.SourceSlotIndex) {
			return ErrMoveSlotLocked
		}
		inv = repo.CloneInventory(inv)
		c = repo.CloneCharacter(c)
		key := slotKey(listTypeMain, cmd.SourceSlotIndex)
		stack, found := inv.Slots[key]
		if !found || stack.Count <= 0 || stack.Count > math.MaxInt32 {
			return ErrSlotNotFound
		}
		if stack.ItemID != d.ItemID || !strings.EqualFold(stack.Extra["item_kind"], "stackable") || uint32(commonItemListStackValueA(stack)) != uint32(cmd.SourceInstanceValue) {
			return ErrItemMismatch
		}
		if isEquipmentLocked(stack) {
			return ErrItemLocked
		}
		if path := stack.Extra["pvf_path"]; path != "" && !strings.EqualFold(strings.ReplaceAll(path, "\\", "/"), d.PVFPath) {
			return ErrItemMismatch
		}
		expire := firstExtraInt(stack.Extra, 0, "expire_time", "expire_unix")
		if (!stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt)) || (expire > 0 && now.Unix() >= expire) || (!d.ExpirationDate.IsZero() && !now.Before(d.ExpirationDate)) {
			return fmt.Errorf("town potion source expired")
		}
		if c.Stats[townPotion002CooldownKey] > now.UnixMilli() {
			return fmt.Errorf("town potion cooldown active")
		}
		// An operational boost-disable must not turn this newly supported potion
		// into a successful debit with no server-side effect.
		if upgradeRateBuffApplyBonus(d.Buff) <= 0 {
			return ErrUseStackableContractRequired
		}
		until, bonus, ok := recordUpgradeRateBuff(&c, d.Buff, stack.ItemID, now)
		if !ok || bonus <= 0 {
			return ErrUseStackableContractRequired
		}
		c.Stats[townPotion002CooldownKey] = now.UnixMilli() + d.CooldownMillis
		previous := stack.Count
		stack.Count--
		if stack.Count == 0 {
			delete(inv.Slots, key)
		} else {
			updateStackRawAmount(&stack)
			inv.Slots[key] = stack
		}
		inv.UpdatedAt = now
		c.UpdatedAt = now
		if e = repo.SaveInventoryFields(ctx, tx.repo, inv, repo.InventoryFieldSlots); e != nil {
			return e
		}
		if e = repo.SaveCharacterFields(ctx, tx.characters, c, repo.CharacterFieldStats); e != nil {
			return e
		}
		result = UseStackableResult{CharacterID: id, AccountID: cmd.AccountID, ListType: listTypeMain, SlotIndex: cmd.SourceSlotIndex, InstanceValue: cmd.SourceInstanceValue,
			ItemID: stack.ItemID, PreviousCount: previous, RemainingCount: stack.Count, Changed: true, PVFPath: d.PVFPath, StackableType: r.StackableType,
			UpgradeRateBuffMode: "reinforce", UpgradeRateBuffBonus: bonus, UpgradeRateBuffUntil: until, UpgradeRateBuffItemID: stack.ItemID}
		return nil
	})
	if err != nil {
		return UseStackableResult{}, true, err
	}
	return result, true, nil
}
