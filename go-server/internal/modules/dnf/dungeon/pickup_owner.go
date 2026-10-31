package dungeon

import (
	"context"
	"errors"
	"fmt"
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type PickupItemKind string

const (
	PickupItemStackable PickupItemKind = "stackable"
	PickupItemEquipment PickupItemKind = "equipment"
)

var (
	ErrPickupItemInvalid   = errors.New("dungeon pickup item is invalid")
	ErrPickupInventoryFull = errors.New("dungeon pickup inventory category is full")
	ErrPickupStackLimit    = errors.New("dungeon pickup exceeds stack limit")
	ErrInventoryNotFound   = errors.New("dungeon inventory is not found")
)

type PickupItemDefinition struct {
	// ListType is the authoritative destination container. Zero preserves
	// ordinary bag placement; list 38 is the guild medal/gem container.
	ListType        byte
	ItemID          uint32
	Kind            PickupItemKind
	StackLimit      int64
	SlotStart       int16
	SlotEnd         int16
	PreferQuickSlot bool
	AllowQuickSlots bool
}

type PickupItemPlacement struct {
	Definition        PickupItemDefinition
	Amount            uint32
	BuildNew          func(int16) (dnfrepo.ItemStack, error)
	NormalizeExisting func(dnfrepo.ItemStack) (dnfrepo.ItemStack, error)
	// MergeIdentity 是本次发放的身份投影（item_kind / pvf_path / stackable_type /
	// 实例到期时间 + Bind）。合并进已有行之前必须过 dnfrepo.CanMergeItemStack：
	// 身份或实例到期时间冲突的行一律不得并（"都要堆叠"只在同一身份族内成立）。
	// ItemID 为 0 表示调用方没有身份证据（例如纯装备/旧调用），退回
	// "同 item + 未绑定 + 上限"的旧判定。
	MergeIdentity dnfrepo.ItemStack
}

type PickupItemCommand struct {
	CharacterID string
	Placement   PickupItemPlacement
	UpdatedAt   time.Time
	Finalize    func(int16, dnfrepo.ItemStack) (dnfrepo.ItemStack, error)
}

type PickupItemResult struct {
	Slot  uint16
	Stack dnfrepo.ItemStack
}

func (o *Owner) GrantPickupItem(ctx context.Context, cmd PickupItemCommand) (PickupItemResult, error) {
	if o == nil || o.items == nil || strings.TrimSpace(cmd.CharacterID) == "" {
		return PickupItemResult{}, ErrOwnerUnavailable
	}
	ctx = contextOrBackground(ctx)
	now := updatedAtOrNow(cmd.UpdatedAt)

	var result PickupItemResult
	err := o.items.WithinCharacterItems(ctx, cmd.CharacterID, func(
		inventories dnfrepo.InventoryRepository,
		_ dnfrepo.EquipmentRepository,
	) error {
		record, found, err := inventories.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrInventoryNotFound
		}
		if err := dnfrepo.BindInventoryContainerState(ctx, o.settings, &record); err != nil {
			return err
		}
		record = dnfrepo.CloneInventory(record)
		slot, stack, err := PlacePickupItem(&record, cmd.Placement)
		if err != nil {
			return err
		}
		if cmd.Finalize != nil {
			stack, err = cmd.Finalize(int16(slot), stack)
			if err != nil {
				return err
			}
			record.Slots[pickupSlotKey(cmd.Placement.Definition.ListType, slot)] = cloneStack(stack)
		}
		record.UpdatedAt = now
		if err := dnfrepo.SaveInventoryFields(ctx, inventories, record, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		result = PickupItemResult{Slot: slot, Stack: cloneStack(stack)}
		return nil
	})
	if err != nil {
		return PickupItemResult{}, err
	}
	return result, nil
}

// PlacePickupItem owns the reusable dungeon/PVF placement policy. Current
// client raw-entry construction is supplied by BuildNew/Finalize.
func PlacePickupItem(record *dnfrepo.InventoryRecord, placement PickupItemPlacement) (uint16, dnfrepo.ItemStack, error) {
	definition := placement.Definition
	if record == nil || placement.Amount == 0 || definition.ItemID == 0 ||
		definition.SlotStart < 0 || (definition.ListType == 0 && definition.SlotStart == 0) || definition.SlotEnd < definition.SlotStart ||
		(definition.ListType != 0 && definition.ListType != 38) {
		return 0, dnfrepo.ItemStack{}, ErrPickupItemInvalid
	}
	if definition.Kind != PickupItemStackable && definition.Kind != PickupItemEquipment {
		return 0, dnfrepo.ItemStack{}, ErrPickupItemInvalid
	}
	if definition.Kind == PickupItemEquipment && placement.Amount != 1 {
		return 0, dnfrepo.ItemStack{}, ErrPickupItemInvalid
	}
	if definition.Kind == PickupItemStackable && definition.StackLimit > 0 &&
		int64(placement.Amount) > definition.StackLimit {
		return 0, dnfrepo.ItemStack{}, fmt.Errorf(
			"%w: item=%d amount=%d limit=%d",
			ErrPickupStackLimit,
			definition.ItemID,
			placement.Amount,
			definition.StackLimit,
		)
	}
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	policy := dnfplacement.SlotPolicy{
		ListType: definition.ListType, Start: definition.SlotStart, End: definition.SlotEnd,
		AllowQuickSlots:  definition.AllowQuickSlots,
		PreferQuickSlots: definition.Kind == PickupItemStackable && definition.PreferQuickSlot,
	}
	policy = policy.ForInventory(record)
	for _, slot := range policy.MergeSlots() {
		if destination, stack, ok, err := stackPickupInRange(record, placement, slot, slot); err != nil {
			return 0, dnfrepo.ItemStack{}, err
		} else if ok {
			return destination, stack, nil
		}
	}
	for _, slot := range policy.EmptySlots() {
		if _, occupied := record.Slots[pickupSlotKey(definition.ListType, uint16(slot))]; !occupied {
			return insertPickup(record, placement, slot)
		}
	}
	return 0, dnfrepo.ItemStack{}, fmt.Errorf(
		"%w: item=%d range=%d..%d",
		ErrPickupInventoryFull,
		definition.ItemID,
		definition.SlotStart,
		definition.SlotEnd,
	)
}

func stackPickupInRange(
	record *dnfrepo.InventoryRecord,
	placement PickupItemPlacement,
	start int16,
	end int16,
) (uint16, dnfrepo.ItemStack, bool, error) {
	definition := placement.Definition
	if definition.Kind != PickupItemStackable {
		return 0, dnfrepo.ItemStack{}, false, nil
	}
	for slot := start; slot <= end; slot++ {
		key := pickupSlotKey(definition.ListType, uint16(slot))
		stack, exists := record.Slots[key]
		if !exists || stack.ItemID != int64(definition.ItemID) || stack.Bind || stack.Count < 0 {
			continue
		}
		// 身份判据（dnfrepo.CanMergeItemStack 是仓库层唯一实现）：pvf_path /
		// item_kind / stackable_type / 实例到期时间冲突的行不得并，绑定不同的也不得并。
		// 旧代码只看 item_id + 未绑定 + 上限，于是"同 item 但另一个实例"的行会被并到一起
		// （掉落/任务奖励/点券商品/活动奖励都走这条路径）。
		if !pickupPlacementIdentityCompatible(placement, stack) {
			continue
		}
		if placement.NormalizeExisting != nil {
			var err error
			stack, err = placement.NormalizeExisting(stack)
			if err != nil {
				return 0, dnfrepo.ItemStack{}, false, err
			}
		}
		amount := int64(placement.Amount)
		if stack.Count > math.MaxInt64-amount ||
			(definition.StackLimit > 0 && stack.Count > definition.StackLimit-amount) {
			continue
		}
		stack.Count += amount
		record.Slots[key] = stack
		return uint16(slot), cloneStack(stack), true, nil
	}
	return 0, dnfrepo.ItemStack{}, false, nil
}

// pickupPlacementIdentityCompatible 用仓库层唯一判据判断本次发放能不能并进这一行。
// 没有身份投影（MergeIdentity.ItemID == 0）的旧调用保持原有语义：同 item + 未绑定
// + 上限（上面的循环已判）即可并。
func pickupPlacementIdentityCompatible(placement PickupItemPlacement, existing dnfrepo.ItemStack) bool {
	incoming := placement.MergeIdentity
	if incoming.ItemID == 0 {
		return true
	}
	return dnfrepo.CanMergeItemStack(existing, incoming)
}

func firstEmptyMainSlot(record *dnfrepo.InventoryRecord, start int16, end int16) (int16, bool) {
	for slot := start; slot <= end; slot++ {
		if _, occupied := record.Slots[mainSlotKey(uint16(slot))]; !occupied {
			return slot, true
		}
	}
	return 0, false
}

func insertPickup(
	record *dnfrepo.InventoryRecord,
	placement PickupItemPlacement,
	slot int16,
) (uint16, dnfrepo.ItemStack, error) {
	if placement.BuildNew == nil {
		return 0, dnfrepo.ItemStack{}, ErrPickupItemInvalid
	}
	stack, err := placement.BuildNew(slot)
	if err != nil {
		return 0, dnfrepo.ItemStack{}, err
	}
	if stack.ItemID != int64(placement.Definition.ItemID) ||
		stack.Count != int64(placement.Amount) {
		return 0, dnfrepo.ItemStack{}, ErrPickupItemInvalid
	}
	record.Slots[pickupSlotKey(placement.Definition.ListType, uint16(slot))] = cloneStack(stack)
	return uint16(slot), cloneStack(stack), nil
}

func pickupSlotKey(listType byte, slot uint16) string {
	return fmt.Sprintf("%d:%d", listType, slot)
}
