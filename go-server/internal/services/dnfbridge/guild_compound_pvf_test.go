package dnfbridge

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guildcompound"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildCompoundPVFTestSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst": "",
		"stackable/stackable.lst": `90002 "stackable/flaggem/90002.stk"
90001 "stackable/flaggem/90001.stk"
100 "stackable/material/generic.stk"
90000 "stackable/flaggem/90000.stk"
90001 "stackable/flaggem/90001.stk"
`,
		"equipment/equipment.lst": `100380011 "equipment/character/common/flag/medal_11.equ"
100380010 "equipment/character/common/flag/medal_10.equ"
100500001 "equipment/weapon/flag_like.equ"
100380011 "equipment/character/common/flag/medal_11.equ"
`,
		"stackable/flaggem/90000.stk": `[name] "guardian zero"
[stackable type] "[flag gem]"
[stack limit] 999
`,
		"stackable/flaggem/90001.stk": `[name] "guardian one"
[stackable type] "[flag gem]"
[rarity] 1
[stack limit] 999
`,
		"stackable/flaggem/90002.stk": `[name] "guardian two"
[stackable type] "[flag gem]"
[rarity] 2
[stack limit] 999
`,
		"stackable/material/generic.stk": `[name] "generic"
[stackable type] "[material]"
`,
		"equipment/character/common/flag/medal_10.equ": `[name] "medal ten"
[equipment type] "[flag]"
[rarity] 1
[stack limit] 1
`,
		"equipment/character/common/flag/medal_11.equ": `[name] "medal eleven"
[equipment type] "[flag]"
[rarity] 1
[stack limit] 1
`,
		"equipment/weapon/flag_like.equ": `[name] "not a medal"
[equipment type] "[weapon]"
[rarity] 1
`,
	}
}

func TestCurrentGuildCompoundDefinitionMapsFlagAndGemToList38(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(guildCompoundPVFTestSource())
	if err != nil {
		t.Fatal(err)
	}

	gem, err := currentGuildCompoundDefinition(catalog, 90001)
	if err != nil {
		t.Fatal(err)
	}
	if gem.Family != guildcompound.MaterialFamilyGuardianGem || gem.Rarity != 1 {
		t.Fatalf("gem definition = %+v", gem)
	}

	medal, err := currentGuildCompoundDefinition(catalog, 100380010)
	if err != nil {
		t.Fatal(err)
	}
	if medal.Family != guildcompound.MaterialFamilyGuildMedal || medal.Rarity != 1 {
		t.Fatalf("medal definition = %+v", medal)
	}

	for _, id := range []uint32{100, 100500001} {
		if _, err := currentGuildCompoundDefinition(catalog, id); !errors.Is(err, errGuildCompoundNotMaterial) {
			t.Fatalf("item %d error=%v, want errGuildCompoundNotMaterial", id, err)
		}
	}
}

func TestCurrentGuildCompoundCandidatePoolFollowsLSTOrderAndSkipsNonFamily(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(guildCompoundPVFTestSource())
	if err != nil {
		t.Fatal(err)
	}

	flags, err := currentGuildCompoundCandidatePool(catalog, guildcompound.CompoundFlag, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint32{100380011, 100380010}; !reflect.DeepEqual(flags, want) {
		t.Fatalf("flag candidates = %v, want lst order %v", flags, want)
	}

	gems, err := currentGuildCompoundCandidatePool(catalog, guildcompound.CompoundGem, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint32{90001}; !reflect.DeepEqual(gems, want) {
		t.Fatalf("gem candidates = %v, want %v", gems, want)
	}

	if _, err := currentGuildCompoundCandidatePool(catalog, guildcompound.CompoundKind(99), 1); err == nil {
		t.Fatal("unknown compound kind must be rejected")
	}
}

func TestCurrentGuildCompoundUpdateStackKeepsKindAndCorrectsCount(t *testing.T) {
	gem := dnfrepo.ItemStack{
		ItemID: 90001,
		Count:  2,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"stackable_type": "[flag gem]",
			"amount":         "9",
		},
	}
	gemEntry := currentItemListEntryFromStack(currentGuildMedalInventoryListType, 49, gem)
	gem.RawEntry = append([]byte(nil), gemEntry.data[:]...)
	binary.LittleEndian.PutUint32(gem.RawEntry[0x06:0x0A], 9)

	updatedGem, err := currentGuildCompoundUpdateStack(gem, 49)
	if err != nil {
		t.Fatal(err)
	}
	if len(updatedGem.RawEntry) != currentItemListEntryWireSize ||
		binary.LittleEndian.Uint16(updatedGem.RawEntry[0:2]) != 49 ||
		binary.LittleEndian.Uint32(updatedGem.RawEntry[2:6]) != 90001 ||
		binary.LittleEndian.Uint32(updatedGem.RawEntry[6:10]) != 2 {
		t.Fatalf("updated gem raw=%x", updatedGem.RawEntry)
	}
	if updatedGem.Count != 2 || updatedGem.ItemID != 90001 ||
		updatedGem.Extra["item_kind"] != "stackable" {
		t.Fatalf("updated gem = %+v", updatedGem)
	}

	medal := dnfrepo.ItemStack{
		ItemID: 100380010,
		Count:  1,
		Extra: map[string]string{
			"item_kind":      "equipment",
			"equipment_type": "[flag]",
			"amount":         "7",
		},
	}
	medalEntry := currentItemListEntryFromStack(currentGuildMedalInventoryListType, 0, medal)
	medal.RawEntry = append([]byte(nil), medalEntry.data[:]...)
	medal.RawEntry[0x20] = 0x5A

	updatedMedal, err := currentGuildCompoundUpdateStack(medal, 0)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(updatedMedal.RawEntry[6:10]) != 1 ||
		updatedMedal.RawEntry[0x20] != 0x5A ||
		binary.LittleEndian.Uint16(updatedMedal.RawEntry[0:2]) != 0 {
		t.Fatalf("updated medal raw=%x", updatedMedal.RawEntry)
	}
}

func TestCurrentGuildCompoundUpdateStackRejectsMalformedRows(t *testing.T) {
	base := dnfrepo.ItemStack{
		ItemID: 90001,
		Count:  1,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"stackable_type": "[flag gem]",
		},
	}
	medal := dnfrepo.ItemStack{
		ItemID: 100380010, Count: 1,
		Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[flag]"},
	}
	tests := []struct {
		name  string
		stack dnfrepo.ItemStack
		slot  int16
	}{
		{"gem outside page", base, 0},
		{"medal outside page", medal, 49},
		{"medal count two", dnfrepo.ItemStack{
			ItemID: 100380010, Count: 2,
			Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[flag]"},
		}, 0},
		{"unknown kind", dnfrepo.ItemStack{
			ItemID: 90001, Count: 1,
			Extra: map[string]string{"item_kind": "unknown"},
		}, 49},
		{"short raw", func() dnfrepo.ItemStack {
			x := base
			x.RawEntry = make([]byte, 5)
			return x
		}(), 49},
		{"raw item mismatch", func() dnfrepo.ItemStack {
			x := base
			x.RawEntry = make([]byte, currentItemListEntryWireSize)
			binary.LittleEndian.PutUint16(x.RawEntry[0:2], 49)
			binary.LittleEndian.PutUint32(x.RawEntry[2:6], 90002)
			return x
		}(), 49},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := currentGuildCompoundUpdateStack(tc.stack, tc.slot); err == nil {
				t.Fatal("malformed stack must be rejected")
			}
		})
	}
}

func TestCurrentGuildCompoundGrantLandsOneList38SlotAndReportsNoSpace(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(guildCompoundPVFTestSource())
	if err != nil {
		t.Fatal(err)
	}
	inventory := dnfrepo.InventoryRecord{
		CharacterID: "7",
		Slots:       map[string]dnfrepo.ItemStack{},
	}

	gemSlot, err := currentGuildCompoundGrant(catalog, &inventory, 90001)
	if err != nil {
		t.Fatal(err)
	}
	if gemSlot != guildcompound.GuardianGemPageFirst {
		t.Fatalf("gem slot=%d, want %d", gemSlot, guildcompound.GuardianGemPageFirst)
	}
	gemKey := currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, gemSlot)
	granted := inventory.Slots[gemKey]
	if granted.ItemID != 90001 || granted.Count != 1 ||
		len(granted.RawEntry) != currentItemListEntryWireSize {
		t.Fatalf("granted gem = %+v", granted)
	}

	again, err := currentGuildCompoundGrant(catalog, &inventory, 90001)
	if err != nil {
		t.Fatal(err)
	}
	if again != gemSlot || inventory.Slots[gemKey].Count != 2 {
		t.Fatalf("second gem grant slot=%d stack=%+v", again, inventory.Slots[gemKey])
	}

	medalSlot, err := currentGuildCompoundGrant(catalog, &inventory, 100380010)
	if err != nil {
		t.Fatal(err)
	}
	if medalSlot != guildcompound.GuildMedalPageFirst {
		t.Fatalf("medal slot=%d, want %d", medalSlot, guildcompound.GuildMedalPageFirst)
	}

	full := dnfrepo.InventoryRecord{
		CharacterID: "8",
		Slots:       map[string]dnfrepo.ItemStack{},
	}
	for slot := guildcompound.GuardianGemPageFirst; slot <= guildcompound.GuardianGemPageLast; slot++ {
		full.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, slot)] =
			dnfrepo.ItemStack{ItemID: 999999, Count: 1}
	}
	before := len(full.Slots)
	if _, err := currentGuildCompoundGrant(catalog, &full, 90001); !errors.Is(err, guildcompound.ErrNoSpace) {
		t.Fatalf("full gem page error=%v, want ErrNoSpace", err)
	}
	if len(full.Slots) != before {
		t.Fatal("failed grant mutated a full inventory")
	}

	if _, err := currentGuildCompoundGrant(catalog, &inventory, 100); !errors.Is(err, errGuildCompoundNotMaterial) {
		t.Fatalf("non-family grant error=%v", err)
	}
}
