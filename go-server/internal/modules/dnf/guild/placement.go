package guild

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const placementReceiptPrefix = "placement:"

var (
	ErrPlacementItem     = errors.New("guild placement book is unavailable")
	ErrPlacementFull     = errors.New("guild placement area is full")
	ErrPlacementIdentity = errors.New("guild placement request identity does not match")
)

type Placement struct {
	ID          uint32          `json:"id"`
	ItemID      uint32          `json:"item_id"`
	ObjectID    uint32          `json:"object_id"`
	CreatorID   uint32          `json:"creator_id"`
	Area        uint8           `json:"area"`
	X           int16           `json:"x"`
	Y           int16           `json:"y"`
	Maintenance uint32          `json:"maintenance"`
	Cells       []PlacementCell `json:"cells"`
	CreatedAt   time.Time       `json:"created_at"`
	ReadyAt     time.Time       `json:"ready_at"`
}
type PlacementState struct {
	NextID uint32      `json:"next_id"`
	Rows   []Placement `json:"rows,omitempty"`
}
type PlacementReceipt struct {
	Owner     r.GuildOwner `json:"owner"`
	GuildID   uint32       `json:"guild_id"`
	Slot      uint32       `json:"slot"`
	Placement Placement    `json:"placement"`
}
type PlacementRequest struct {
	Owner        r.GuildOwner
	GuildID      uint32
	RequestID    string
	Slot         uint32
	X, Y         int
	Book         PlacementBook
	Map          *PlacementMap
	BuildSeconds uint32
	Revalidate   func() bool
	CanInteract  func(r.CharacterRecord, *Guild) bool
}

func (p Placement) validate() error {
	if p.ID == 0 || p.ItemID == 0 || p.ObjectID == 0 || p.CreatorID == 0 || p.Area > 7 || p.X < 0 || p.Y < 0 || p.Maintenance == 0 || p.Maintenance > 300 || len(p.Cells) == 0 || len(p.Cells) > 1024 || p.CreatedAt.IsZero() || p.ReadyAt.Before(p.CreatedAt) || p.ReadyAt.Unix() > math.MaxInt32 {
		return ErrInvalidRegistry
	}
	seen := map[PlacementCell]bool{}
	for _, c := range p.Cells {
		if seen[c] || c.X < -32767 || c.X > 32767 || c.Y < 0 || c.Y > 32767 {
			return ErrInvalidRegistry
		}
		seen[c] = true
	}
	return nil
}
func (s *PlacementState) Validate() error {
	if s == nil {
		return nil
	}
	if s.NextID == 0 {
		return ErrInvalidRegistry
	}
	seen := map[uint32]bool{}
	occupied := map[[3]int]bool{}
	var counts [8]int
	for _, p := range s.Rows {
		if p.validate() != nil || seen[p.ID] || p.ID >= s.NextID {
			return ErrInvalidRegistry
		}
		seen[p.ID] = true
		counts[p.Area]++
		if counts[p.Area] > 64 {
			return ErrInvalidRegistry
		}
		for _, c := range p.Cells {
			key := [3]int{int(p.Area), c.X, c.Y}
			if occupied[key] {
				return ErrInvalidRegistry
			}
			occupied[key] = true
		}
	}
	return nil
}

func validatePlacementReceipts(reg Registry) error {
	for key, p := range reg.PlacementReceipts {
		id, ok := strings.CutPrefix(key, placementReceiptPrefix)
		if !ok || !validPurchaseID(id) || p.GuildID == 0 || p.Owner.AccountID == "" || p.Owner.CharacterID == "" || p.Slot > 32767 || p.Placement.validate() != nil {
			return ErrInvalidRegistry
		}
		if strconv.FormatUint(uint64(p.Placement.CreatorID), 10) != p.Owner.CharacterID {
			return ErrInvalidRegistry
		}
	}
	return nil
}

// ApplyPlacement consumes the actual inventory book and inserts the guild
// placement in the same transaction. Its durable receipt survives reconnects.
func ApplyPlacement(ctx context.Context, uow r.GuildUnitOfWork, q PlacementRequest, now time.Time) (PlacementReceipt, error) {
	var result PlacementReceipt
	if uow == nil || q.Revalidate == nil || q.CanInteract == nil || now.IsZero() {
		return result, ErrRepositoryUnavailable
	}
	if !validPurchaseID(q.RequestID) || q.GuildID == 0 || q.Slot > 32767 || q.X < 0 || q.X > 32767 || q.Y < 0 || q.Y > 32767 || q.BuildSeconds == 0 || q.BuildSeconds > 86400 {
		return result, ErrPlacementIdentity
	}
	if err := q.Book.Validate(); err != nil {
		return result, err
	}
	cells, err := q.Map.CellsAt(q.Book, q.X, q.Y)
	if err != nil {
		return result, err
	}
	creator, err := strconv.ParseUint(q.Owner.CharacterID, 10, 32)
	if err != nil || creator == 0 {
		return result, ErrCharacterNotFound
	}
	ready := now.Add(time.Duration(q.BuildSeconds) * time.Second)
	if ready.Unix() > math.MaxInt32 {
		return result, ErrPlacementIdentity
	}
	invUOW, ok := uow.(r.GuildInventoryUnitOfWork)
	if !ok {
		return result, r.ErrGuildTransaction
	}
	key := placementReceiptPrefix + q.RequestID
	err = invUOW.WithinGuildInventory(ctx, q.Owner, func(tx r.Group) error {
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.AccountID != q.Owner.AccountID || actor.CharacterID != q.Owner.CharacterID || actor.Stats["delete_flag"] != 0 {
			return ErrCharacterNotFound
		}
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, _, found := reg.GuildForOwner(q.Owner)
		if !found || g.ID != q.GuildID {
			return ErrNotMember
		}
		if !q.CanInteract(actor, g) {
			return ErrPlacementPosition
		}
		if old, replay := reg.PlacementReceipts[key]; replay {
			p := old.Placement
			if old.Owner != q.Owner || old.GuildID != q.GuildID || old.Slot != q.Slot || p.ItemID != q.Book.ItemID || p.Area != q.Map.Area || int(p.X) != q.X || int(p.Y) != q.Y {
				return ErrPlacementIdentity
			}
			if !q.Revalidate() {
				return ErrCharacterNotFound
			}
			result = old
			result.Placement.Cells = append([]PlacementCell(nil), p.Cells...)
			return nil
		}
		inventory, found, err := tx.Inventory.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != q.Owner.CharacterID {
			return ErrPlacementItem
		}
		inventory = r.CloneInventory(inventory)
		slot := fmt.Sprintf("0:%d", q.Slot)
		book, found := inventory.Slots[slot]
		if !found || book.ItemID != int64(q.Book.ItemID) || book.Count < 1 || (!book.ExpireAt.IsZero() && !now.Before(book.ExpireAt)) {
			return ErrPlacementItem
		}
		if g.Placements == nil {
			g.Placements = &PlacementState{NextID: 1}
		}
		state := g.Placements
		if state.NextID == math.MaxUint32 {
			return ErrPlacementFull
		}
		count := 0
		occupied := map[PlacementCell]bool{}
		for _, p := range state.Rows {
			if p.Area != q.Map.Area {
				continue
			}
			count++
			for _, c := range p.Cells {
				occupied[c] = true
			}
		}
		if count >= 64 {
			return ErrPlacementFull
		}
		for _, c := range cells {
			if occupied[c] {
				return ErrPlacementPosition
			}
		}
		book.Count--
		if book.Count == 0 {
			delete(inventory.Slots, slot)
		} else {
			inventory.Slots[slot] = book
		}
		p := Placement{ID: state.NextID, ItemID: q.Book.ItemID, ObjectID: q.Book.ObjectID, CreatorID: uint32(creator), Area: q.Map.Area, X: int16(q.X), Y: int16(q.Y), Maintenance: q.Book.Maintenance, Cells: append([]PlacementCell(nil), cells...), CreatedAt: now, ReadyAt: ready}
		state.NextID++
		state.Rows = append(state.Rows, p)
		g.UpdatedAt = now
		inventory.UpdatedAt = now
		result = PlacementReceipt{Owner: q.Owner, GuildID: g.ID, Slot: q.Slot, Placement: p}
		if reg.PlacementReceipts == nil {
			reg.PlacementReceipts = map[string]PlacementReceipt{}
		}
		reg.PlacementReceipts[key] = result
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		if err = r.SaveInventoryFields(ctx, tx.Inventory, inventory, r.InventoryFieldSlots); err != nil {
			return err
		}
		if err = Save(ctx, tx.Settings, reg, now); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		return nil
	})
	if err != nil {
		return PlacementReceipt{}, err
	}
	return result, nil
}
