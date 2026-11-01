package inventory

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrEventItem = errors.New("event item source, target or container state is invalid")

type EventItemKind byte

const (
	EventInventoryExpansion EventItemKind = iota + 1
	EventSilverCargo
	EventPermanentWeapon
)

// EventItemPolicy contains only server-resolved PVF data. Resolve executes
// inside the asset transaction against the currently held source and target.
type EventItemPolicy struct {
	Kind          EventItemKind
	SourceSlots   itemplacement.SlotPolicy
	TargetSlots   itemplacement.SlotPolicy
	MinimumLevel  int64
	OutputItemID  int64
	OutputPVFPath string
}

type EventItemRequest struct {
	AccountID, CharacterID string
	SourceSlot, TargetSlot int16
	Kind                   EventItemKind
	Revalidate             func() bool
	Resolve                func(repo.CharacterRecord, repo.ItemStack, repo.ItemStack) (EventItemPolicy, repo.ItemStack, repo.ItemStack, error)
}

type EventItemResult struct {
	SourceItemID              int64
	Remaining                 int64
	MainExpansion, CargoSlots uint16
	Target                    repo.ItemStack
}

// ApplyEventItem commits consumption and its effect together. These legacy
// packets carry no nonce; each accepted command is one use. We never retry a
// commit or network-send failure, and re-read the held items on every request.
func ApplyEventItem(ctx context.Context, g repo.Group, req EventItemRequest, now time.Time) (EventItemResult, error) {
	if g.CeraShopAssets == nil || req.AccountID == "" || req.CharacterID == "" || req.SourceSlot < 0 || req.Resolve == nil || req.Revalidate == nil || now.IsZero() || repo.IsAccountSharedInventorySlot(0, req.SourceSlot) {
		return EventItemResult{}, ErrEventItem
	}
	if req.Kind == EventPermanentWeapon && (req.TargetSlot < 0 || req.TargetSlot == req.SourceSlot || repo.IsAccountSharedInventorySlot(0, req.TargetSlot)) {
		return EventItemResult{}, ErrEventItem
	}
	var result EventItemResult
	err := g.CeraShopAssets.WithinCeraShopAssets(ctx, req.AccountID, req.CharacterID, repo.CharacterContainerStateScope(req.CharacterID), func(accounts repo.AccountRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository, settings repo.SettingsRepository) error {
		if !req.Revalidate() {
			return ErrEventItem
		}
		a, found, err := accounts.Load(ctx, req.AccountID)
		if err != nil {
			return err
		}
		if !found || a.AccountID != req.AccountID {
			return ErrEventItem
		}
		c, found, err := characters.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.AccountID != req.AccountID || c.Stats["delete_flag"] != 0 || c.Location.DungeonID != 0 {
			return ErrEventItem
		}
		bag, found, err := inventories.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found || bag.CharacterID != req.CharacterID {
			return ErrEventItem
		}
		bag = repo.CloneInventory(bag)
		stateRecord, found, err := settings.Load(ctx, repo.CharacterContainerStateScope(req.CharacterID))
		if err != nil {
			return err
		}
		if !found {
			return ErrEventItem
		}
		state, err := repo.CharacterContainerStateFromSettings(stateRecord, req.CharacterID)
		if err != nil {
			return err
		}
		bag.ContainerState = &state
		sourceKey, targetKey := fmt.Sprintf("0:%d", req.SourceSlot), fmt.Sprintf("0:%d", req.TargetSlot)
		source, found := bag.Slots[sourceKey]
		if !found || source.ItemID <= 0 || source.ItemID > math.MaxUint32 || source.Count < 1 || source.Count > math.MaxInt32 {
			return ErrEventItem
		}
		target := bag.Slots[targetKey]
		policy, projected, projectedTarget, err := req.Resolve(c, cloneStack(source), cloneStack(target))
		if err != nil {
			return err
		}
		if policy.Kind != req.Kind || policy.MinimumLevel < 0 || int64(c.Level) < policy.MinimumLevel || !policy.SourceSlots.ForInventory(&bag).Contains(0, req.SourceSlot) || projected.ItemID != source.ItemID || projected.Count != source.Count || !eventItemUsable(projected, now) {
			return ErrEventItem
		}
		source = projected
		result = EventItemResult{SourceItemID: source.ItemID}
		switch req.Kind {
		case EventInventoryExpansion:
			if state.MainSlotCount >= 24 {
				return ErrEventItem
			}
			result.MainExpansion = state.MainSlotCount + 8
			stateRecord = repo.CloneSettings(stateRecord)
			stateRecord.Values["main_list_param16"] = strconv.Itoa(int(result.MainExpansion))
		case EventSilverCargo:
			// PVF explicitly says the tool is consumed even when the owner has
			// silver or better storage. Preserve that capacity, never downgrade.
			result.CargoSlots = max(state.PersonalCargoSlotCount, 40)
			stateRecord = repo.CloneSettings(stateRecord)
			stateRecord.Values["personal_cargo_list_param16"] = strconv.Itoa(int(result.CargoSlots))
		case EventPermanentWeapon:
			if target.ItemID <= 0 || target.Count != 1 || projectedTarget.ItemID != target.ItemID || projectedTarget.Count != 1 || !eventItemUsable(projectedTarget, now) || !policy.TargetSlots.ForInventory(&bag).Contains(0, req.TargetSlot) || policy.OutputItemID <= 0 || policy.OutputItemID > math.MaxUint32 || policy.OutputItemID == target.ItemID || policy.OutputPVFPath == "" {
				return ErrEventItem
			}
			target = makeEventWeaponPermanent(projectedTarget, policy.OutputItemID, policy.OutputPVFPath)
			bag.Slots[targetKey] = target
			result.Target = cloneStack(target)
		default:
			return ErrEventItem
		}
		source.Count--
		result.Remaining = source.Count
		if source.Count == 0 {
			delete(bag.Slots, sourceKey)
		} else {
			updateStackRawAmount(&source)
			bag.Slots[sourceKey] = source
		}
		bag.UpdatedAt = now
		if err := repo.SaveInventoryFields(ctx, inventories, bag, repo.InventoryFieldSlots); err != nil {
			return err
		}
		if req.Kind != EventPermanentWeapon {
			stateRecord.Values["source"] = "event_container_item"
			stateRecord.Values["last_event_item"] = strconv.FormatInt(source.ItemID, 10)
			stateRecord.UpdatedAt = now
			if err := settings.Save(ctx, stateRecord); err != nil {
				return err
			}
		}
		if !req.Revalidate() {
			return ErrEventItem
		}
		return nil
	})
	if err != nil {
		return EventItemResult{}, err
	}
	return result, nil
}

func eventItemUsable(s repo.ItemStack, now time.Time) bool {
	if itemlock.IsLockedAt(s, now) || (!s.ExpireAt.IsZero() && !now.Before(s.ExpireAt)) {
		return false
	}
	for _, key := range []string{"expire_time", "expire_unix", "daily_delete_at"} {
		if raw := s.Extra[key]; raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v < 0 || (v > 0 && v <= now.Unix()) {
				return false
			}
		}
	}
	if len(s.RawEntry) != 0 && len(s.RawEntry) != currentItemListEntrySize {
		return false
	}
	if len(s.RawEntry) == currentItemListEntrySize {
		v := binary.LittleEndian.Uint32(s.RawEntry[currentItemListExpireTimeOffset:])
		if v != 0 && int64(v) <= now.Unix() {
			return false
		}
	}
	return true
}

func makeEventWeaponPermanent(s repo.ItemStack, id int64, path string) repo.ItemStack {
	s = cloneStack(s)
	restoreEnchantRawCache(&s)
	oldExpire := itemListStackExpireUnix(s)
	s.ItemID, s.ExpireAt = id, time.Time{}
	if s.Extra == nil {
		s.Extra = make(map[string]string)
	}
	for _, key := range []string{"expire_time", "expire_unix", "expiration_source", "usable_period_days", "daily_delete_at", "gm_expire_days"} {
		delete(s.Extra, key)
	}
	s.Extra["pvf_path"] = path
	s.Extra["item_kind"] = "equipment"
	if len(s.RawEntry) == currentItemListEntrySize {
		binary.LittleEndian.PutUint32(s.RawEntry[2:6], uint32(id))
		clear(s.RawEntry[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4])
		if oldExpire != 0 && binary.LittleEndian.Uint32(s.RawEntry[legacyWrongItemExpireTimeOffset:]) == oldExpire {
			clear(s.RawEntry[legacyWrongItemExpireTimeOffset : legacyWrongItemExpireTimeOffset+4])
		}
	}
	// The limited weapon's PVF explicitly removes enchantment on conversion.
	// Keep reinforcement, forging, quality and all unrelated instance bytes.
	setEnchantFields(&s, 0, 0)
	if len(s.RawEntry) != 0 {
		s.Extra["raw_entry_hex"] = hex.EncodeToString(s.RawEntry)
	}
	return s
}
