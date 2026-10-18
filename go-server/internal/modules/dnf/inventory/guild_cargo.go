package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	guild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrGuildCargoRequest = errors.New("invalid guild cargo transfer")
var ErrGuildCargoStale = errors.New("guild cargo item no longer matches request")
var ErrGuildCargoInventoryFull = errors.New("guild cargo withdrawal has no compatible inventory slot")

type GuildCargoLocation struct {
	Cargo bool
	List  byte
	Slot  uint16
}

// These fields describe the resolved authoritative operation. Withdrawal's
// destination slot must be allocated by the server, not supplied by the client.
type GuildCargoRequest struct {
	Owner             r.GuildOwner
	GuildID           uint32
	Source            GuildCargoLocation
	Destination       GuildCargoLocation
	SourceItemID      uint32
	DestinationItemID uint32
	Count             uint32
	RequestID         string
	Revalidate        func() bool
}

// Rules are supplied by the trusted runtime/PVF resolver.
// Validate receives copies and must not perform writes or network I/O.
type GuildCargoRules struct {
	MaxCapacity uint32
	Validate    func(r.CharacterRecord, GuildCargoRequest, r.ItemStack, r.ItemStack) error
	// WithdrawalRange derives the allowed range from the locked character and
	// authoritative item/PVF rules. Like Validate, it receives detached copies.
	WithdrawalRange func(r.CharacterRecord, r.ItemStack) (GuildCargoSlotRange, error)
	Placement       func(r.CharacterRecord, r.ItemStack) (itemplacement.SlotPolicy, error)
	Normalize       func(r.ItemStack) (r.ItemStack, error)
}

type GuildCargoResult struct {
	GuildID     uint32
	Moved       uint32
	Remaining   uint32
	Destination GuildCargoLocation
	Replayed    bool
}

// TransferGuildCargo commits character inventory and guild ownership together.
// It never retries an error or an unknown commit outcome. The bridge derives a
// durable request ID from the authenticated connection's extended sequence.
func (o *Owner) TransferGuildCargo(ctx context.Context, req GuildCargoRequest, rules GuildCargoRules, now time.Time) (GuildCargoResult, error) {
	return o.transferGuildCargo(ctx, req, rules, now, guildCargoExactDestination)
}

type guildCargoDestinationMode byte

const (
	guildCargoExactDestination guildCargoDestinationMode = iota
	guildCargoAllocateWithdrawal
	guildCargoResolveDeposit
)

func (o *Owner) transferGuildCargo(ctx context.Context, req GuildCargoRequest, rules GuildCargoRules, now time.Time, mode guildCargoDestinationMode) (GuildCargoResult, error) {
	allocate := mode == guildCargoAllocateWithdrawal
	if o == nil || req.GuildID == 0 || req.SourceItemID == 0 || req.Source == req.Destination ||
		(!req.Source.Cargo && !req.Destination.Cargo) || req.Revalidate == nil || rules.Validate == nil ||
		rules.MaxCapacity == 0 || rules.MaxCapacity > math.MaxUint16+1 || !validGuildCargoLocation(req.Source) ||
		(!allocate && !validGuildCargoLocation(req.Destination)) ||
		(allocate && (!req.Source.Cargo || req.Destination.Cargo || (rules.WithdrawalRange == nil && rules.Placement == nil) || !validGuildCargoInventoryList(req.Destination.List))) ||
		(mode == guildCargoResolveDeposit && (req.Source.Cargo || !req.Destination.Cargo)) {
		return GuildCargoResult{}, ErrGuildCargoRequest
	}
	swap := req.Source.Cargo && req.Destination.Cargo
	if (swap && req.Count != 0) || (!swap && (req.Count == 0 || req.Count > math.MaxInt32)) {
		return GuildCargoResult{}, ErrGuildCargoRequest
	}
	uow, ok := o.guild.(r.GuildInventoryUnitOfWork)
	if !ok {
		return GuildCargoResult{}, r.ErrGuildTransaction
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result GuildCargoResult
	var fingerprint string
	if req.RequestID != "" {
		id, err := hex.DecodeString(req.RequestID)
		if err != nil || len(id) != 32 {
			return result, ErrGuildCargoRequest
		}
		fingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q|%q|%d|%+v|%+v|%d|%d|%d|%d", req.Owner.AccountID, req.Owner.CharacterID, req.GuildID, req.Source, req.Destination, req.SourceItemID, req.DestinationItemID, req.Count, mode))))
	}
	err := uow.WithinGuildInventory(ctx, req.Owner, func(tx r.Group) error {
		if !req.Revalidate() {
			return guild.ErrPermissionDenied
		}
		character, found, err := tx.Character.Load(ctx, req.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != req.Owner.CharacterID || character.AccountID != req.Owner.AccountID {
			return guild.ErrPermissionDenied
		}
		registry, err := guild.Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, member, found := registry.GuildForCharacter(req.Owner.CharacterID)
		if !found || g == nil || g.ID != req.GuildID || member.AccountID != req.Owner.AccountID {
			return guild.ErrNotMember
		}
		if guild.GradePermissionsFor(g, member.Grade)&guild.CargoPermission == 0 {
			return guild.ErrPermissionDenied
		}
		if g.Cargo == nil {
			return guild.ErrCargoUninitialized
		}
		if receipt, found := g.Cargo.Transfers[req.RequestID]; req.RequestID != "" && found {
			if receipt.Fingerprint != fingerprint {
				return ErrGuildCargoStale
			}
			if !req.Revalidate() {
				return guild.ErrPermissionDenied
			}
			result = GuildCargoResult{GuildID: g.ID, Moved: receipt.Moved, Remaining: receipt.Remaining, Destination: GuildCargoLocation{Cargo: receipt.Cargo, List: receipt.List, Slot: receipt.Slot}, Replayed: true}
			return nil
		}
		if g.Cargo.Capacity > rules.MaxCapacity ||
			(req.Source.Cargo && uint32(req.Source.Slot) >= g.Cargo.Capacity) ||
			(req.Destination.Cargo && uint32(req.Destination.Slot) >= g.Cargo.Capacity) {
			return ErrGuildCargoRequest
		}
		inv, found, err := tx.Inventory.Load(ctx, req.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrInventoryNotFound
		}
		inv = r.CloneInventory(inv)
		source := guildCargoStack(inv, g.Cargo, req.Source)
		if source.ItemID != int64(req.SourceItemID) || source.Count <= 0 || source.Count > math.MaxInt32 {
			return ErrGuildCargoStale
		}
		if isEquipmentLocked(source) {
			return ErrItemLocked
		}
		if rules.Normalize != nil {
			source, err = normalizeGuildCargoStack(source, rules.Normalize)
			if err != nil {
				return err
			}
		}
		var placement itemplacement.SlotPolicy
		if rules.Placement != nil {
			placement, err = rules.Placement(r.CloneCharacter(character), cloneStack(source))
			if err != nil {
				return err
			}
			placement = placement.ForInventory(&inv)
			if !req.Source.Cargo && !placement.Contains(req.Source.List, int16(req.Source.Slot)) {
				return ErrGuildCargoRequest
			}
		}
		if !swap {
			count := int64(req.Count)
			if count > source.Count || (!isStackable(source) && (source.Count != 1 || count != 1)) {
				return ErrGuildCargoRequest
			}
		}
		if allocate {
			var location GuildCargoLocation
			if rules.Placement != nil {
				location, err = allocateGuildCargoPolicy(inv, source, req.Count, req.Destination.List, placement, rules.Normalize)
			} else {
				allowed, err := rules.WithdrawalRange(r.CloneCharacter(character), cloneStack(source))
				if err != nil {
					return err
				}
				location, err = allocateGuildCargoWithdrawal(inv, source, req.Count, req.Destination.List, allowed)
				if err != nil {
					return err
				}
			}
			if err != nil {
				return err
			}
			req.Destination = location
			req.DestinationItemID = uint32(guildCargoStack(inv, g.Cargo, location).ItemID)
		}
		dest := guildCargoStack(inv, g.Cargo, req.Destination)
		if mode == guildCargoResolveDeposit {
			// Native247 names a cargo slot but not its current item ID. The
			// registry read under this transaction supplies that identity.
			req.DestinationItemID = uint32(dest.ItemID)
		}
		if dest.ItemID != int64(req.DestinationItemID) ||
			(dest.ItemID != 0 && (dest.Count <= 0 || dest.Count > math.MaxInt32)) {
			return ErrGuildCargoStale
		}
		if dest.ItemID != 0 && isEquipmentLocked(dest) {
			return ErrItemLocked
		}
		if dest.ItemID != 0 && rules.Normalize != nil {
			dest, err = normalizeGuildCargoStack(dest, rules.Normalize)
			if err != nil {
				return err
			}
		}
		if rules.Placement != nil && !req.Destination.Cargo && !placement.Contains(req.Destination.List, int16(req.Destination.Slot)) {
			return ErrGuildCargoRequest
		}
		if err = rules.Validate(r.CloneCharacter(character), req, cloneStack(source), cloneStack(dest)); err != nil {
			return err
		}
		if swap {
			putGuildCargoStack(&inv, g.Cargo, req.Source, dest)
			putGuildCargoStack(&inv, g.Cargo, req.Destination, source)
			result = GuildCargoResult{GuildID: g.ID, Moved: uint32(source.Count), Remaining: uint32(dest.Count), Destination: req.Destination}
		} else {
			count := int64(req.Count)
			historyItem := cloneStack(source)
			historyItem.Count = count
			if isStackable(historyItem) {
				updateStackRawAmount(&historyItem)
			}
			if dest.ItemID != 0 {
				if dest.Count > math.MaxInt32-count || !canStack(source, dest, count) {
					return ErrGuildCargoRequest
				}
				preserveMergedStackLimit(&dest, source)
				dest.Count += count
			} else {
				dest = cloneStack(source)
				dest.Count = count
			}
			source.Count -= count
			if isStackable(dest) {
				updateStackRawAmount(&dest)
				updateStackRawAmount(&source)
			}
			putGuildCargoStack(&inv, g.Cargo, req.Source, source)
			putGuildCargoStack(&inv, g.Cargo, req.Destination, dest)
			kind := guild.CargoHistoryDeposit
			if req.Source.Cargo {
				kind = guild.CargoHistoryWithdraw
			}
			if err = g.Cargo.AppendHistory(guild.CargoHistoryEntry{At: now, CharacterID: character.CharacterID, CharacterName: character.Name, Kind: kind, Item: historyItem}); err != nil {
				return err
			}
			result = GuildCargoResult{GuildID: g.ID, Moved: req.Count, Remaining: uint32(source.Count), Destination: req.Destination}
		}
		if req.RequestID != "" {
			if g.Cargo.Transfers == nil {
				g.Cargo.Transfers = map[string]guild.CargoTransferReceipt{}
			}
			g.Cargo.Transfers[req.RequestID] = guild.CargoTransferReceipt{Fingerprint: fingerprint, Moved: result.Moved, Remaining: result.Remaining, Cargo: result.Destination.Cargo, List: result.Destination.List, Slot: result.Destination.Slot}
		}
		inv.UpdatedAt, g.UpdatedAt = now, now
		if !swap {
			if err = r.SaveInventoryFields(ctx, tx.Inventory, inv, r.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if err = guild.Save(ctx, tx.Settings, registry, now); err != nil {
			return err
		}
		if !req.Revalidate() {
			return guild.ErrPermissionDenied
		}
		return nil
	})
	if err != nil {
		return GuildCargoResult{}, err
	}
	return result, nil
}

func validGuildCargoLocation(p GuildCargoLocation) bool {
	if p.Cargo {
		return p.List == 0
	}
	return (p.List == 0 || p.List == 1 || p.List == 7) && p.Slot <= math.MaxInt16 &&
		!r.IsAccountSharedInventorySlot(p.List, int16(p.Slot)) && !(p.List == 0 && p.Slot == 0)
}

func guildCargoStack(inv r.InventoryRecord, cargo *guild.Cargo, p GuildCargoLocation) r.ItemStack {
	if p.Cargo {
		return cloneStack(cargo.Slots[p.Slot])
	}
	return cloneStack(inv.Slots[slotKey(p.List, int16(p.Slot))])
}

func putGuildCargoStack(inv *r.InventoryRecord, cargo *guild.Cargo, p GuildCargoLocation, stack r.ItemStack) {
	if p.Cargo {
		if cargo.Slots == nil {
			cargo.Slots = map[uint16]r.ItemStack{}
		}
		if stack.ItemID == 0 || stack.Count == 0 {
			delete(cargo.Slots, p.Slot)
		} else {
			cargo.Slots[p.Slot] = cloneStack(stack)
		}
		return
	}
	if inv.Slots == nil {
		inv.Slots = map[string]r.ItemStack{}
	}
	key := slotKey(p.List, int16(p.Slot))
	if stack.ItemID == 0 || stack.Count == 0 {
		delete(inv.Slots, key)
	} else {
		inv.Slots[key] = cloneStack(stack)
	}
}
