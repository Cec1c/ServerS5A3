package wedding

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type Owner struct {
	Repositories r.Group
	Rules        *Rules
}
type PairResult struct {
	Room           *Room
	PurchasedSlot  uint16
	Actor, Partner r.CharacterRecord
	Inventories    map[string]r.InventoryRecord
	ChangedSlots   map[string][]string
}
type PairRequest struct {
	CeremonyRoute  *CeremonyRoute
	Actor, Partner Actor
	Now            time.Time
	Revalidate     func() bool
}
type AssetGrant func(*r.InventoryRecord, int64, int64, time.Time) error

func (o Owner) pair(ctx context.Context, req PairRequest, apply func(*r.CharacterRecord, *r.CharacterRecord, *r.InventoryRecord, *r.InventoryRecord) error) (PairResult, error) {
	if o.Rules == nil || o.Repositories.CharacterTrade == nil || apply == nil || req.Now.IsZero() {
		return PairResult{}, ErrUnavailable
	}
	if req.Actor.AccountID == "" || req.Partner.AccountID == "" || req.Actor.AccountID == req.Partner.AccountID {
		return PairResult{}, ErrOwnership
	}
	if _, err := NumericID(req.Actor.CharacterID); err != nil {
		return PairResult{}, err
	}
	if _, err := NumericID(req.Partner.CharacterID); err != nil {
		return PairResult{}, err
	}
	if req.Actor.CharacterID == req.Partner.CharacterID {
		return PairResult{}, ErrOwnership
	}
	var result PairResult
	err := o.Repositories.CharacterTrade.WithinCharacterTrade(ctx, req.Actor.CharacterID, req.Partner.CharacterID, func(chars r.CharacterRepository, inventories r.InventoryRepository) error {
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrState
		}
		ids := []string{req.Actor.CharacterID, req.Partner.CharacterID}
		sort.Strings(ids)
		c := map[string]r.CharacterRecord{}
		inv := map[string]r.InventoryRecord{}
		for _, id := range ids {
			v, ok, e := chars.Load(ctx, id)
			if e != nil {
				return e
			}
			if !ok || v.CharacterID != id {
				return ErrOwnership
			}
			c[id] = r.CloneCharacter(v)
		}
		a, b := c[req.Actor.CharacterID], c[req.Partner.CharacterID]
		if a.AccountID != req.Actor.AccountID || b.AccountID != req.Partner.AccountID {
			return ErrOwnership
		}
		// A selection-screen deletion can win before an already authorized
		// engagement acquires these locks. Never bind a tombstoned character.
		if a.Slot < 0 || b.Slot < 0 || a.Stats["delete_flag"] != 0 || b.Stats["delete_flag"] != 0 {
			return ErrOwnership
		}
		for _, id := range ids {
			v, ok, e := inventories.Load(ctx, id)
			if e != nil {
				return e
			}
			if !ok || v.CharacterID != id {
				return ErrItem
			}
			inv[id] = r.CloneInventory(v)
		}
		ai, bi := inv[a.CharacterID], inv[b.CharacterID]
		before := map[string]r.InventoryRecord{a.CharacterID: r.CloneInventory(ai), b.CharacterID: r.CloneInventory(bi)}
		if err := apply(&a, &b, &ai, &bi); err != nil {
			return err
		}
		c[a.CharacterID], c[b.CharacterID] = a, b
		inv[a.CharacterID], inv[b.CharacterID] = ai, bi
		for _, id := range ids {
			v := c[id]
			v.UpdatedAt = req.Now
			if err := r.SaveCharacterFields(ctx, chars, v, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
				return err
			}
		}
		for _, id := range ids {
			v := inv[id]
			v.UpdatedAt = req.Now
			if err := r.SaveInventoryFields(ctx, inventories, v, r.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrState
		}
		changes := make(map[string][]string)
		for _, id := range ids {
			keys := map[string]bool{}
			for key := range before[id].Slots {
				keys[key] = true
			}
			for key := range inv[id].Slots {
				keys[key] = true
			}
			for key := range keys {
				if !reflect.DeepEqual(before[id].Slots[key], inv[id].Slots[key]) {
					changes[id] = append(changes[id], key)
				}
			}
			sort.Strings(changes[id])
		}
		result = PairResult{Actor: a, Partner: b, Inventories: inv, ChangedSlots: changes}
		return nil
	})
	if err != nil {
		return PairResult{}, err
	}
	return result, nil
}

func (o Owner) Engage(ctx context.Context, req PairRequest) (PairResult, error) {
	return o.withRelationshipTransaction(ctx, req, func(inner Owner, g r.Group) (PairResult, error) {
		return inner.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
			if err := eligible(*a, *b, o.Rules); err != nil {
				return err
			}
			x, e := Read(*a)
			if e != nil {
				return e
			}
			y, e := Read(*b)
			if e != nil {
				return e
			}
			if x.Phase != Single || y.Phase != Single || x.CooldownUntil > req.Now.Unix() || y.CooldownUntil > req.Now.Unix() {
				return ErrState
			}
			x.PartnerID, _ = NumericID(b.CharacterID)
			y.PartnerID, _ = NumericID(a.CharacterID)
			x.Phase, y.Phase = Engaged, Engaged
			token, err := AllocateWeddingToken(ctx, g.Settings, req.Now)
			if err != nil {
				return err
			}
			x.RelationToken, y.RelationToken = int64(token), int64(token)
			write(a, x)
			write(b, y)
			return nil
		})
	})
}

func completed(record r.QuestRecord, id int64) bool {
	for _, states := range []map[int64]r.QuestState{record.States, record.Progress} {
		switch strings.ToLower(strings.TrimSpace(states[id].Status)) {
		case "complete", "completed", "cleared", "finished", "done":
			return true
		}
	}
	return false
}

func (o Owner) preparationComplete(ctx context.Context, id string) error {
	if o.Repositories.Quest == nil {
		return ErrUnavailable
	}
	q, ok, err := o.Repositories.Quest.Load(ctx, id)
	if err != nil {
		return err
	}
	if !ok || q.CharacterID != id {
		return ErrQuest
	}
	for _, qid := range o.Rules.QuestIDs {
		if !completed(q, qid) {
			return ErrQuest
		}
	}
	return nil
}

func (o Owner) Reserve(ctx context.Context, req PairRequest, kind, duration byte, day int64) (PairResult, error) {
	if o.Rules == nil || int(duration) >= len(o.Rules.Durations) || day <= 0 || req.CeremonyRoute == nil || !req.CeremonyRoute.Valid() {
		return PairResult{}, ErrState
	}
	ceremony, ok := o.Rules.Ceremonies[kind]
	if !ok {
		return PairResult{}, ErrState
	}
	if req.Now.Unix() < 0 || req.Now.Unix() > 0xffffffff || o.Rules.Durations[duration] > 0xffffffff-req.Now.Unix() {
		return PairResult{}, ErrState
	}
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, ai, _ *r.InventoryRecord) error {
		x, y, err := ValidatePair(*a, *b)
		if err != nil {
			return err
		}
		if x.Phase != Engaged || x.LastWeddingDay >= day || y.LastWeddingDay >= day {
			return ErrState
		}
		if err = eligible(*a, *b, o.Rules); err != nil {
			return err
		}
		if err = o.preparationComplete(ctx, a.CharacterID); err != nil {
			return err
		}
		if err = o.preparationComplete(ctx, b.CharacterID); err != nil {
			return err
		}
		if a.Stats["gold"] < ceremony.Gold {
			return ErrGold
		}
		if ceremony.TicketID != 0 {
			if err = consume(ai, ceremony.TicketID, ceremony.TicketCount, "", req.Now); err != nil {
				return err
			}
		}
		a.Stats["gold"] -= ceremony.Gold
		x.Phase, y.Phase = Reserved, Reserved
		x.Kind, y.Kind = kind, kind
		x.CeremonyAt = req.Now.Unix() + o.Rules.Durations[duration]
		y.CeremonyAt = x.CeremonyAt
		x.LastWeddingDay, y.LastWeddingDay = day, day
		x.CeremonyChannel, y.CeremonyChannel = int64(req.CeremonyRoute.Channel), int64(req.CeremonyRoute.Channel)
		x.CeremonyArea, y.CeremonyArea = int64(req.CeremonyRoute.Area), int64(req.CeremonyRoute.Area)
		write(a, x)
		write(b, y)
		return nil
	})
}

func (o Owner) UpgradeRing(ctx context.Context, req PairRequest, stage int, slot string) (PairResult, error) {
	if o.Rules == nil || stage <= 0 || stage >= len(o.Rules.Rings) {
		return PairResult{}, ErrState
	}
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, ai, _ *r.InventoryRecord) error {
		x, y, e := ValidatePair(*a, *b)
		if e != nil {
			return e
		}
		if x.Phase != Married || stage != x.Ring+1 {
			return ErrState
		}
		t := o.Rules.Rings[stage]
		if e = consume(ai, t.TicketID, t.TicketCount, slot, req.Now); e != nil {
			return e
		}
		x.Ring, y.Ring = stage, stage
		write(a, x)
		write(b, y)
		return nil
	})
}

func consume(record *r.InventoryRecord, id, count int64, onlySlot string, now time.Time) error {
	if count <= 0 || id <= 0 {
		return ErrItem
	}
	keys := []string{}
	for key, stack := range record.Slots {
		if stack.ItemID == id && strings.HasPrefix(key, strconv.Itoa(int(r.MainInventoryListType))+":") && (onlySlot == "" || key == onlySlot) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		v := record.Slots[key]
		_, slotText, _ := strings.Cut(key, ":")
		slot, err := strconv.Atoi(slotText)
		if err != nil || slot < 3 || slot > 288 || itemlock.IsLockedAt(v, now) || !v.ExpireAt.IsZero() && !now.Before(v.ExpireAt) {
			continue
		}
		expired := false
		for _, k := range []string{"expire_time", "expire_unix"} {
			if n, e := strconv.ParseInt(v.Extra[k], 10, 64); e == nil && n > 0 && n <= now.Unix() {
				expired = true
			}
		}
		if expired || v.Count <= 0 {
			continue
		}
		take := v.Count
		if take > count {
			take = count
		}
		v.Count -= take
		count -= take
		if v.Count == 0 {
			delete(record.Slots, key)
		} else {
			v.RawEntry = nil
			if v.Extra != nil {
				delete(v.Extra, "raw_entry_hex")
				delete(v.Extra, "current_raw_entry_77")
				delete(v.Extra, "raw_entry_77")
			}
			record.Slots[key] = v
		}
		if count == 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: item %d", ErrItem, id)
}
