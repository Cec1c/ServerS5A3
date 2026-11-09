package wedding

import (
	"context"
	"encoding/json"
	"fmt"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type Room struct {
	Version int                    `json:"version"`
	Tree    TreeState              `json:"tree,omitempty"`
	Bag     map[uint16]r.ItemStack `json:"bag"`
	Placed  map[uint16]r.ItemStack `json:"placed"`
}

func decodeRoom(record r.SettingsRecord) (Room, error) {
	room := Room{Version: 1, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}}
	if raw := record.Values["room"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &room); err != nil {
			return Room{}, fmt.Errorf("%w: room decode", ErrState)
		}
	}
	if room.Version != 1 || room.Bag == nil || room.Placed == nil || len(room.Bag) > RoomInventoryCapacity {
		return Room{}, ErrState
	}
	return room, nil
}

func (o Owner) RoomPoints(room Room) (int64, error) {
	if o.Rules == nil {
		return 0, ErrUnavailable
	}
	var points int64
	for slot, stack := range room.Bag {
		if slot >= RoomInventoryCapacity || stack.Count != 1 {
			return 0, ErrItem
		}
		if _, known := o.Rules.Furniture[stack.ItemID]; !known {
			return 0, ErrItem
		}
	}
	for slot, stack := range room.Placed {
		f, known := o.Rules.Furniture[stack.ItemID]
		if !known || stack.Count != 1 || !f.Positions[slot] || f.Points < 0 || points > 0xffffffff-f.Points {
			return 0, ErrItem
		}
		points += f.Points
	}
	return points, nil
}

func (o Owner) ReadRoom(ctx context.Context, a, b r.CharacterRecord) (Room, error) {
	x, _, err := ValidatePair(a, b)
	if err != nil || x.Phase != Married {
		return Room{}, ErrState
	}
	if o.Repositories.Settings == nil {
		return Room{}, ErrUnavailable
	}
	scope, err := r.WeddingPairScope([]string{a.CharacterID, b.CharacterID})
	if err != nil {
		return Room{}, err
	}
	record, _, err := o.Repositories.Settings.Load(ctx, scope)
	if err != nil {
		return Room{}, err
	}
	room, err := decodeRoom(record)
	if err != nil {
		return Room{}, err
	}
	points, err := o.RoomPoints(room)
	if err != nil || points != x.RoomPoints {
		return Room{}, ErrState
	}
	return room, nil
}

// pairInTransaction uses repositories already scoped by WithinWeddingAssets;
// it never starts a nested transaction or falls back to base repositories.
type pairInTransaction struct {
	ids       [2]string
	chars     r.CharacterRepository
	inventory r.InventoryRepository
}

func (u pairInTransaction) WithinCharacterTrade(ctx context.Context, a, b string, f func(r.CharacterRepository, r.InventoryRepository) error) error {
	if a != u.ids[0] || b != u.ids[1] {
		return ErrOwnership
	}
	return f(u.chars, u.inventory)
}

func (o Owner) room(ctx context.Context, req PairRequest, apply func(*r.CharacterRecord, *Room) error) (PairResult, error) {
	u, ok := o.Repositories.CharacterTrade.(r.WeddingAssetUnitOfWork)
	if !ok {
		return PairResult{}, ErrUnavailable
	}
	ids := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	scope, err := r.WeddingPairScope(ids)
	if err != nil {
		return PairResult{}, err
	}
	var result PairResult
	err = u.WithinWeddingAssets(ctx, ids, scope, func(chars r.CharacterRepository, inventories r.InventoryRepository, settings r.SettingsRepository) error {
		record, _, err := settings.Load(ctx, scope)
		if err != nil {
			return err
		}
		record = r.CloneSettings(record)
		record.Scope = scope
		room, err := decodeRoom(record)
		if err != nil {
			return err
		}
		nested := o
		nested.Repositories.CharacterTrade = pairInTransaction{[2]string{ids[0], ids[1]}, chars, inventories}
		result, err = nested.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
			x, y, err := ValidatePair(*a, *b)
			if err != nil || x.Phase != Married {
				return ErrState
			}
			before, err := o.RoomPoints(room)
			if err != nil || before != x.RoomPoints {
				return ErrState
			}
			if err = apply(a, &room); err != nil {
				return err
			}
			points, err := o.RoomPoints(room)
			if err != nil {
				return err
			}
			// Preserve individual balances changed by the purchase callback.
			x, err = Read(*a)
			if err != nil {
				return err
			}
			x.RoomPoints, y.RoomPoints = points, points
			write(a, x)
			write(b, y)
			return nil
		})
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(room)
		if err != nil {
			return err
		}
		if record.Values == nil {
			record.Values = map[string]string{}
		}
		record.Values["room"] = string(encoded)
		record.UpdatedAt = req.Now
		if err = settings.Save(ctx, record); err != nil {
			return err
		}
		result.Room = &room
		return nil
	})
	if err != nil {
		return PairResult{}, err
	}
	return result, nil
}

func DebitLovePoints(c *r.CharacterRecord, count int64) error {
	state, err := Read(*c)
	if err != nil || state.Phase != Married || count <= 0 || state.LovePoints < count {
		return ErrItem
	}
	state.LovePoints -= count
	write(c, state)
	return nil
}

func (o Owner) BuyFurniture(ctx context.Context, req PairRequest, item r.ItemStack) (PairResult, error) {
	if o.Rules == nil || item.Count != 1 || !usableAssetAt(item, req.Now) || !item.ExpireAt.IsZero() {
		return PairResult{}, ErrItem
	}
	f, ok := o.Rules.Furniture[item.ItemID]
	if !ok {
		return PairResult{}, ErrItem
	}
	var slot uint16
	result, err := o.room(ctx, req, func(c *r.CharacterRecord, room *Room) error {
		if c.Stats["gold"] < f.Gold {
			return ErrGold
		}
		found := false
		for i := uint16(0); i < RoomInventoryCapacity; i++ {
			if _, used := room.Bag[i]; !used {
				slot = i
				found = true
				break
			}
		}
		if !found {
			return ErrItem
		}
		if f.Points > 0 {
			if err := DebitLovePoints(c, f.Points); err != nil {
				return err
			}
		}
		c.Stats["gold"] -= f.Gold
		room.Bag[slot] = item
		return nil
	})
	result.PurchasedSlot = slot
	return result, err
}

type RoomMove struct {
	FromBag, ToBag      bool
	FromSlot, ToSlot    uint16
	Count               int64
	SourceItemID        int64
	ExpectedDestination int64
}

func (o Owner) MoveFurniture(ctx context.Context, req PairRequest, q RoomMove) (PairResult, error) {
	if q.Count != 1 || q.SourceItemID <= 0 || q.SourceItemID > 0xffffffff || q.ExpectedDestination < 0 || q.ExpectedDestination > 0xffffffff || q.FromBag == q.ToBag && q.FromSlot == q.ToSlot {
		return PairResult{}, ErrItem
	}
	return o.room(ctx, req, func(_ *r.CharacterRecord, room *Room) error {
		from, to := room.Placed, room.Placed
		if q.FromBag {
			from = room.Bag
			if q.FromSlot >= RoomInventoryCapacity {
				return ErrItem
			}
		}
		if q.ToBag {
			to = room.Bag
			if q.ToSlot >= RoomInventoryCapacity {
				return ErrItem
			}
		}
		stack, found := from[q.FromSlot]
		other, occupied := to[q.ToSlot]
		if !found || stack.ItemID != q.SourceItemID || stack.Count != 1 || !usableAssetAt(stack, req.Now) {
			return ErrItem
		}
		// This room keeps one owned item per inventory row. Swapping an
		// identical occupied bag item would leave the furniture installed
		// while falsely reporting a successful removal.
		if !q.FromBag && q.ToBag && occupied {
			return ErrItem
		}
		// Native cross-container senders write the moving item's ID into
		// both endpoints, including an empty destination. A replacement of
		// installed furniture likewise carries the new item, not the old one.
		installHint := q.FromBag && !q.ToBag && q.ExpectedDestination == stack.ItemID
		if occupied && other.ItemID != q.ExpectedDestination && !installHint || !occupied && q.ExpectedDestination != 0 && q.ExpectedDestination != stack.ItemID {
			return ErrState
		}
		if !q.ToBag && !o.Rules.Furniture[stack.ItemID].Positions[q.ToSlot] {
			return ErrItem
		}
		if occupied {
			if !usableAssetAt(other, req.Now) || !q.FromBag && !o.Rules.Furniture[other.ItemID].Positions[q.FromSlot] {
				return ErrItem
			}
			from[q.FromSlot] = other
		} else {
			delete(from, q.FromSlot)
		}
		to[q.ToSlot] = stack
		return nil
	})
}

func (o Owner) DailyLovePoints(ctx context.Context, req PairRequest, day int64) (PairResult, error) {
	if day <= 0 {
		return PairResult{}, ErrState
	}
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
		x, _, err := ValidatePair(*a, *b)
		if err != nil || x.Phase != Married {
			return ErrState
		}
		if x.LovePoints > o.Rules.MaxLovePoint {
			return ErrState
		}
		if x.LastLoveDay >= day {
			return nil
		}
		x.LastLoveDay = day
		x.LovePoints += o.Rules.DailyLovePoint
		if x.LovePoints > o.Rules.MaxLovePoint {
			x.LovePoints = o.Rules.MaxLovePoint
		}
		write(a, x)
		return nil
	})
}
