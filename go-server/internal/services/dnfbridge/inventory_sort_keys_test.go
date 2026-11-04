package dnfbridge

import (
	"context"
	"os"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
)

func TestInventorySortUsesPVFKeysThroughNativeRequest(t *testing.T) {
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "9001 `weapon/a.equ`\n9002 `coat/b.equ`\n",
		"stackable/stackable.lst": "100 `a.stk`\n200 `b.stk`\n",
		"equipment/weapon/a.equ":  "[equipment type] `[weapon]`\n[minimum level] 90\n[rarity] 4\n",
		"equipment/coat/b.equ":    "[equipment type] `[coat]`\n[minimum level] 1\n[rarity] 6\n",
		"stackable/a.stk":         "[stackable type] `[waste]`\n",
		"stackable/b.stk":         "[stackable type] `[booster]`\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	f := newRefreshCostFixture(t)
	f.service.pvfItemCatalog = catalog
	a, err := f.service.currentInventorySortKeys(r.ItemStack{ItemID: 9001})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.service.currentInventorySortKeys(r.ItemStack{ItemID: 9002})
	if err != nil {
		t.Fatal(err)
	}
	if a.MinimumLevel != 90 || a.Rarity != 4 || a.PartRank != 12 || b.PartRank != 14 {
		t.Fatalf("PVF keys=%+v %+v", a, b)
	}
	// Stale metadata disagrees with runtime PVF. Only the active catalog governs.
	ctx := context.Background()
	if err := f.group.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{
		"0:65": {ItemID: 100, Count: 4, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[aaa]"}},
		"0:68": {ItemID: 200, Count: 3, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[zzz]"}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Current protobuf body: fields 2=list0, 3=category2, 4=condition3.
	handled, err := f.service.handleAlignedGameCommand(f.session, 1, 20, []byte{6, 0, 0, 0, 16, 0, 24, 2, 32, 3})
	if err != nil || !handled {
		t.Fatalf("sort dispatch handled=%v err=%v", handled, err)
	}
	after, _, _ := f.group.Inventory.Load(ctx, "19")
	if after.Slots["0:65"].ItemID != 200 || after.Slots["0:66"].ItemID != 100 {
		t.Fatalf("condition was ignored: %+v", after.Slots)
	}
	frames := splitSortLockFrames(t, f.buffer.write.Bytes())
	if len(frames) < 2 || frames[0].Header.MsgID != 20 || frames[1].Header.MsgID != 13 {
		t.Fatalf("sort ACK/list order: %+v", frames)
	}
}

func TestRealPVFInventorySortKeys(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := pvf.LoadArchive(pvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{pvfItemCatalog: catalog}
	families := map[string]bool{}
	for _, id := range catalog.stackableIDs {
		def, err := catalog.ResolveItem(id)
		if err != nil {
			continue
		}
		dst := currentPVFItemTargetContainer(def)
		if dst.ListType != 0 || dst.SlotStart != 65 || families[def.StackableType] {
			continue
		}
		keys, err := service.currentInventorySortKeys(r.ItemStack{ItemID: int64(id), Extra: map[string]string{"stackable_type": "not-authoritative"}})
		if err != nil {
			t.Fatal(err)
		}
		if keys.StackableType != def.StackableType || strings.TrimSpace(keys.StackableType) == "" {
			t.Fatalf("sort key missing: %+v", keys)
		}
		t.Logf("real consumable item=%d type=%s path=%s", id, keys.StackableType, def.PVFPath)
		families[def.StackableType] = true
		if len(families) == 2 {
			break
		}
	}
	if len(families) != 2 {
		t.Fatal("two distinct real consumable types required")
	}
}
