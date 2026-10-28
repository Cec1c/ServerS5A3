package guild

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const (
	PlacementRemove       uint8 = 2
	PlacementMove         uint8 = 4
	placementChangePrefix       = "placement_change:"
)

type PlacementChange struct {
	Action   uint8  `json:"action"`
	ID       uint32 `json:"id"`
	ObjectID uint32 `json:"object_id"`
	Area     uint8  `json:"area"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
}

func (c PlacementChange) valid() bool {
	return (c.Action == PlacementRemove || c.Action == PlacementMove) && c.ID != 0 && c.ObjectID != 0 && c.Area < 8 && c.X >= 0 && c.X <= 32767 && c.Y >= 0 && c.Y <= 32767
}

type PlacementChangeReceipt struct {
	Owner     r.GuildOwner    `json:"owner"`
	GuildID   uint32          `json:"guild_id"`
	Change    PlacementChange `json:"change"`
	Placement Placement       `json:"placement"`
}

type PlacementChangeRequest struct {
	Owner       r.GuildOwner
	GuildID     uint32
	RequestID   string
	Change      PlacementChange
	Books       map[uint32]PlacementBook
	Map         *PlacementMap
	Revalidate  func() bool
	CanInteract func(r.CharacterRecord, *Guild) bool
}

func validatePlacementChangeReceipts(reg Registry) error {
	for key, receipt := range reg.PlacementChangeReceipts {
		id, ok := strings.CutPrefix(key, placementChangePrefix)
		p, c := receipt.Placement, receipt.Change
		if !ok || !validPurchaseID(id) || receipt.GuildID == 0 || receipt.Owner.AccountID == "" || receipt.Owner.CharacterID == "" || !c.valid() || p.validate() != nil || p.ID != c.ID || p.ObjectID != c.ObjectID || p.Area != c.Area || int(p.X) != c.X || int(p.Y) != c.Y {
			return ErrInvalidRegistry
		}
	}
	return nil
}

// ApplyPlacementChange preserves the original book debit and construction timer.
// The receipt belongs to the registry transaction so an old move cannot undo a
// later move or resurrect a removed object when the packet is replayed.
func ApplyPlacementChange(ctx context.Context, uow r.GuildUnitOfWork, q PlacementChangeRequest, now time.Time) (PlacementChangeReceipt, error) {
	var result PlacementChangeReceipt
	if uow == nil || q.Revalidate == nil || q.CanInteract == nil || now.IsZero() {
		return result, ErrRepositoryUnavailable
	}
	if !validPurchaseID(q.RequestID) || q.GuildID == 0 || !q.Change.valid() || q.Map == nil || q.Map.Area != q.Change.Area {
		return result, ErrPlacementIdentity
	}
	key := placementChangePrefix + q.RequestID
	err := uow.WithinGuild(ctx, []r.GuildOwner{q.Owner}, func(tx r.Group) error {
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
		g, member, found := reg.GuildForOwner(q.Owner)
		if !found || g.ID != q.GuildID {
			return ErrNotMember
		}
		if !q.CanInteract(actor, g) {
			return ErrPlacementPosition
		}
		if old, ok := reg.PlacementChangeReceipts[key]; ok {
			if old.Owner != q.Owner || old.GuildID != q.GuildID || old.Change != q.Change {
				return ErrPlacementIdentity
			}
			if !q.Revalidate() {
				return ErrCharacterNotFound
			}
			result = old
			result.Placement.Cells = slices.Clone(old.Placement.Cells)
			return nil
		}
		if g.Placements == nil {
			return ErrPlacementIdentity
		}
		index := slices.IndexFunc(g.Placements.Rows, func(p Placement) bool { return p.ID == q.Change.ID })
		if index < 0 {
			return ErrPlacementIdentity
		}
		p := g.Placements.Rows[index]
		if p.ObjectID != q.Change.ObjectID || p.Area != q.Change.Area {
			return ErrPlacementIdentity
		}
		// Native context menu: creator flag or guild permission index 16.
		if strconv.FormatUint(uint64(p.CreatorID), 10) != q.Owner.CharacterID && GradePermissionsFor(g, member.Grade)&(1<<16) == 0 {
			return ErrPermissionDenied
		}
		if q.Change.Action == PlacementMove {
			book, ok := q.Books[p.ItemID]
			if !ok || book.ObjectID != p.ObjectID || book.ItemID != p.ItemID {
				return ErrPlacementCatalog
			}
			cells, err := q.Map.CellsAt(book, q.Change.X, q.Change.Y)
			if err != nil {
				return err
			}
			occupied := map[PlacementCell]bool{}
			for _, other := range g.Placements.Rows {
				if other.ID == p.ID || other.Area != p.Area {
					continue
				}
				for _, cell := range other.Cells {
					occupied[cell] = true
				}
			}
			for _, cell := range cells {
				if occupied[cell] {
					return ErrPlacementPosition
				}
			}
			p.X, p.Y, p.Cells = int16(q.Change.X), int16(q.Change.Y), slices.Clone(cells)
			g.Placements.Rows[index] = p
		} else {
			if int(p.X) != q.Change.X || int(p.Y) != q.Change.Y {
				return ErrPlacementIdentity
			}
			g.Placements.Rows = slices.Delete(g.Placements.Rows, index, index+1)
		}
		result = PlacementChangeReceipt{Owner: q.Owner, GuildID: g.ID, Change: q.Change, Placement: p}
		result.Placement.Cells = slices.Clone(p.Cells)
		if reg.PlacementChangeReceipts == nil {
			reg.PlacementChangeReceipts = map[string]PlacementChangeReceipt{}
		}
		reg.PlacementChangeReceipts[key] = result
		g.UpdatedAt = now
		if !q.Revalidate() {
			return ErrCharacterNotFound
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
		return PlacementChangeReceipt{}, err
	}
	return result, nil
}
