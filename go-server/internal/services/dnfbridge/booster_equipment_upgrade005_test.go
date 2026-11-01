package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

func TestEquipmentUpgrade005CategoryScopeAndKinds(t *testing.T) {
	text := "[booster selection num] 1\n[booster select category] 0\n[booster equipment upgrade] 7\n[booster equipment separate] 5\n[equipment] 700 1\n[stackable] 800 2\n[booster equipment upgrade] 12\n[equipment] 701 1\n[/booster select category]\n[booster select category] 1\n[equipment] 702 1\n[/booster select category]"
	doc, err := pvf.Parse("stackable/test.stk", text)
	if err != nil {
		t.Fatal(err)
	}
	_, categories, err := parseCurrentBoosterSelectionCatalog(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32][2]byte{700: {7, 5}, 800: {0, 0}, 701: {12, 5}, 702: {0, 0}}
	for _, rows := range categories {
		for _, row := range rows {
			if [2]byte{row.EquipmentUpgrade, row.EquipmentSeparate} != want[row.ItemID] {
				t.Fatalf("item %d upgrade %d want %v", row.ItemID, row.EquipmentUpgrade, want[row.ItemID])
			}
			delete(want, row.ItemID)
		}
	}
	if len(want) != 0 {
		t.Fatal("missing reward rows")
	}
}

func TestEquipmentUpgrade005RejectsUnprovedMetadata(t *testing.T) {
	for _, field := range []string{"[booster equipment upgrade] 255", "[booster equipment upgrade] -1", "[booster equipment upgrade] 32", "[booster equipment upgrade] `7`", "[booster equipment upgrade] 7.5", "[booster equipment upgrade] 7 12", "[booster equipment grade] 4", "[booster equipment separate] 9", "[booster equipment separate] -1", "[booster equipment separate] `5`", "[booster equipment separate] 5.5", "[booster equipment separate] 3 5"} {
		doc, e := pvf.Parse("stackable/test.stk", "[booster selection num] 1\n[booster select category] 0\n"+field+"\n[equipment] 700 1\n[/booster select category]")
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = parseCurrentBoosterSelectionCatalog(doc); e == nil {
			t.Fatalf("accepted %s", field)
		}
	}
}

func TestEquipmentUpgrade005PreservesOtherInstanceBytes(t *testing.T) {
	raw := make([]byte, 119)
	for i := range raw {
		raw[i] = byte(i*17 + 3)
	}
	raw[10] = 0xe0
	stack := repo.ItemStack{ItemID: 700, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "12345", "durability": "45", "seal_flag": "1"}}
	before := currentItemListEntryFromStack(0, 9, stack)
	changed, err := currentBoosterUpgradedStack("0:9", stack, 12, 5)
	if err != nil {
		t.Fatal(err)
	}
	projected := currentItemListEntryFromStack(0, 9, changed)
	if changed.RawEntry[10] != 0xec || projected.data[10] != 0xec || changed.Extra["upgrade_level"] != "12" || changed.RawEntry[0x57] != 5 || projected.data[0x57] != 5 || changed.Extra["byte_57"] != "5" || changed.Extra["value_57"] != "5" {
		t.Fatalf("wrong packed/typed upgrade %+v", changed.Extra)
	}
	expected := append([]byte(nil), before.data[:]...)
	expected[10] = 0xec
	expected[0x57] = 5
	if !bytes.Equal(expected, changed.RawEntry) || !bytes.Equal(expected, projected.data[:]) {
		t.Fatal("unrelated instance bytes changed")
	}
	if changed.Extra["quality_seed"] != "12345" || changed.Extra["durability"] != "45" || changed.Extra["seal_flag"] != "1" {
		t.Fatal("unrelated equipment fields changed")
	}
	for _, level := range []byte{0, 32, 255} {
		if _, e := currentBoosterUpgradedStack("0:9", stack, level, 0); e == nil {
			t.Fatalf("invalid level accepted %d", level)
		}
	}
}

func TestEquipmentUpgrade005ForgingOnlyAndInvalidRanges(t *testing.T) {
	for _, level := range []byte{1, 3, 4, 5, 6, 8} {
		stack := repo.ItemStack{ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}
		changed, e := currentBoosterUpgradedStack("0:9", stack, 0, level)
		if e != nil {
			t.Fatal(e)
		}
		entry := currentItemListEntryFromStack(0, 9, changed)
		if entry.data[10]&31 != 0 || entry.data[0x57] != level {
			t.Fatal("forging-only projection changed")
		}
	}
	for _, level := range []byte{9, 255} {
		stack := repo.ItemStack{ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}
		if _, e := currentBoosterUpgradedStack("0:9", stack, 0, level); e == nil {
			t.Fatal("invalid forging accepted")
		}
	}
}

func TestEquipmentUpgrade005StackableCompatibilityRealDispatch(t *testing.T) {
	for _, attribute := range []string{"[booster equipment upgrade] 12", "[booster equipment separate] 5", "[booster equipment grade] 4"} {
		source := dungeonDropCatalogTestSource{
			"monster/monster.lst": "", "equipment/equipment.lst": "",
			"stackable/stackable.lst": "500 `box.stk`\n600 `reward.stk`\n",
			"stackable/box.stk":       "[stackable type] `[booster selection]`\n[booster selection num] 1\n[booster select category] 0\n" + attribute + "\n[equipment] 600 3\n[/booster select category]",
			"stackable/reward.stk":    "[stackable type] `[material]`\n[stack limit] 999",
		}
		catalog, e := newPVFDungeonDropCatalog(source)
		if e != nil {
			t.Fatal(e)
		}
		g := memory.NewMemoryGroup()
		ctx := context.Background()
		if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}); e != nil {
			t.Fatal(e)
		}
		if e := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:75": {ItemID: 500, Count: 1}}}); e != nil {
			t.Fatal(e)
		}
		body := make([]byte, 10)
		binary.LittleEndian.PutUint16(body, 75)
		binary.LittleEndian.PutUint32(body[4:], 600)
		conn := &bufferConn{}
		session := &gameSession{conn: conn, selectedCharacterID: 19}
		if e := currentBoosterTestService(catalog, g).handleGameCommand(session, 1, 160, body); e != nil {
			t.Fatal(e)
		}
		ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if ack.Body[0] != 1 {
			t.Fatalf("historical stackable reward rejected: %s", attribute)
		}
		bag, _, _ := g.Inventory.Load(ctx, "19")
		var count int64
		for _, stack := range bag.Slots {
			if stack.ItemID != 600 || stack.Extra["item_kind"] != "stackable" || stack.Extra["upgrade_level"] != "" || stack.Extra["byte_57"] != "" {
				t.Fatalf("non-stackable instance fields: %+v", stack)
			}
			count += stack.Count
		}
		if count != 3 {
			t.Fatal("stackable reward count changed")
		}
	}
}
