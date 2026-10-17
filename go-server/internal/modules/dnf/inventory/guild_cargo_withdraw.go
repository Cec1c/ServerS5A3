package inventory

import (
	"context"
	"math"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type GuildCargoSlotRange struct {
	List  byte
	First uint16
	Last  uint16
}

// Native248 does not supply a destination slot or destination item ID.
type GuildCargoWithdrawalRequest struct {
	Owner           r.GuildOwner
	GuildID         uint32
	CargoSlot       uint16
	ItemID          uint32
	Count           uint32
	DestinationList byte
	RequestID       string
	Revalidate      func() bool
}

func (o *Owner) WithdrawGuildCargo(ctx context.Context, req GuildCargoWithdrawalRequest, rules GuildCargoRules, now time.Time) (GuildCargoResult, error) {
	return o.transferGuildCargo(ctx, GuildCargoRequest{
		Owner: req.Owner, GuildID: req.GuildID, Source: GuildCargoLocation{Cargo: true, Slot: req.CargoSlot},
		Destination: GuildCargoLocation{List: req.DestinationList}, SourceItemID: req.ItemID, Count: req.Count, Revalidate: req.Revalidate, RequestID: req.RequestID,
	}, rules, now, guildCargoAllocateWithdrawal)
}

func validGuildCargoInventoryList(list byte) bool { return list == 0 || list == 1 || list == 7 }

// Select a single destination for the entire ACK quantity. Reuse the existing
// stack identity/expiry/PVF-limit predicate and prefer the lowest compatible
// slot, then the lowest empty slot. Never split one ACK across several slots.
func allocateGuildCargoWithdrawal(inv r.InventoryRecord, source r.ItemStack, count uint32, list byte, allowed GuildCargoSlotRange) (GuildCargoLocation, error) {
	if !validGuildCargoInventoryList(list) || allowed.List != list || allowed.First > allowed.Last || allowed.Last > math.MaxInt16 {
		return GuildCargoLocation{}, ErrGuildCargoRequest
	}
	for slot := int(allowed.First); slot <= int(allowed.Last); slot++ {
		if !validGuildCargoLocation(GuildCargoLocation{List: list, Slot: uint16(slot)}) {
			return GuildCargoLocation{}, ErrGuildCargoRequest
		}
	}
	for slot := int(allowed.First); slot <= int(allowed.Last); slot++ {
		candidate := inv.Slots[slotKey(list, int16(slot))]
		if candidate.ItemID > 0 && candidate.ItemID <= math.MaxUint32 && candidate.Count <= math.MaxInt32-int64(count) && !isEquipmentLocked(candidate) && canStack(source, candidate, int64(count)) {
			return GuildCargoLocation{List: list, Slot: uint16(slot)}, nil
		}
	}
	for slot := int(allowed.First); slot <= int(allowed.Last); slot++ {
		candidate := inv.Slots[slotKey(list, int16(slot))]
		if candidate.ItemID == 0 && candidate.Count == 0 {
			return GuildCargoLocation{List: list, Slot: uint16(slot)}, nil
		}
	}
	return GuildCargoLocation{}, ErrGuildCargoInventoryFull
}
