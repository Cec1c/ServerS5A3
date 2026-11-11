package inventory

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	testWornPetItemID       int64 = 400990167
	testWornPetSerial       int64 = 37
	testWornPetCardID       int64 = 10008668
	testWornPetSecondCardID int64 = 10008713
	testWornBeadSlot        int16 = 12
	testWornSecondBeadSlot  int16 = 13
	testWornPetRawSize            = 43
)

// saveWornPetEnchantFixture 造出"宝珠在主背包 + 宠物已穿在装备槽 26"的现场：
// 穿戴行 raw 用**穿戴布局**（43 字节，宝珠 id 在 0x10:0x14），PetEntry 的坐标是 (3,26)。
func saveWornPetEnchantFixture(t *testing.T, ctx context.Context, repos dnfrepo.Group, beadCount int64) {
	t.Helper()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, testWornBeadSlot): {
				ItemID: 490007163,
				Count:  beadCount,
				Extra:  map[string]string{"item_kind": "stackable"},
			},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	wornRaw := make([]byte, testWornPetRawSize)
	binary.LittleEndian.PutUint32(wornRaw[5:9], uint32(testWornPetSerial))
	binary.LittleEndian.PutUint32(wornRaw[24:28], uint32(testWornPetSerial))
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"26": {
				SlotIndex: currentPetWornEquipmentSlot,
				ItemID:    testWornPetItemID,
				RawEntry:  wornRaw,
				Extra: map[string]string{
					"creature_serial_or_handle": "37",
					"item_kind":                 "equipment",
					"equipment_type":            "[creature]",
					"pvf_path":                  "equipment/creature/chn_2018_SpringFestival/chn_400990167.equ",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save equipment: %v", err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.PetEntry{
			"37": {
				PetKey:          "37",
				CreatureKey:     uint32(testWornPetSerial),
				ItemID:          testWornPetItemID,
				SourceListType:  listTypeEquipment,
				SourceSlotIndex: currentPetWornEquipmentSlot,
				RawEntry:        make([]byte, currentItemListEntrySize),
			},
		},
	}); err != nil {
		t.Fatalf("save pet: %v", err)
	}
}

func wornPetEnchantCommand() Command {
	return NewEnchantCommand(alignedcmd.Request{
		AccountID:           "acc",
		SelectedCharacterID: 77,
	}, EnchantByBeadRequest{
		BeadListType:    listTypeMain,
		BeadSlotIndex:   testWornBeadSlot,
		TargetListType:  listTypeEquipment,
		TargetSlotIndex: currentPetWornEquipmentSlot,
	})
}

// saveWornPetSecondEnchantFixture 保留第一张宝珠 A 的全部持久投影，再从另一个
// 宝珠槽准备 B。第一次 A→A 证明既有路径不变，第二次 A→B 才会把旧实现的
// response raw/cache 留在 A。
func saveWornPetSecondEnchantFixture(t *testing.T, ctx context.Context, repos dnfrepo.Group) {
	t.Helper()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, testWornBeadSlot): {
				ItemID: 490007163,
				Count:  1,
				Extra: map[string]string{
					"item_kind": "stackable",
					"byte_12":   "2",
				},
			},
			slotKey(listTypeMain, testWornSecondBeadSlot): {
				ItemID: 490007596,
				Count:  1,
				Extra: map[string]string{
					"item_kind": "stackable",
					"byte_12":   "1",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	wornRaw := make([]byte, testWornPetRawSize)
	binary.LittleEndian.PutUint32(wornRaw[5:9], uint32(testWornPetSerial))
	binary.LittleEndian.PutUint32(wornRaw[24:28], uint32(testWornPetSerial))
	binary.LittleEndian.PutUint32(wornRaw[0x10:0x14], uint32(testWornPetCardID))
	wornRaw[0x14] = 2
	petRaw := make([]byte, currentItemListEntrySize)
	binary.LittleEndian.PutUint32(petRaw[0x0E:0x12], uint32(testWornPetCardID))
	petRaw[0x12] = 2
	wornCard := "10008668"
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"26": {
				SlotIndex: currentPetWornEquipmentSlot,
				ItemID:    testWornPetItemID,
				RawEntry:  wornRaw,
				Extra: map[string]string{
					"creature_serial_or_handle": "37",
					"item_kind":                 "equipment",
					"equipment_type":            "[creature]",
					"pvf_path":                  "equipment/creature/chn_2018_SpringFestival/chn_400990167.equ",
					"value_a":                   wornCard,
					"enchant_card_id":           wornCard,
					"pet_enchant_card_item_id":  wornCard,
					"enchant_upgrade_count":     "2",
					"pet_enchant_upgrade_count": "2",
					"byte_12":                   "2",
					"raw_entry_hex":             hex.EncodeToString(wornRaw),
				},
			},
		},
	}); err != nil {
		t.Fatalf("save equipment: %v", err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.PetEntry{
			"37": {
				PetKey:          "37",
				CreatureKey:     uint32(testWornPetSerial),
				ItemID:          testWornPetItemID,
				SourceListType:  listTypeEquipment,
				SourceSlotIndex: currentPetWornEquipmentSlot,
				RawEntry:        petRaw,
				Extra: map[string]string{
					"value_a":                   wornCard,
					"enchant_card_id":           wornCard,
					"pet_enchant_card_item_id":  wornCard,
					"enchant_upgrade_count":     "2",
					"pet_enchant_upgrade_count": "2",
					"byte_12":                   "2",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save pet: %v", err)
	}
}

func assertWornPetRawCard(t *testing.T, label string, raw []byte, card int64, upgrade byte) {
	t.Helper()
	if len(raw) < 0x15 {
		t.Fatalf("%s raw length = %d, want worn 43-byte row", label, len(raw))
	}
	if got := int64(binary.LittleEndian.Uint32(raw[0x10:0x14])); got != card {
		t.Fatalf("%s raw 0x10:0x14 = %d, want %d", label, got, card)
	}
	if got := raw[0x14]; got != upgrade {
		t.Fatalf("%s raw 0x14 = %d, want %d", label, got, upgrade)
	}
}

func assertPetEnchantAliases(t *testing.T, label string, extra map[string]string, card, upgrade string) {
	t.Helper()
	for _, key := range []string{"value_a", "enchant_card_id", "pet_enchant_card_item_id"} {
		if got := extra[key]; got != card {
			t.Fatalf("%s %s = %q, want %s; extra=%+v", label, key, got, card, extra)
		}
	}
	for _, key := range []string{"enchant_upgrade_count", "pet_enchant_upgrade_count", "byte_12"} {
		if got := extra[key]; got != upgrade {
			t.Fatalf("%s %s = %q, want %s; extra=%+v", label, key, got, upgrade, extra)
		}
	}
}

func creatureEnchantResolverWithUpgradeCounts(cardItemID, targetItemID int64, upgradeCounts ...int64) alignedcmd.EnchantBeadResolver {
	return staticEnchantResolver(alignedcmd.EnchantBeadResolution{
		CardItemID:            cardItemID,
		TargetWhitelist:       []int64{targetItemID},
		AllowedEquipmentTypes: []string{"[creature]"},
		UpgradeCounts:         append([]int64(nil), upgradeCounts...),
		TargetEquipmentType:   "[creature]",
		TargetKind:            "equipment",
	})
}

func TestStackFromWornPetEntryUsesCanonicalCreatureSerialAcrossRawLayouts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		rawSize     int
		rawSerial   uint32
		extraSerial string
		wantSerial  string
	}{
		{name: "worn_43_raw", rawSize: 43, rawSerial: 37, wantSerial: "37"},
		{name: "current_77_raw", rawSize: currentItemListEntrySize, rawSerial: 37, wantSerial: "37"},
		{name: "short_raw_extra_fallback", rawSize: 20, extraSerial: "37", wantSerial: "37"},
		{name: "zero_serial_extra_fallback", rawSize: 43, extraSerial: "37", wantSerial: "37"},
		{name: "no_serial", rawSize: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := make([]byte, tc.rawSize)
			if tc.rawSerial != 0 {
				binary.LittleEndian.PutUint32(raw[24:28], tc.rawSerial)
			}
			extra := map[string]string{"item_kind": "equipment"}
			if tc.extraSerial != "" {
				extra["creature_serial_or_handle"] = tc.extraSerial
			}
			stack := stackFromWornPetEntry(dnfrepo.EquipmentEntry{
				SlotIndex: currentPetWornEquipmentSlot,
				ItemID:    testWornPetItemID,
				RawEntry:  raw,
				Extra:     extra,
			})
			if stack.Count != 1 {
				t.Fatalf("Count = %d, want 1; amount_or_count must project the instance, not replace stack count", stack.Count)
			}
			if got := stack.Extra["amount_or_count"]; got != tc.wantSerial {
				t.Fatalf("amount_or_count = %q, want %q", got, tc.wantSerial)
			}
		})
	}
}

// 操作者报障的另一半："宠物要脱下才能打宝珠"。客户端打穿戴中宠物给的就是
// (list 3, 槽 26)，此前被目标守卫直接拒绝（现场 error=23 / 0x0110 错误 ACK）。
// 现在必须：成功、消耗 1 个宝珠、并把附魔同时写进穿戴行（raw 0x10:0x14 穿戴布局
// + Extra）与 PetEntry。
func TestOwnerEnchantWornPetCreatureConsumesBeadAndUpdatesWornRow(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveWornPetEnchantFixture(t, ctx, repos, 2)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Enchant(ctx, wornPetEnchantCommand(), creatureEnchantResolver(testWornPetCardID, testWornPetItemID))
	if err != nil {
		t.Fatalf("Enchant error = %v", err)
	}
	if !result.Success || !result.Changed || !result.WornPetTarget {
		t.Fatalf("result = %+v", result)
	}
	if result.CardItemID != testWornPetCardID || result.BeadRemainingCount != 1 {
		t.Fatalf("card/bead = %d/%d", result.CardItemID, result.BeadRemainingCount)
	}
	if result.WornPetUpdatedStack.Extra["value_a"] != "10008668" {
		t.Fatalf("worn refresh stack extra = %+v", result.WornPetUpdatedStack.Extra)
	}

	equipment, found, err := repos.Equipment.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("Load equipment found=%t err=%v", found, err)
	}
	worn := equipment.Entries["26"]
	if worn.Extra["pet_enchant_card_item_id"] != "10008668" || worn.Extra["enchant_card_id"] != "10008668" {
		t.Fatalf("worn row extra = %+v", worn.Extra)
	}
	if got := int64(binary.LittleEndian.Uint32(worn.RawEntry[0x10:0x14])); got != testWornPetCardID {
		t.Fatalf("worn row raw 0x10:0x14 = %d, want %d", got, testWornPetCardID)
	}
	if worn.RawEntry[0x14] != 0 {
		t.Fatalf("worn row raw 0x14 = %d, want 0", worn.RawEntry[0x14])
	}

	petRecord, found, err := repos.Pet.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("Load pet found=%t err=%v", found, err)
	}
	pet := petRecord.Entries["37"]
	if pet.Extra["pet_enchant_card_item_id"] != "10008668" {
		t.Fatalf("typed pet extra = %+v", pet.Extra)
	}
	if got := int64(binary.LittleEndian.Uint32(pet.RawEntry[0x0E:0x12])); got != testWornPetCardID {
		t.Fatalf("typed pet raw card = %d, want %d", got, testWornPetCardID)
	}

	inventory := loadTestInventory(t, ctx, repos, "77")
	bead, found := inventory.Slots[slotKey(listTypeMain, testWornBeadSlot)]
	if !found || bead.Count != 1 {
		t.Fatalf("bead row after worn-pet enchant = %+v found=%t", bead, found)
	}
}

// 穿戴态打宝珠必须**立即**回一条穿戴行（list 3 槽 26）的 op14 行，玩家不需要重登；
// 手里那个宝珠行同样要刷新，最后才是 0x0110 ACK。
func TestHandlerEnchantWornPetCreatureSendsWornRowThenBeadThenAck(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveWornPetEnchantFixture(t, ctx, repos, 1)

	got := handleEnchantRequest(t, repos, creatureEnchantResolver(testWornPetCardID, testWornPetItemID),
		enchantRequestBody(listTypeMain, testWornBeadSlot, listTypeEquipment, currentPetWornEquipmentSlot))
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 3 {
		t.Fatalf("result = %+v", got)
	}
	wornUpdate := got.UpperResponses[0]
	if wornUpdate.Classification != 0 || wornUpdate.MsgID != msgItemListUpdate ||
		len(wornUpdate.Body) != 3+currentItemListEntrySize || wornUpdate.Body[0] != listTypeEquipment {
		t.Fatalf("worn update = class%d msg0x%04X body=%x", wornUpdate.Classification, wornUpdate.MsgID, wornUpdate.Body)
	}
	wornRow := wornUpdate.Body[3:]
	if gotInstance := binary.LittleEndian.Uint32(wornRow[0x06:0x0A]); gotInstance != uint32(testWornPetSerial) {
		t.Fatalf("worn row creature instance = %d, want serial %d; row=%x", gotInstance, testWornPetSerial, wornRow)
	}
	if gotCard := uint32(wornRow[0x0E]) | uint32(wornRow[0x0F])<<8 | uint32(wornRow[0x10])<<16 | uint32(wornRow[0x11])<<24; gotCard != uint32(testWornPetCardID) {
		t.Fatalf("worn row card = %d, want %d", gotCard, testWornPetCardID)
	}
	beadUpdate := got.UpperResponses[1]
	if beadUpdate.Classification != 0 || beadUpdate.MsgID != msgItemListUpdate || beadUpdate.Body[0] != listTypeMain {
		t.Fatalf("bead update = class%d msg0x%04X body=%x", beadUpdate.Classification, beadUpdate.MsgID, beadUpdate.Body)
	}
	ack := got.UpperResponses[2]
	if len(ack.Body) == 0 {
		t.Fatalf("missing 0x0110 ACK: %+v", ack)
	}
}

func TestHandlerEnchantWornPetSerialOneAndNoTypedRawKeepsLatestCard(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveWornPetEnchantFixture(t, ctx, repos, 2)
	worn, _, err := repos.Equipment.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	entry := worn.Entries["26"]
	binary.LittleEndian.PutUint32(entry.RawEntry[5:9], 1)
	binary.LittleEndian.PutUint32(entry.RawEntry[24:28], 1)
	binary.LittleEndian.PutUint32(entry.RawEntry[16:20], 10008696)
	entry.Extra["creature_serial_or_handle"] = "1"
	entry.Extra["enchant_card_id"] = "10008696"
	entry.Extra["pet_enchant_card_item_id"] = "10008696"
	entry.Extra["value_a"] = "10008696"
	entry.Extra["raw_entry_hex"] = hex.EncodeToString(entry.RawEntry)
	worn.Entries["26"] = entry
	if err := repos.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "77", Entries: map[string]dnfrepo.PetEntry{
		"1": {PetKey: "1", CreatureKey: 1, ItemID: testWornPetItemID, SourceListType: listTypeEquipment,
			SourceSlotIndex: currentPetWornEquipmentSlot, RawEntry: nil,
			Extra: map[string]string{"enchant_card_id": "10008730", "pet_enchant_card_item_id": "10008730", "value_a": "10008730"}},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, card := range []int64{10008730, 10008696} {
		got := handleEnchantRequest(t, repos, creatureEnchantResolver(card, testWornPetItemID),
			enchantRequestBody(listTypeMain, testWornBeadSlot, listTypeEquipment, currentPetWornEquipmentSlot))
		if len(got.UpperResponses) != 3 {
			t.Fatalf("missing ordered refresh and ACK: %+v", got)
		}
		response := got.UpperResponses[0]
		if response.MsgID != msgItemListUpdate || len(response.Body) != 3+currentItemListEntrySize {
			t.Fatalf("bad worn update: %+v", response)
		}
		row := response.Body[3:]
		if serial := binary.LittleEndian.Uint32(row[6:10]); serial != 1 {
			t.Fatalf("serial=%d, want 1", serial)
		}
		if actual := binary.LittleEndian.Uint32(row[14:18]); actual != uint32(card) {
			t.Fatalf("response card=%d, want latest %d", actual, card)
		}
		worn, _, err = repos.Equipment.Load(ctx, "77")
		if err != nil {
			t.Fatal(err)
		}
		entry = worn.Entries["26"]
		assertWornPetRawCard(t, "serial-one durable worn", entry.RawEntry, card, 0)
		if entry.Extra["raw_entry_hex"] != hex.EncodeToString(entry.RawEntry) {
			t.Fatal("durable worn raw cache is stale")
		}
		pets, _, err := repos.Pet.Load(ctx, "77")
		if err != nil {
			t.Fatal(err)
		}
		if actual := pets.Entries["1"].Extra["enchant_card_id"]; actual != entry.Extra["enchant_card_id"] {
			t.Fatalf("typed card=%s differs from worn %s", actual, entry.Extra["enchant_card_id"])
		}
		if len(pets.Entries["1"].RawEntry) != 0 {
			t.Fatal("typed raw must remain absent for this fixture")
		}
	}
}

// 第二次替换必须覆盖第一张宝珠，并同时刷新三个容易分叉的副本：穿戴 Equipment
// 的 43-byte raw/Extra、PetEntry 的 0x77 raw/Extra，以及立即回包的
// WornPetUpdatedStack/raw_entry_hex cache。旧实现把回包 stack 取自 setEnchantFields
// 之前的 target；A->B 第二次替换时它会保留 A，而持久层已经是 B。
func TestOwnerEnchantWornPetSecondReplacementUsesNewCardAcrossRawAndCache(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveWornPetSecondEnchantFixture(t, ctx, repos)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	first, err := owner.Enchant(ctx, wornPetEnchantCommand(),
		creatureEnchantResolverWithUpgradeCounts(testWornPetCardID, testWornPetItemID, 2))
	if err != nil || !first.Success {
		t.Fatalf("first enchant result=%+v err=%v", first, err)
	}
	assertWornPetRawCard(t, "first response", first.WornPetUpdatedStack.RawEntry, testWornPetCardID, 2)

	secondCmd := wornPetEnchantCommand()
	secondCmd.BeadSlotIndex = testWornSecondBeadSlot
	second, err := owner.Enchant(ctx, secondCmd,
		creatureEnchantResolverWithUpgradeCounts(testWornPetSecondCardID, testWornPetItemID, 1))
	if err != nil || !second.Success {
		t.Fatalf("second enchant result=%+v err=%v", second, err)
	}
	if second.CardItemID != testWornPetSecondCardID || second.EnchantUpgradeCount != 1 {
		t.Fatalf("second card/upgrade = %d/%d", second.CardItemID, second.EnchantUpgradeCount)
	}

	response := second.WornPetUpdatedStack
	assertWornPetRawCard(t, "second response", response.RawEntry, testWornPetSecondCardID, 1)
	assertPetEnchantAliases(t, "second response", response.Extra, "10008713", "1")
	if got := response.Extra["amount_or_count"]; got != "37" {
		t.Fatalf("second response creature instance = %q, want 37", got)
	}
	if got := response.Extra["raw_entry_hex"]; got != hex.EncodeToString(response.RawEntry) {
		t.Fatalf("second response raw_entry_hex is stale: got=%q want=%q", got, hex.EncodeToString(response.RawEntry))
	}

	equipment, found, err := repos.Equipment.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("Load equipment found=%t err=%v", found, err)
	}
	worn := equipment.Entries["26"]
	assertWornPetRawCard(t, "persisted equipment", worn.RawEntry, testWornPetSecondCardID, 1)
	assertPetEnchantAliases(t, "persisted equipment", worn.Extra, "10008713", "1")
	if _, persisted := worn.Extra["amount_or_count"]; persisted {
		t.Fatal("response-only creature instance marker leaked into persisted EquipmentEntry")
	}
	if got := worn.Extra["raw_entry_hex"]; got != hex.EncodeToString(worn.RawEntry) {
		t.Fatalf("persisted equipment raw_entry_hex = %q, want %q", got, hex.EncodeToString(worn.RawEntry))
	}
	projected := stackFromWornPetEntry(worn)
	assertWornPetRawCard(t, "persisted equipment projection", projected.RawEntry, testWornPetSecondCardID, 1)
	assertPetEnchantAliases(t, "persisted equipment projection", projected.Extra, "10008713", "1")
	if got := projected.Extra["raw_entry_hex"]; got != hex.EncodeToString(projected.RawEntry) {
		t.Fatalf("projected equipment raw_entry_hex = %q, want %q", got, hex.EncodeToString(projected.RawEntry))
	}

	petRecord, found, err := repos.Pet.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("Load pet found=%t err=%v", found, err)
	}
	pet := petRecord.Entries["37"]
	if got := int64(binary.LittleEndian.Uint32(pet.RawEntry[0x0E:0x12])); got != testWornPetSecondCardID {
		t.Fatalf("persisted PetEntry raw card = %d, want %d", got, testWornPetSecondCardID)
	}
	if got := pet.RawEntry[0x12]; got != 1 {
		t.Fatalf("persisted PetEntry raw upgrade = %d, want 1", got)
	}
	assertPetEnchantAliases(t, "persisted PetEntry", pet.Extra, "10008713", "1")
	if _, persisted := pet.Extra["amount_or_count"]; persisted {
		t.Fatal("response-only creature instance marker leaked into persisted PetEntry")
	}

	inventory := loadTestInventory(t, ctx, repos, "77")
	for _, slot := range []int16{testWornBeadSlot, testWornSecondBeadSlot} {
		if _, found := inventory.Slots[slotKey(listTypeMain, slot)]; found {
			t.Fatalf("exhausted second-enchant bead slot %d was not removed", slot)
		}
	}
}
