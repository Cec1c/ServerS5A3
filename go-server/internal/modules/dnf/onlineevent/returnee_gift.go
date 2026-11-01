package onlineevent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"path"
	"strconv"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrNPCGiftLevel = errors.New("NPC gift level requirement")
	ErrNPCGiftItem  = errors.New("NPC gift item not allowed")
	ErrNPCGiftCount = errors.New("NPC gift item count insufficient")
	ErrNPCGiftLimit = errors.New("NPC daily gift limit")
)

type ReturneeGiftRules struct {
	NPC                     uint32
	Level                   int64
	Count, Limit, MaxPoints int64
	Points                  map[int64][2]int64
}

func LoadReturneeGiftRules(source dnfpvf.Source, npc uint32) (ReturneeGiftRules, error) {
	r := ReturneeGiftRules{NPC: npc, Points: map[int64][2]int64{}}
	read := func(p string) (*dnfpvf.Document, error) {
		s, e := source.ReadText(p)
		if e != nil {
			return nil, e
		}
		return dnfpvf.Parse(p, s)
	}
	d, e := read("etc/npcfavorsystem.etc")
	if e != nil {
		return r, e
	}
	for name, dst := range map[string]*int64{"favor condition level": &r.Level, "favor gift item count": &r.Count, "favor gift limit": &r.Limit} {
		v, ok := d.Section(name)
		if !ok || len(v) != 1 || v[0].Kind != dnfpvf.TokenInt || v[0].Int <= 0 {
			return r, fmt.Errorf("invalid NPC favor %s", name)
		}
		*dst = v[0].Int
	}
	v, ok := d.Section("favor level point up")
	if !ok || len(v)%3 != 0 {
		return r, fmt.Errorf("invalid NPC gift point rows")
	}
	for i := 0; i < len(v); i += 3 {
		if v[i].Int <= 0 || v[i+1].Int <= 0 || v[i+2].Int < v[i+1].Int {
			return r, fmt.Errorf("invalid NPC gift points")
		}
		r.Points[v[i].Int] = [2]int64{v[i+1].Int, v[i+2].Int}
	}
	list, e := read("npc/npc.lst")
	if e != nil {
		return r, e
	}
	for _, entry := range dnfpvf.ParseList(list) {
		if entry.ID != int64(npc) {
			continue
		}
		if path.IsAbs(entry.Path) || path.Clean(entry.Path) == ".." {
			return r, fmt.Errorf("invalid NPC path")
		}
		d, e = read(path.Join("npc", entry.Path))
		if e != nil {
			return r, e
		}
		if _, ok := d.Section("favorable relationship 2"); !ok {
			break
		}
		v, ok := d.Section("favor level point")
		if !ok || len(v) != 3 {
			break
		}
		r.MaxPoints = v[2].Int
		break
	}
	if r.MaxPoints <= 0 || r.MaxPoints > math.MaxInt32 || r.Count > math.MaxUint16 || r.Limit > math.MaxUint8 {
		return r, fmt.Errorf("NPC %d has no supported favor rules", npc)
	}
	return r, nil
}

type ReturneeGiftResult struct {
	Points, Used, Remaining int64
	Added                   int64
	Stack                   dnfrepo.ItemStack
}

// Native op812 gift consumes the configured cube amount. Inventory, affection,
// daily per-NPC limit and the welcome mission counter share one transaction.
func GiveReturneeNPCGift(ctx context.Context, repos dnfrepo.Group, r ReturneeGiftRules, accountID, characterID string, container byte, slot int16, now time.Time) (ReturneeGiftResult, error) {
	var result ReturneeGiftResult
	if repos.AccountAssets == nil || r.Count <= 0 || r.MaxPoints <= 0 || now.IsZero() {
		return result, ErrOwnerUnavailable
	}
	warehouse := slot >= dnfrepo.CrystalWarehouseFirstSlot && slot <= dnfrepo.CrystalWarehouseLastSlot
	if (container != 0 && container != 36) || slot < 0 || (container == 36 && !warehouse) || (!warehouse && slot >= dnfrepo.CrystalWarehouseFirstSlot) {
		return result, ErrNPCGiftItem
	}
	e := repos.AccountAssets.WithinAccountCharacterAssets(ctx, accountID, characterID, func(accounts dnfrepo.AccountInventoryRepository, characters dnfrepo.CharacterRepository, items dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		ch, ok, e := characters.Load(ctx, characterID)
		if e != nil {
			return e
		}
		if !ok || ch.AccountID != accountID {
			return fmt.Errorf("NPC gift character requirement")
		}
		if int64(ch.Level) < r.Level {
			return ErrNPCGiftLevel
		}
		var account dnfrepo.AccountInventoryRecord
		var inventory dnfrepo.InventoryRecord
		var slots map[string]dnfrepo.ItemStack
		if warehouse {
			account, _, e = accounts.Load(ctx, accountID)
			account = dnfrepo.CloneAccountInventory(account)
			slots = account.Slots
		} else {
			inventory, _, e = items.Load(ctx, characterID)
			inventory = dnfrepo.CloneInventory(inventory)
			slots = inventory.Slots
		}
		if e != nil {
			return e
		}
		key := dnfrepo.AccountSharedInventorySlotKey(slot)
		stack, ok := slots[key]
		points, allowed := r.Points[stack.ItemID]
		if !ok || !allowed || points[0] <= 0 || points[1] < points[0] {
			return ErrNPCGiftItem
		}
		if !stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt) {
			return ErrNPCGiftItem
		}
		if stack.Count < r.Count {
			return ErrNPCGiftCount
		}
		prefix := "npc_favor_" + strconv.FormatUint(uint64(r.NPC), 10) + "_"
		day := now.In(time.FixedZone("CST", 8*3600)).Add(-6 * time.Hour).Format("20060102")
		dayID, _ := strconv.ParseInt(day, 10, 64)
		ch = dnfrepo.CloneCharacter(ch)
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		if ch.Stats[prefix+"day"] != dayID {
			ch.Stats[prefix+"day"] = dayID
			ch.Stats[prefix+"gifts"] = 0
		}
		if ch.Stats[prefix+"gifts"] >= r.Limit {
			return ErrNPCGiftLimit
		}
		ch.Stats[prefix+"gifts"]++
		before := min(max(ch.Stats[prefix+"points"], 0), r.MaxPoints)
		ch.Stats[prefix+"points"] = min(before+points[0]+rand.Int64N(points[1]-points[0]+1), r.MaxPoints)
		if ch.Stats["is_return_user"] != 0 && ch.Stats["seria_returnee_active_until"] > now.Unix() {
			ch.Stats[ReturneeGiftCountKey] = min(max(ch.Stats[ReturneeGiftCountKey], 0), math.MaxInt64-1) + 1
		}
		stack.Count -= r.Count
		stack.RawEntry = nil
		slots[key] = stack
		if !warehouse && stack.Count == 0 {
			delete(slots, key)
		}
		dnfrepo.AddBattlePassCounter(&ch, 25, -1, 1, now)
		ch.UpdatedAt = now
		if warehouse {
			account.UpdatedAt = now
			e = accounts.Save(ctx, account)
		} else {
			inventory.UpdatedAt = now
			e = dnfrepo.SaveInventoryFields(ctx, items, inventory, dnfrepo.InventoryFieldSlots)
		}
		if e != nil {
			return e
		}
		if e := dnfrepo.SaveCharacterFields(ctx, characters, ch, dnfrepo.CharacterFieldStats); e != nil {
			return e
		}
		result = ReturneeGiftResult{Points: ch.Stats[prefix+"points"], Added: ch.Stats[prefix+"points"] - before, Used: ch.Stats[prefix+"gifts"], Remaining: stack.Count, Stack: stack}
		return nil
	})
	return result, e
}
