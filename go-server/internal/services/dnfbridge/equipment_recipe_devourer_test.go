package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const devourerUpgradeDrawing = "" +
	"[stackable type]\n" +
	"`[recipe]` 1\n" +
	"[minimum level]\n" +
	"84\n" +
	"[usable job]\n" +
	"`[all]`\n" +
	"[/usable job]\n" +
	"[need material]\n" +
	"10093974 30\n" +
	"[/need material]\n" +
	"[input item]\n" +
	"100300212 1 10093972 100 3262 1000 10095702 5 10000224 1\n" +
	"[/input item]\n" +
	"[output item]\n" +
	"100300221 1\n" +
	"[/output item]\n" +
	"[maintain ability]\n" +
	"`normal`\n" +
	"[minimum itemmaking level]\n" +
	"`amulet` 8\n"

type devourerTestSource map[string]string

func (s devourerTestSource) ReadText(relativePath string) (string, error) {
	value, found := s[relativePath]
	if !found {
		return "", errors.New("devourer test path not found: " + relativePath)
	}
	return value, nil
}

func devourerRecipeTestSource(itemID int64) devourerTestSource {
	return devourerTestSource{
		"monster/monster.lst":                             "",
		"equipment/equipment.lst":                         "100300212 `character/common/amulet/100300212.equ`\n100300221 `character/common/amulet/100300221.equ`\n",
		"equipment/character/common/amulet/100300212.equ": "[equipment type]\n`[amulet]`\n[grade]\n91\n[attach type]\n`[trade]`\n[durability]\n40\n",
		"equipment/character/common/amulet/100300221.equ": "[equipment type]\n`[amulet]`\n[grade]\n91\n[attach type]\n`[trade]`\n[durability]\n40\n",
		"stackable/stackable.lst": "10096047 `10096001/final.stk`\n" +
			"10093972 `10093001/pure.stk`\n" +
			"3262 `material/cube.stk`\n" +
			"10095702 `10095001/soul.stk`\n" +
			"10000224 `indexnamed/furnace.stk`\n" +
			"10093974 `10093001/condensed.stk`\n",
		"stackable/10096001/final.stk":     devourerUpgradeDrawing,
		"stackable/10093001/pure.stk":      "[stackable type]\n`[material]`\n[stack limit]\n999\n",
		"stackable/material/cube.stk":      "[stackable type]\n`[material]`\n[stack limit]\n999\n",
		"stackable/10095001/soul.stk":      "[stackable type]\n`[material]`\n[stack limit]\n999\n",
		"stackable/indexnamed/furnace.stk": "[stackable type]\n`[material]`\n[stack limit]\n999\n",
		"stackable/10093001/condensed.stk": "[stackable type]\n`[material]`\n[stack limit]\n999\n",
	}
}

func TestDevourerUpgradeTreatsNeedMaterialAsPurchasePrice(t *testing.T) {
	source := devourerRecipeTestSource(10096047)
	recipes, err := newPVFEquipmentRecipeCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := recipes.Recipe(10096047)
	if err != nil || !found {
		t.Fatalf("drawing found=%t err=%v", found, err)
	}
	document, err := currentEquipmentRecipeUpgradeDocument(entry, recipes.source)
	if err != nil {
		t.Fatalf("owned devourer drawing rejected: %v", err)
	}
	if _, found, err := currentEquipmentRecipeCompoundQualificationTokens(document, "need material"); err != nil || found {
		t.Fatalf("purchase price leaked into crafting document found=%t err=%v", found, err)
	}
	if want := []equipmentRecipeItemStack{{100300212, 1}, {10093972, 100}, {3262, 1000}, {10095702, 5}, {10000224, 1}}; !reflect.DeepEqual(entry.Materials, want) {
		t.Fatalf("crafting materials=%v want=%v", entry.Materials, want)
	}
}

func TestDevourerUpgradeGenericNeedMaterialGateRemainsClosed(t *testing.T) {
	source := devourerRecipeTestSource(10096047)
	source["stackable/stackable.lst"] += "10096053 `10096001/final.stk`\n"
	recipes, err := newPVFEquipmentRecipeCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := recipes.Recipe(10096053)
	if err != nil || !found {
		t.Fatalf("unknown drawing found=%t err=%v", found, err)
	}
	if _, err := currentEquipmentRecipeUpgradeDocument(entry, recipes.source); err == nil {
		t.Fatal("same shape under an unproven item ID was admitted")
	}
}

func TestDevourerUpgradeDoesNotConsumePurchasePrice(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	source := devourerRecipeTestSource(10096047)
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], 9)
	binary.LittleEndian.PutUint32(raw[2:6], 100300212)
	binary.LittleEndian.PutUint32(raw[6:10], 12345)
	raw[0x0A] = 12
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 40)
	raw[currentEquipmentVectorOffset] = 2
	binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+1:], 123)
	binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+5:], 456)
	inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:9":   {ItemID: 100300212, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/character/common/amulet/100300212.equ"}},
		"0:69":  {ItemID: 10096047, Count: 2},
		"0:121": {ItemID: 10093972, Count: 200},
		"0:123": {ItemID: 10095702, Count: 10},
		"0:124": {ItemID: 10000224, Count: 2},
		"0:125": {ItemID: 10093974, Count: 60},
	}}
	for _, err := range []error{
		repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 0}}),
		repos.Inventory.Save(ctx, inventory),
		repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{
			dnfrepo.AccountSharedInventorySlotKey(359): {ItemID: 3262, Count: 2000},
		}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:        options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog: items,
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		},
	}
	session := &gameSession{selectedCharacterID: 19, itemListMigrationsDone: true}
	result, err := service.commitCurrentEquipmentRecipeCompound(ctx, session, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, SourceItemID: -1, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs != 1 {
		t.Fatalf("result=%+v", result)
	}
	after, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if after.Slots["0:9"].ItemID != 100300221 || after.Slots["0:69"].Count != 1 {
		t.Fatalf("upgrade result=%+v", after.Slots)
	}
	for slot, want := range map[string]int64{"0:121": 100, "0:123": 5, "0:124": 1, "0:125": 60} {
		if after.Slots[slot].Count != want {
			t.Errorf("slot %s count=%d want=%d", slot, after.Slots[slot].Count, want)
		}
	}
	account, _, err := repos.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if account.Slots[dnfrepo.AccountSharedInventorySlotKey(359)].Count != 1000 {
		t.Errorf("gold cube balance=%+v", account.Slots[dnfrepo.AccountSharedInventorySlotKey(359)])
	}
}

func TestRealPVFEquipmentRecipeDevourerUpgrades(t *testing.T) {
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
	want := map[int64][2]int64{
		10304468: {100312099, 100312095},
		10304469: {100300216, 100300212},
		10304470: {100321958, 100321954},
		10096047: {100300212, 100300221},
		10096048: {100312095, 100312103},
		10096049: {100321954, 100321962},
		10096050: {100343175, 100343190},
		10096051: {100352106, 100352113},
		10096052: {100352106, 100352115},
	}
	for id, inputOutput := range want {
		entry, found, err := recipes.Recipe(id)
		if err != nil || !found {
			t.Fatalf("drawing %d found=%t err=%v", id, found, err)
		}
		if len(entry.Materials) == 0 || entry.Materials[0].ItemID != inputOutput[0] || len(entry.Outputs) != 1 || entry.Outputs[0].ItemID != inputOutput[1] {
			t.Fatalf("drawing %d input/output=%v/%v", id, entry.Materials, entry.Outputs)
		}
		if !reflect.DeepEqual(entry.ExtraMaterials, []equipmentRecipeItemStack{{10093974, 30}}) {
			t.Fatalf("drawing %d purchase price=%v", id, entry.ExtraMaterials)
		}
		if _, err := currentEquipmentRecipeUpgradeDocument(entry, archive); err != nil {
			t.Fatalf("drawing %d rejected: %v entry=%+v", id, err, entry)
		}
	}
}

func TestDevourerUpgradeChoosesUnlockedInstanceAndPreservesIdentity(t *testing.T) {
	source := devourerRecipeTestSource(10096047)
	recipes, e := newPVFEquipmentRecipeCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	entry, _, e := recipes.Recipe(10096047)
	if e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	inv := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
	for _, slot := range []int16{9, 10, 11} {
		raw := make([]byte, currentItemListEntryWireSize)
		binary.LittleEndian.PutUint16(raw, uint16(slot))
		binary.LittleEndian.PutUint32(raw[2:], 100300212)
		binary.LittleEndian.PutUint32(raw[6:], uint32(1000+slot))
		raw[10] = byte(slot)
		extra := map[string]string{"instance": string(rune('A' + slot - 9))}
		if slot == 9 {
			extra["equipment_lock_state"] = "1"
		}
		inv.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 100300212, Count: 1, RawEntry: raw, Extra: extra}
	}
	before := dnfrepo.CloneInventory(inv)
	plan, e := planCurrentEquipmentRecipeUpgrade(entry, items, &inv, 1)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Slot != 10 || plan.Stack.Extra["instance"] != "B" {
		t.Fatalf("selected wrong instance %+v", plan)
	}
	delete(inv.Slots, plan.Key)
	if e = applyCurrentEquipmentRecipeUpgrade(&inv, plan); e != nil {
		t.Fatal(e)
	}
	if inv.Slots["0:10"].ItemID != 100300221 || inv.Slots["0:10"].RawEntry[10] != 10 || inv.Slots["0:10"].Extra["instance"] != "B" {
		t.Fatal("lost selected identity")
	}
	for _, key := range []string{"0:9", "0:11"} {
		if !reflect.DeepEqual(inv.Slots[key], before.Slots[key]) {
			t.Fatalf("changed unrelated instance %s", key)
		}
	}
}
