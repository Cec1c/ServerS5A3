package inventory

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	testWornPetItemID  int64 = 400990167
	testWornPetSerial  int64 = 1
	testWornPetCardID  int64 = 10008668
	testWornBeadSlot   int16 = 12
	testWornPetRawSize       = 43
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
	binary.LittleEndian.PutUint32(wornRaw[24:28], uint32(testWornPetSerial))
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"26": {
				SlotIndex: currentPetWornEquipmentSlot,
				ItemID:    testWornPetItemID,
				RawEntry:  wornRaw,
				Extra: map[string]string{
					"creature_serial_or_handle": "1",
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
			"1": {
				PetKey:          "1",
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
	pet := petRecord.Entries["1"]
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
