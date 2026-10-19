package inventory

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrGuildMileageItem = errors.New("guild mileage item contract is invalid")

// GuildMileageItemDefinition is trusted runtime-PVF data, never client input.
type GuildMileageItemDefinition struct {
	ItemID       int64
	PVFPath      string
	Amount       uint32
	MinimumLevel int64
}

type GuildMileageItemRequest struct {
	Owner      dnfrepo.GuildOwner
	SlotIndex  int16
	Definition GuildMileageItemDefinition
	// The bridge rechecks its canonical character generation and village scene
	// after the transaction has acquired its database locks.
	Revalidate func() bool
}

type GuildMileageItemResult struct {
	Remaining int64
	GuildID   uint32
	Mileage   uint32
}

// UseGuildMileageItem consumes one physical bag and credits its guild in one
// transaction. op515 has no operation nonce: each accepted command is one use,
// and no error (including an unknown SQL commit outcome) is retried here.
func (o *Owner) UseGuildMileageItem(ctx context.Context, req GuildMileageItemRequest, now time.Time) (GuildMileageItemResult, error) {
	if o == nil || req.SlotIndex < 0 || req.Definition.ItemID <= 0 || req.Definition.ItemID > math.MaxUint32 || req.Definition.Amount == 0 || strings.TrimSpace(req.Definition.PVFPath) == "" || req.Definition.MinimumLevel < 0 || req.Revalidate == nil {
		return GuildMileageItemResult{}, ErrGuildMileageItem
	}
	uow, ok := o.guild.(dnfrepo.GuildInventoryUnitOfWork)
	if !ok {
		return GuildMileageItemResult{}, dnfrepo.ErrGuildTransaction
	}
	var result GuildMileageItemResult
	err := uow.WithinGuildInventory(ctx, req.Owner, func(tx dnfrepo.Group) error {
		if !req.Revalidate() {
			return dnfguild.ErrPermissionDenied
		}
		character, found, err := tx.Character.Load(ctx, req.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || int64(character.Level) < req.Definition.MinimumLevel {
			return ErrGuildMileageItem
		}
		record, found, err := tx.Inventory.Load(ctx, req.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrInventoryNotFound
		}
		record = dnfrepo.CloneInventory(record)
		key := slotKey(dnfrepo.MainInventoryListType, req.SlotIndex)
		stack, found := record.Slots[key]
		// The action ACK contains the action ID, not the remaining count. The
		// authoritative op14 slot refresh uses the existing signed i32 amount.
		if !found || stack.ItemID != req.Definition.ItemID || stack.Count <= 0 || stack.Count > math.MaxInt32 || (!stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt)) {
			return ErrGuildMileageItem
		}
		if p := strings.TrimSpace(stack.Extra["pvf_path"]); p != "" && !strings.EqualFold(strings.ReplaceAll(p, "\\", "/"), req.Definition.PVFPath) {
			return ErrGuildMileageItem
		}
		if kind := strings.TrimSpace(stack.Extra["item_kind"]); kind != "" && !strings.EqualFold(kind, "stackable") {
			return ErrGuildMileageItem
		}
		registry, err := dnfguild.Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, err := registry.GrantItemMileage(req.Owner, req.Definition.Amount, character.Name, now)
		if err != nil {
			return err
		}
		stack.Count--
		if stack.Count == 0 {
			delete(record.Slots, key)
		} else {
			updateStackRawAmount(&stack)
			record.Slots[key] = stack
		}
		record.UpdatedAt = now
		if err := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, record, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		if err := dnfguild.Save(ctx, tx.Settings, registry, now); err != nil {
			return err
		}
		if !req.Revalidate() {
			return dnfguild.ErrPermissionDenied
		}
		result = GuildMileageItemResult{Remaining: stack.Count, GuildID: g.ID, Mileage: g.Mileage}
		return nil
	})
	if err != nil {
		return GuildMileageItemResult{}, err
	}
	return result, nil
}
