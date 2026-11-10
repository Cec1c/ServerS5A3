package wedding

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// ExtensionReceipt binds one native connection request to its exact meaning.
// A reconnect has a fresh server-owned nonce; a successful request cannot debit
// another item or claim another harvest when its ACK is retransmitted.
type ExtensionReceipt struct {
	Session     string
	Sequence    uint64
	Opcode      uint16
	Fingerprint string
}

func extensionReceipt(c *r.CharacterRecord, receipt ExtensionReceipt, store bool) (bool, error) {
	nonce, e := hex.DecodeString(receipt.Session)
	if e != nil || len(nonce) != 32 || receipt.Sequence == 0 || receipt.Sequence > math.MaxInt64 || receipt.Opcode == 0 || receipt.Fingerprint == "" {
		return false, ErrReplay
	}
	h := sha256.Sum256([]byte(receipt.Fingerprint))
	same := true
	for i := 0; i < 4; i++ {
		if c.Stats[statPrefix+"extension_nonce_"+strconv.Itoa(i)] != int64(binary.LittleEndian.Uint64(nonce[i*8:])) {
			same = false
		}
	}
	last := c.Stats[statPrefix+"extension_sequence"]
	if same && last >= int64(receipt.Sequence) {
		if last != int64(receipt.Sequence) || c.Stats[statPrefix+"extension_opcode"] != int64(receipt.Opcode) {
			return false, ErrReplay
		}
		for i := 0; i < 4; i++ {
			if c.Stats[statPrefix+"extension_fingerprint_"+strconv.Itoa(i)] != int64(binary.LittleEndian.Uint64(h[i*8:])) {
				return false, ErrReplay
			}
		}
		return true, nil
	}
	if store {
		if c.Stats == nil {
			c.Stats = map[string]int64{}
		}
		for i := 0; i < 4; i++ {
			c.Stats[statPrefix+"extension_nonce_"+strconv.Itoa(i)] = int64(binary.LittleEndian.Uint64(nonce[i*8:]))
			c.Stats[statPrefix+"extension_fingerprint_"+strconv.Itoa(i)] = int64(binary.LittleEndian.Uint64(h[i*8:]))
		}
		c.Stats[statPrefix+"extension_sequence"], c.Stats[statPrefix+"extension_opcode"] = int64(receipt.Sequence), int64(receipt.Opcode)
	}
	return false, nil
}

func (o Owner) extensionMutation(ctx context.Context, req PairRequest, receipt ExtensionReceipt, apply func(*r.CharacterRecord, *r.CharacterRecord, *r.InventoryRecord, *r.InventoryRecord, *Room, r.Group) error) (PairResult, bool, error) {
	u, ok := o.Repositories.CharacterTrade.(r.WeddingLifecycleUnitOfWork)
	if !ok || o.Rules == nil || req.Now.IsZero() || apply == nil {
		return PairResult{}, false, ErrUnavailable
	}
	ids := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	scope, e := r.WeddingPairScope(ids)
	if e != nil || req.Actor.AccountID == "" || req.Partner.AccountID == "" || req.Actor.AccountID == req.Partner.AccountID {
		return PairResult{}, false, ErrOwnership
	}
	ids, _ = r.CharacterInventoryGroupIDs(ids)
	var result PairResult
	replayed := false
	e = u.WithinWeddingLifecycle(ctx, ids, scope, func(g r.Group) error {
		if g.Character == nil || g.Inventory == nil || g.Settings == nil || g.Quest == nil || g.Mailbox == nil {
			return ErrUnavailable
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrState
		}
		chars := map[string]r.CharacterRecord{}
		bags := map[string]r.InventoryRecord{}
		for _, id := range ids {
			c, found, e := g.Character.Load(ctx, id)
			if e != nil {
				return e
			}
			if !found || c.CharacterID != id {
				return ErrOwnership
			}
			chars[id] = r.CloneCharacter(c)
			bag, found, e := g.Inventory.Load(ctx, id)
			if e != nil {
				return e
			}
			if !found || bag.CharacterID != id {
				return ErrItem
			}
			bags[id] = r.CloneInventory(bag)
		}
		a, b := chars[req.Actor.CharacterID], chars[req.Partner.CharacterID]
		ai, bi := bags[a.CharacterID], bags[b.CharacterID]
		if a.AccountID != req.Actor.AccountID || b.AccountID != req.Partner.AccountID {
			return ErrOwnership
		}
		if e = r.BindInventoryContainerState(ctx, g.Settings, &ai); e != nil {
			return e
		}
		if e = r.BindInventoryContainerState(ctx, g.Settings, &bi); e != nil {
			return e
		}
		bags[a.CharacterID], bags[b.CharacterID] = ai, bi
		x, _, e := ValidatePair(a, b)
		if e != nil || x.Phase != Married {
			return ErrState
		}
		record, _, e := g.Settings.Load(ctx, scope)
		if e != nil {
			return e
		}
		record = r.CloneSettings(record)
		record.Scope = scope
		room, e := decodeRoom(record)
		if e != nil {
			return e
		}
		points, e := o.RoomPoints(room)
		if e != nil || points != x.RoomPoints {
			return ErrState
		}
		if receipt.Opcode != 0 {
			replayed, e = extensionReceipt(&a, receipt, false)
			if e != nil {
				return e
			}
		}
		before := map[string]r.InventoryRecord{a.CharacterID: r.CloneInventory(ai), b.CharacterID: r.CloneInventory(bi)}
		if !replayed {
			if e = apply(&a, &b, &ai, &bi, &room, g); e != nil {
				return e
			}
			if receipt.Opcode != 0 {
				if _, e = extensionReceipt(&a, receipt, true); e != nil {
					return e
				}
			}
			chars[a.CharacterID], chars[b.CharacterID] = a, b
			bags[a.CharacterID], bags[b.CharacterID] = ai, bi
			for _, id := range ids {
				c := chars[id]
				c.UpdatedAt = req.Now
				chars[id] = c
				if e = r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); e != nil {
					return e
				}
				bag := bags[id]
				bag.UpdatedAt = req.Now
				bags[id] = bag
				if e = r.SaveInventoryFields(ctx, g.Inventory, bag, r.InventoryFieldSlots); e != nil {
					return e
				}
			}
			encoded, e := json.Marshal(room)
			if e != nil {
				return e
			}
			if record.Values == nil {
				record.Values = map[string]string{}
			}
			record.Values["room"] = string(encoded)
			record.UpdatedAt = req.Now
			if e = g.Settings.Save(ctx, record); e != nil {
				return e
			}
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrState
		}
		changes := map[string][]string{}
		for _, id := range ids {
			keys := map[string]bool{}
			for key := range before[id].Slots {
				keys[key] = true
			}
			for key := range bags[id].Slots {
				keys[key] = true
			}
			for key := range keys {
				if !reflect.DeepEqual(before[id].Slots[key], bags[id].Slots[key]) {
					changes[id] = append(changes[id], key)
				}
			}
			sort.Strings(changes[id])
		}
		result = PairResult{Actor: chars[a.CharacterID], Partner: chars[b.CharacterID], Inventories: bags, ChangedSlots: changes, Room: &room}
		return nil
	})
	if e != nil {
		return PairResult{}, false, e
	}
	return result, replayed, nil
}
