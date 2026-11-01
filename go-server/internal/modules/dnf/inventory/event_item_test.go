package inventory

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func eventItemFixture(t *testing.T, kind EventItemKind) (repo.Group, EventItemRequest, time.Time) {
	t.Helper()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	raw := make([]byte, currentItemListEntrySize)
	binary.LittleEndian.PutUint32(raw[2:6], 200)
	binary.LittleEndian.PutUint32(raw[6:10], 987654321) // quality
	raw[10], raw[0x59] = 12, 7                          // reinforcement and independent instance state
	binary.LittleEndian.PutUint32(raw[0x0e:0x12], 12345)
	raw[0x12] = 3
	binary.LittleEndian.PutUint32(raw[0x38:0x3c], uint32(now.Add(time.Hour).Unix()))
	for _, err := range []error{
		g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}),
		g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Level: 90}),
		g.Settings.Save(ctx, repo.SettingsRecord{Scope: repo.CharacterContainerStateScope("1"), Values: map[string]string{"main_list_param16": "0", "avatar_list_param16": "0", "personal_cargo_list_param16": "8", "keep": "yes"}}),
		g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:70": {ItemID: 100, Count: 1}, "0:10": {ItemID: 200, Count: 1, Bind: true, ExpireAt: now.Add(time.Hour), RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/temporary.equ", "upgrade_level": "12", "forge_level": "7", "enchant_card_id": "12345", "raw_entry_hex": hex.EncodeToString(raw)}}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	r := EventItemRequest{AccountID: "a", CharacterID: "1", SourceSlot: 70, TargetSlot: 10, Kind: kind, Revalidate: func() bool { return true }, Resolve: func(_ repo.CharacterRecord, source, target repo.ItemStack) (EventItemPolicy, repo.ItemStack, repo.ItemStack, error) {
		if source.ItemID != 100 || (kind == EventPermanentWeapon && target.ItemID != 200) {
			return EventItemPolicy{}, source, target, ErrEventItem
		}
		return EventItemPolicy{Kind: kind, SourceSlots: itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 120}, TargetSlots: itemplacement.SlotPolicy{ListType: 0, Start: 9, End: 64}, MinimumLevel: 1, OutputItemID: 201, OutputPVFPath: "equipment/permanent.equ"}, source, target, nil
	}}
	return g, r, now
}

func eventItemSnapshot(t *testing.T, g repo.Group) []any {
	t.Helper()
	ctx := context.Background()
	i, _, e := g.Inventory.Load(ctx, "1")
	if e != nil {
		t.Fatal(e)
	}
	s, _, e := g.Settings.Load(ctx, repo.CharacterContainerStateScope("1"))
	if e != nil {
		t.Fatal(e)
	}
	c, _, e := g.Character.Load(ctx, "1")
	if e != nil {
		t.Fatal(e)
	}
	return []any{i, s, c}
}

func TestEventItemAtomicEffectsAndReplay(t *testing.T) {
	for _, kind := range []EventItemKind{EventInventoryExpansion, EventSilverCargo, EventPermanentWeapon} {
		t.Run(string(rune('0'+kind)), func(t *testing.T) {
			g, req, now := eventItemFixture(t, kind)
			before, _, _ := g.Inventory.Load(context.Background(), "1")
			got, err := ApplyEventItem(context.Background(), g, req, now)
			if err != nil || got.Remaining != 0 {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			after, _, _ := g.Inventory.Load(context.Background(), "1")
			if _, exists := after.Slots["0:70"]; exists {
				t.Fatal("source remains")
			}
			s, _, err := repo.LoadCharacterContainerState(context.Background(), g.Settings, "1")
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case EventInventoryExpansion:
				if s.MainSlotCount != 8 || s.PersonalCargoSlotCount != 8 {
					t.Fatalf("%+v", s)
				}
			case EventSilverCargo:
				if s.PersonalCargoSlotCount != 40 || s.MainSlotCount != 0 {
					t.Fatalf("%+v", s)
				}
			case EventPermanentWeapon:
				w := after.Slots["0:10"]
				if w.ItemID != 201 || !w.ExpireAt.IsZero() || !w.Bind || w.Extra["upgrade_level"] != "12" || w.Extra["forge_level"] != "7" || w.Extra["enchant_card_id"] != "0" || w.Extra["raw_entry_hex"] != hex.EncodeToString(w.RawEntry) {
					t.Fatalf("weapon=%+v", w)
				}
				want := append([]byte(nil), before.Slots["0:10"].RawEntry...)
				binary.LittleEndian.PutUint32(want[2:6], 201)
				clear(want[0x0e:0x13])
				clear(want[0x38:0x3c])
				if !reflect.DeepEqual(w.RawEntry, want) {
					t.Fatalf("instance bytes changed: got=%x want=%x", w.RawEntry, want)
				}
			}
			stable := eventItemSnapshot(t, g)
			if _, err := ApplyEventItem(context.Background(), g, req, now); err == nil {
				t.Fatal("replay accepted after source consumed")
			}
			if !reflect.DeepEqual(stable, eventItemSnapshot(t, g)) {
				t.Fatal("replay mutated assets")
			}
		})
	}
}

func TestEventItemRejectsWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"owner", "deleted", "dungeon", "level", "source_expired", "source_raw_expired", "source_locked", "source_wrong", "slot_closed", "target_expired", "target_locked", "target_count", "target_wrong", "same_slot", "source_path", "stale_before", "stale_after", "missing_state", "invalid_state", "maximum"} {
		t.Run(scenario, func(t *testing.T) {
			kind := EventPermanentWeapon
			if scenario == "maximum" {
				kind = EventInventoryExpansion
			}
			g, req, now := eventItemFixture(t, kind)
			ctx := context.Background()
			bag, _, _ := g.Inventory.Load(ctx, "1")
			c, _, _ := g.Character.Load(ctx, "1")
			source, target := bag.Slots["0:70"], bag.Slots["0:10"]
			switch scenario {
			case "owner":
				c.AccountID = "foreign"
			case "deleted":
				c.Stats = map[string]int64{"delete_flag": 1}
			case "dungeon":
				c.Location.DungeonID = 1
			case "level":
				c.Level = 0
			case "source_expired":
				source.ExpireAt = now
			case "source_raw_expired":
				source.RawEntry = make([]byte, currentItemListEntrySize)
				binary.LittleEndian.PutUint32(source.RawEntry[0x38:], uint32(now.Unix()))
			case "source_locked":
				source.Extra = map[string]string{"equipment_lock_state": "locked"}
			case "source_wrong":
				source.ItemID++
			case "slot_closed":
				req.SourceSlot = 115
				bag.Slots["0:115"] = source
			case "target_expired":
				target.ExpireAt = now
			case "target_locked":
				target.Extra["equipment_lock_state"] = "locked"
			case "target_count":
				target.Count = 2
			case "target_wrong":
				target.ItemID++
			case "same_slot":
				req.TargetSlot = req.SourceSlot
			case "source_path":
				req.Resolve = func(_ repo.CharacterRecord, a, b repo.ItemStack) (EventItemPolicy, repo.ItemStack, repo.ItemStack, error) {
					return EventItemPolicy{}, a, b, ErrEventItem
				}
			case "stale_before":
				req.Revalidate = func() bool { return false }
			case "stale_after":
				n := 0
				req.Revalidate = func() bool { n++; return n == 1 }
			case "missing_state":
				g = memory.NewMemoryGroup()
				if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
					t.Fatal(err)
				}
			case "invalid_state", "maximum":
				v, _, _ := g.Settings.Load(ctx, repo.CharacterContainerStateScope("1"))
				v.Values["main_list_param16"] = "24"
				if scenario == "invalid_state" {
					v.Values["main_list_param16"] = "7"
				}
				if err := g.Settings.Save(ctx, v); err != nil {
					t.Fatal(err)
				}
			}
			bag.Slots["0:70"], bag.Slots["0:10"] = source, target
			if err := g.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			before := eventItemSnapshot(t, g)
			if _, err := ApplyEventItem(ctx, g, req, now); err == nil {
				t.Fatal("invalid operation accepted")
			}
			if !reflect.DeepEqual(before, eventItemSnapshot(t, g)) {
				t.Fatal("failure changed assets")
			}
		})
	}
}

type eventItemFailUOW struct{ repo.CeraShopAssetUnitOfWork }

func (u eventItemFailUOW) WithinCeraShopAssets(ctx context.Context, a, c, s string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error) error {
	return u.CeraShopAssetUnitOfWork.WithinCeraShopAssets(ctx, a, c, s, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository, sr repo.SettingsRepository) error {
		if err := apply(ar, cr, ir, er, sr); err != nil {
			return err
		}
		return errors.New("forced transaction abort after staged writes")
	})
}

func TestEventItemRollbackAndConcurrency(t *testing.T) {
	for _, kind := range []EventItemKind{EventInventoryExpansion, EventSilverCargo, EventPermanentWeapon} {
		g, req, now := eventItemFixture(t, kind)
		before := eventItemSnapshot(t, g)
		base := g.CeraShopAssets
		g.CeraShopAssets = eventItemFailUOW{base}
		if _, err := ApplyEventItem(context.Background(), g, req, now); err == nil {
			t.Fatal("abort not returned")
		}
		if !reflect.DeepEqual(before, eventItemSnapshot(t, g)) {
			t.Fatal("partial commit")
		}
		g.CeraShopAssets = base
		var wg sync.WaitGroup
		result := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := ApplyEventItem(context.Background(), g, req, now); result <- err }()
		}
		wg.Wait()
		close(result)
		successes := 0
		for err := range result {
			if err == nil {
				successes++
			}
		}
		if successes != 1 {
			t.Fatalf("concurrent successes=%d", successes)
		}
	}
}

func TestEventSilverCargoNeverDowngrades(t *testing.T) {
	g, req, now := eventItemFixture(t, EventSilverCargo)
	ctx := context.Background()
	s, _, _ := g.Settings.Load(ctx, repo.CharacterContainerStateScope("1"))
	s.Values["personal_cargo_list_param16"] = "264"
	if err := g.Settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, err := ApplyEventItem(ctx, g, req, now)
	if err != nil || r.CargoSlots != 264 || r.Remaining != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestEventInventoryExpansionStopsAtMaximum(t *testing.T) {
	g, req, now := eventItemFixture(t, EventInventoryExpansion)
	ctx := context.Background()
	bag, _, _ := g.Inventory.Load(ctx, "1")
	s := bag.Slots["0:70"]
	s.Count = 4
	bag.Slots["0:70"] = s
	if err := g.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint16{8, 16, 24} {
		r, err := ApplyEventItem(ctx, g, req, now)
		if err != nil || r.MainExpansion != want {
			t.Fatalf("%+v %v", r, err)
		}
	}
	before := eventItemSnapshot(t, g)
	if _, err := ApplyEventItem(ctx, g, req, now); err == nil {
		t.Fatal("maximum accepted")
	}
	if !reflect.DeepEqual(before, eventItemSnapshot(t, g)) {
		t.Fatal("maximum consumed source")
	}
}

func TestEventPermanentWeaponExtraOnlyCache(t *testing.T) {
	g, req, now := eventItemFixture(t, EventPermanentWeapon)
	ctx := context.Background()
	bag, _, _ := g.Inventory.Load(ctx, "1")
	s := bag.Slots["0:10"]
	s.RawEntry = nil
	bag.Slots["0:10"] = s
	if err := g.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	r, err := ApplyEventItem(ctx, g, req, now)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(r.Target.RawEntry[2:6]) != 201 || binary.LittleEndian.Uint32(r.Target.RawEntry[0x38:0x3c]) != 0 || r.Target.RawEntry[10] != 12 {
		t.Fatal("cache restored expired or old template data")
	}
}
