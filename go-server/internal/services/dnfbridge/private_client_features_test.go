package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestPrivateFeatureFingerprintSurvivesRealWearOwnerRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		list byte
		slot int16
		kind string
	}{{"armor", 0, 14, "[coat]"}, {"avatar", 1, 0, "[hat avatar]"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			raw := make([]byte, 119)
			binary.LittleEndian.PutUint32(raw[2:], 1000)
			binary.LittleEndian.PutUint32(raw[6:], 333)
			raw[0xb] = 5
			stack := repo.ItemStack{ItemID: 1000, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "equipment_type": tc.kind, "quality_seed": "333", "durability": "5"}}
			key := strconv.Itoa(int(tc.list)) + ":20"
			if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "preset-account"}); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{key: stack}}); err != nil {
				t.Fatal(err)
			}
			if err := g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "77", Entries: map[string]repo.EquipmentEntry{}}); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfequip.NewOwnerWithPlacementValidator(g, dnfequip.PlacementValidatorFunc(func(context.Context, dnfequip.Placement) error { return nil }))
			if err != nil {
				t.Fatal(err)
			}
			equip := dnfequip.MoveCommand{SelectedCharacterID: 77, SourceListType: tc.list, SourceSlotIndex: 20, SourceInstanceValue: 1000, MoveCount: 1, DestinationListType: 3, DestinationSlotIndex: tc.slot}
			if _, err = owner.Move(ctx, equip); err != nil {
				t.Fatal(err)
			}
			eq, _, _ := g.Equipment.Load(ctx, "77")
			wanted, ok := privateFeatureWorn(eq, time.Now())[uint16(tc.slot)]
			if !ok {
				t.Fatal("new current-client worn slot not projected", eq)
			}
			if _, err = owner.Move(ctx, dnfequip.MoveCommand{SelectedCharacterID: 77, SourceListType: 3, SourceSlotIndex: tc.slot, SourceInstanceValue: 1000, MoveCount: 1, DestinationListType: tc.list, DestinationSlotIndex: 21, PlayerRequestFlag: true}); err != nil {
				t.Fatal(err)
			}
			eq, _, _ = g.Equipment.Load(ctx, "77")
			inv, _, _ := g.Inventory.Load(ctx, "77")
			status, list, slot := privateFeatureResolve(wanted, eq, inv, time.Now())
			if status != 1 || list != uint16(tc.list) || slot != 21 {
				t.Fatalf("unequipped saved instance was lost: status=%d list=%d slot=%d eq=%+v inv=%+v", status, list, slot, eq, inv)
			}
			equip.SourceSlotIndex = 21
			if _, err = owner.Move(ctx, equip); err != nil {
				t.Fatal(err)
			}
			eq, _, _ = g.Equipment.Load(ctx, "77")
			if status, _, _ := privateFeatureResolve(wanted, eq, inv, time.Now()); status != 0 {
				t.Fatal("native wear acknowledgement not recognized", status)
			}
		})
	}
}

func TestPrivateFeatureSkyFullSetRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("paired PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{101550535, 101560695, 101570449, 101500716, 101510880, 101540633, 101520519, 101530478}
	now := time.Now().UTC()
	eq := repo.EquipmentRecord{Entries: map[string]repo.EquipmentEntry{}}
	for slot, id := range ids {
		key := strconv.Itoa(slot)
		eq.Entries[key] = repo.EquipmentEntry{SlotIndex: int16(slot), ItemID: id, Extra: map[string]string{"current_exe_equipment_type": key, "current_exe_runtime_move": "1", "item_kind": "avatar"}}
	}
	if got, ok := privateFeatureSkyIDs(catalog, eq, now); !ok || got[0] != uint32(ids[0]) || got[7] != uint32(ids[7]) {
		t.Fatal("PVF-proven grade3 rarity2 rare set rejected", got, ok)
	}
	// These two legacy gunner definitions formerly omitted the shared set
	// association. Test the production parser and radius classifier, not text.
	for _, legacy := range []struct {
		slot string
		id   int64
	}{{"0", 47121}, {"6", 48307}} {
		old := eq.Entries[legacy.slot]
		entry := old
		entry.ItemID = legacy.id
		eq.Entries[legacy.slot] = entry
		if _, ok := privateFeatureSkyIDs(catalog, eq, now); !ok {
			t.Fatalf("legacy sky part %d missing set association", legacy.id)
		}
		eq.Entries[legacy.slot] = old
	}
	for slot := range ids {
		key := strconv.Itoa(slot)
		entry := eq.Entries[key]
		delete(eq.Entries, key)
		if _, ok := privateFeatureSkyIDs(catalog, eq, now); ok {
			t.Fatal("incomplete set enabled radius", slot)
		}
		entry.ExpireAt = now.Add(-time.Second)
		eq.Entries[key] = entry
		if _, ok := privateFeatureSkyIDs(catalog, eq, now); ok {
			t.Fatal("expired piece enabled radius", slot)
		}
		entry.ExpireAt = time.Time{}
		eq.Entries[key] = entry
	}
	// A rare armor template and a duplicate hat in another costume slot must
	// never replace the missing matching costume part.
	old := eq.Entries["7"]
	bad := old
	bad.ItemID = ids[0]
	eq.Entries["7"] = bad
	if _, ok := privateFeatureSkyIDs(catalog, eq, now); ok {
		t.Fatal("wrong costume part enabled radius")
	}
	eq.Entries["7"] = old
}

func TestPrivateFeatureFingerprintInstanceAndWearState(t *testing.T) {
	var a currentItemListEntry
	binary.LittleEndian.PutUint32(a.data[2:], 12345)
	binary.LittleEndian.PutUint32(a.data[6:], 98765)
	a.data[10] = 11
	a.data[0x3c] = 4
	base := privateFeatureFingerprint(a, false)
	for _, off := range []int{0, 1, 0xb, 0xc, 0x60} {
		b := a
		b.data[off] = 7
		if privateFeatureFingerprint(b, false) != base {
			t.Fatalf("transient wear field %x invalidated identity", off)
		}
	}
	for _, off := range []int{2, 6, 10, 0x12, 0x3c, 0x44, 0x72} {
		b := a
		b.data[off]++
		if privateFeatureFingerprint(b, false) == base {
			t.Fatalf("different instance/affix %x aliased", off)
		}
	}
	b := a
	b.data[6]++
	if privateFeatureFingerprint(b, true) != privateFeatureFingerprint(a, true) {
		t.Fatal("rental countdown treated as avatar identity")
	}
	b = a
	b.data[0xb]++
	if privateFeatureFingerprint(b, true) == privateFeatureFingerprint(a, true) {
		t.Fatal("different avatar ability aliased")
	}
}
func TestPrivateFeatureResolveNeverSubstitutesSameTemplate(t *testing.T) {
	now := time.Now().UTC()
	stack := repo.ItemStack{ItemID: 100, Count: 1, Extra: map[string]string{"quality_seed": "333", "item_kind": "equipment", "ext_data0": "7", "durability": "20"}}
	row := currentItemListEntryFromStack(0, 20, stack)
	wanted := privateFeatureItem{12, 100, privateFeatureFingerprint(row, false)}
	inv := repo.InventoryRecord{Slots: map[string]repo.ItemStack{"0:20": stack}}
	eq := repo.EquipmentRecord{Entries: map[string]repo.EquipmentEntry{}}
	status, l, slot := privateFeatureResolve(wanted, eq, inv, now)
	if status != 1 || l != 0 || slot != 20 {
		t.Fatalf("exact instance %d %d %d", status, l, slot)
	}
	other := stack
	other.Extra = map[string]string{}
	for k, v := range stack.Extra {
		other.Extra[k] = v
	}
	other.Extra["quality_seed"] = "334"
	inv.Slots["0:20"] = other
	if status, _, _ := privateFeatureResolve(wanted, eq, inv, now); status != 3 {
		t.Fatal("same-template different quality substituted", status)
	}
	inv.Slots["0:20"] = stack
	inv.Slots["0:21"] = stack
	if status, _, _ := privateFeatureResolve(wanted, eq, inv, now); status != 4 {
		t.Fatal("ambiguous duplicate accepted", status)
	}
	delete(inv.Slots, "0:21")
	expired := stack
	expired.ExpireAt = now.Add(-time.Second)
	inv.Slots["0:20"] = expired
	if status, _, _ := privateFeatureResolve(wanted, eq, inv, now); status != 3 {
		t.Fatal("expired item accepted", status)
	}
}
func TestPrivateFeatureCurrentSlotCatalog(t *testing.T) {
	if len(privateFeatureSlots) != 29 {
		t.Fatal(len(privateFeatureSlots))
	}
	seen := map[uint16]bool{}
	for _, slot := range privateFeatureSlots {
		if seen[slot] || slot > 32 || slot == 11 || slot == 24 || slot == 30 || slot == 31 {
			t.Fatal("invalid current-client slot", slot)
		}
		seen[slot] = true
	}
	for _, slot := range []uint16{25, 26, 27, 28, 29, 32} {
		if !seen[slot] {
			t.Fatal("current earring/pet/medal missing", slot)
		}
	}
}
