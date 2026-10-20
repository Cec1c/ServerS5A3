package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

func TestRealPVFEquipmentRecipeUpgradeTransactions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := newPVFEquipmentRecipeCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	entries, coverage, err := recipes.Scan()
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	normal, admitted, committed := 0, 0, 0
	rejected := map[string]int{}
	lukeArmor := map[int64]bool{}
	heblon := map[int64]bool{}
	for _, entry := range entries {
		if entry.MaintainAbility != "normal" {
			continue
		}
		normal++
		_, err := currentEquipmentRecipeUpgradeDocument(entry, archive)
		if err != nil {
			rejected[err.Error()]++
			continue
		}
		admitted++
		t.Run(fmt.Sprint(entry.ItemID), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			input := entry.Materials[0].ItemID
			raw := make([]byte, currentItemListEntryWireSize)
			binary.LittleEndian.PutUint16(raw[:2], 9)
			binary.LittleEndian.PutUint32(raw[2:6], uint32(input))
			binary.LittleEndian.PutUint32(raw[6:10], 203004656)
			raw[0x0A] = 12
			binary.LittleEndian.PutUint32(raw[0x0E:0x12], 987654)
			raw[currentEquipmentVectorOffset] = 2
			binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+1:], 123)
			binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+5:], 456)
			inv := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:9": {ItemID: input, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment"}}, "0:69": {ItemID: entry.ItemID, Count: 2}}}
			account := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}
			for i, m := range entry.Materials[1:] {
				key := fmt.Sprintf("0:%d", 121+i)
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(m.ItemID); shared {
					account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = dnfrepo.ItemStack{ItemID: m.ItemID, Count: m.Count * 2}
				} else {
					inv.Slots[key] = dnfrepo.ItemStack{ItemID: m.ItemID, Count: m.Count * 2}
				}
			}
			character := dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 100, Stats: map[string]int64{"gold": entry.GoldCost*2 + 100}}
			for _, err := range []error{repos.Character.Save(ctx, character), repos.Inventory.Save(ctx, inv), repos.AccountInventory.Save(ctx, account)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{options: options{accountID: "account-1"}, pvfItemCatalog: items, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			result, err := s.commitCurrentEquipmentRecipeCompound(ctx, &gameSession{selectedCharacterID: 19}, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, Count: 1})
			if err != nil {
				t.Fatalf("%s: %v", entry.Name, err)
			}
			after, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			a, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			c, _, _ := repos.Character.Load(ctx, "19")
			if after.Slots["0:9"].ItemID != entry.Outputs[0].ItemID || after.Slots["0:69"].Count != 1 || result.Outputs != 1 || c.Stats["gold"] != character.Stats["gold"]-entry.GoldCost {
				t.Fatalf("incorrect result=%+v inv=%+v", result, after)
			}
			for _, m := range entry.Materials[1:] {
				n := equipmentRecipeCompoundTestTotal(after, m.ItemID)
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(m.ItemID); shared {
					n = a.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count
				}
				if n != m.Count {
					t.Fatalf("material=%d remaining=%d expected=%d", m.ItemID, n, m.Count)
				}
			}
			want := append([]byte(nil), raw...)
			binary.LittleEndian.PutUint32(want[2:6], uint32(entry.Outputs[0].ItemID))
			want[0x0D] = after.Slots["0:9"].RawEntry[0x0D]
			if !entry.MaintainSocketStat {
				clear(want[currentEquipmentVectorOffset : currentEquipmentVectorOffset+9])
			}
			if !bytes.Equal(want, after.Slots["0:9"].RawEntry) {
				t.Fatalf("attributes lost got=%x want=%x", after.Slots["0:9"].RawEntry, want)
			}
			if _, err := s.commitCurrentEquipmentRecipeCompound(ctx, &gameSession{selectedCharacterID: 19}, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, Count: 1}); err == nil {
				t.Fatal("replay accepted")
			}
			again, _, _ := repos.Inventory.Load(ctx, "19")
			againC, _, _ := repos.Character.Load(ctx, "19")
			againA, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if !reflect.DeepEqual(after, again) || !reflect.DeepEqual(c, againC) || !reflect.DeepEqual(a, againA) {
				t.Fatal("replay changed assets")
			}
			committed++
			if entry.ItemID >= 10159624 && entry.ItemID <= 10159648 {
				lukeArmor[entry.ItemID] = true
			}
			if entry.ItemID >= 10304471 && entry.ItemID <= 10304473 {
				heblon[entry.ItemID] = true
			}
		})
	}
	t.Logf("coverage=%+v normal=%d admitted=%d committed=%d luke90Barmor=%d rejected=%v", coverage, normal, admitted, committed, len(lukeArmor), rejected)
	if normal != 287 || admitted != 93 || committed != 93 || len(lukeArmor) != 25 || len(heblon) != 3 {
		t.Fatalf("coverage changed normal=%d admitted=%d committed=%d", normal, admitted, committed)
	}
	for id := int64(10159634); id <= 10159638; id++ {
		if !lukeArmor[id] {
			t.Errorf("missing tested Luke larmor drawing %d", id)
		}
	}
}
