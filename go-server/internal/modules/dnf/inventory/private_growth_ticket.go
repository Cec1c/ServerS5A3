package inventory

import (
	"bytes"
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"time"
)

var ErrGrowthTicketInvalid = errors.New("growth ticket or target changed, locked, expired or not eligible")

// Uses the existing current-client zero-move acknowledgement contract. The
// native worn-state guard completes the request without attempting a wear.
func GrowthTicketOpenMoveAck(q MoveItemspaceRequest) []byte {
	return buildMoveItemspaceSuccessAck(Command{SourceListType: 0, SourceSlotIndex: q.SourceSlotIndex, DestinationListType: 3, DestinationSlotIndex: q.DestinationSlotIndex}, 0)
}

// Template identity only. Existing retired templates remain readable; runtime
// mode and attribute must be taken from GrowthTicketMode, never this default.
func GrowthTicketType(id int64) (mode string, attribute byte, ok bool) {
	switch id {
	case 990033101:
		return "reinforce", 0, true
	case 990033102:
		return "amplify", 3, true // strength
	case 990033103:
		return "amplify", 4, true // intelligence
	case 990033104:
		return "amplify", 1, true // physical vitality
	case 990033105:
		return "amplify", 2, true // mental spirit
	}
	return "", 0, false
}

// A coupon is trained as reinforcement until a real amplification material
// changes its instance. Books own that mutation and retain their normal rules.
func GrowthTicketMode(stack repo.ItemStack) (mode string, attribute byte, ok bool) {
	if _, _, known := GrowthTicketType(stack.ItemID); !known {
		return "", 0, false
	}
	attr, value := upgradeAmplifyState(stack)
	if attr == 0 && value == 0 {
		return "reinforce", 0, true
	}
	if attr >= 1 && attr <= 4 && value > 0 && value <= 65535 {
		return "amplify", attr, true
	}
	return "", 0, false
}

func InitializeGrowthTicket(stack *repo.ItemStack, _ uint16) error {
	_, _, ok := GrowthTicketType(stack.ItemID)
	if !ok || stack.Count != 1 {
		return ErrGrowthTicketInvalid
	}
	setUpgradeLevel(stack, 0)
	setAmplifyState(stack, 0, 0)
	if stack.Extra == nil {
		stack.Extra = map[string]string{}
	}
	stack.Extra["attach_type"] = "account"
	return nil
}

// A snapshot binds a preview to the entire instance, including lock/expiry and
// packed metadata. Sorting/replacing either row invalidates the confirmation.
func GrowthTicketSameInstance(a, b repo.ItemStack) bool {
	if a.ItemID != b.ItemID || a.Count != b.Count || !a.ExpireAt.Equal(b.ExpireAt) || !bytes.Equal(a.RawEntry, b.RawEntry) || len(a.Extra) != len(b.Extra) {
		return false
	}
	for k, v := range a.Extra {
		if b.Extra[k] != v {
			return false
		}
	}
	return true
}
func GrowthTicketLevel(stack repo.ItemStack) byte { return upgradeLevelOf(stack) }
func GrowthTicketUsable(stack repo.ItemStack, now time.Time) bool {
	_, _, ok := GrowthTicketMode(stack)
	return ok && stack.Count == 1 && !isEquipmentLocked(stack) && upgradeDurabilityOK(stack) &&
		(stack.ExpireAt.IsZero() || stack.ExpireAt.After(now))
}

func GrowthTicketTargetUsable(ticket, target repo.ItemStack, now time.Time) bool {
	mode, _, ok := GrowthTicketMode(ticket)
	_, _, isTicket := GrowthTicketType(target.ItemID)
	attr, _ := upgradeAmplifyState(target)
	return ok && !isTicket && target.Count == 1 && isUpgradeableEquipmentStack(target) && !isEquipmentLocked(target) && upgradeDurabilityOK(target) &&
		(target.ExpireAt.IsZero() || target.ExpireAt.After(now)) && upgradeLevelOf(ticket) > upgradeLevelOf(target) &&
		((mode == "reinforce" && attr == 0) || (mode == "amplify" && attr <= 4))
}

type GrowthTicketApply struct {
	CharacterID            uint16
	TicketSlot, TargetSlot int16
	Ticket, Target         repo.ItemStack
	TargetAllowed          bool   // resolved from the active PVF by the server, never the packet
	TargetAmplifyValue     uint16 // target rarity's initial value, resolved from the active PVF
	Now                    time.Time
}

func (o *Owner) ApplyGrowthTicket(ctx context.Context, q GrowthTicketApply) (repo.ItemStack, error) {
	if o == nil || o.repo == nil {
		return repo.ItemStack{}, ErrOwnerUnavailable
	}
	if q.CharacterID == 0 || q.TicketSlot < 9 || q.TicketSlot > 64 || q.TargetSlot < 9 || q.TargetSlot > 64 || q.TicketSlot == q.TargetSlot || !q.TargetAllowed {
		return repo.ItemStack{}, ErrGrowthTicketInvalid
	}
	character := strconv.Itoa(int(q.CharacterID))
	if !o.inItemTransaction {
		var out repo.ItemStack
		err := o.withinInventoryTransaction(ctx, character, func(tx *Owner) error { var e error; out, e = tx.ApplyGrowthTicket(ctx, q); return e })
		if err != nil {
			return repo.ItemStack{}, err
		}
		return out, nil
	}
	record, found, err := o.repo.Load(ctx, character)
	if err != nil {
		return repo.ItemStack{}, err
	}
	if !found {
		return repo.ItemStack{}, ErrInventoryNotFound
	}
	record = repo.CloneInventory(record)
	tk, dk := slotKey(0, q.TicketSlot), slotKey(0, q.TargetSlot)
	ticket, tok := record.Slots[tk]
	target, dok := record.Slots[dk]
	if !tok || !dok || !GrowthTicketSameInstance(ticket, q.Ticket) || !GrowthTicketSameInstance(target, q.Target) || !GrowthTicketUsable(ticket, q.Now) || !GrowthTicketTargetUsable(ticket, target, q.Now) {
		return repo.ItemStack{}, ErrGrowthTicketInvalid
	}
	if _, _, isTicket := GrowthTicketType(target.ItemID); isTicket {
		return repo.ItemStack{}, ErrGrowthTicketInvalid
	}
	level := upgradeLevelOf(ticket)
	mode, attr, _ := GrowthTicketMode(ticket)
	if level == 0 || level > 31 || level <= upgradeLevelOf(target) || (mode == "amplify" && q.TargetAmplifyValue == 0) {
		return repo.ItemStack{}, ErrGrowthTicketInvalid
	}
	target = cloneStack(target)
	setUpgradeLevel(&target, level)
	// A trained amplification coupon transfers its real instance attribute. A
	// purchased white coupon cannot create an attribute without a real book.
	if mode == "amplify" {
		setAmplifyState(&target, attr, q.TargetAmplifyValue)
	}
	record.Slots[dk] = target
	delete(record.Slots, tk)
	record.UpdatedAt = q.Now
	if err := repo.SaveInventoryFields(ctx, o.repo, record, repo.InventoryFieldSlots); err != nil {
		return repo.ItemStack{}, err
	}
	return target, nil
}
