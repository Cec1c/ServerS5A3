package equip

import (
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 操作者本次报障：宝珠打上后**穿在身上正常**，但**脱下**后在背包里看不到宝珠词条。
// 原因：穿戴行是 43 字节布局（宝珠 id 在 0x10:0x14、强化数在 0x14），背包行是 0x77
// 布局（id 在 0x0E:0x12、强化数在 0x12）；脱下方向此前只 cloneExtra，没做这个搬运。
func TestStackFromEntryCarriesPetEnchantIntoInventoryRowLayout(t *testing.T) {
	const cardID uint32 = 10008668
	wornRaw := make([]byte, 43)
	binary.LittleEndian.PutUint32(wornRaw[24:28], 1) // 宠物 serial
	binary.LittleEndian.PutUint32(wornRaw[0x10:0x14], cardID)
	wornRaw[0x14] = 3

	stack := stackFromEntry(dnfrepo.EquipmentEntry{
		SlotIndex: petCreatureWornSlot,
		ItemID:    400990167,
		RawEntry:  wornRaw,
		Extra: map[string]string{
			"equipment_type": "[creature]",
			"item_kind":      "equipment",
		},
	})

	if len(stack.RawEntry) != 0x77 {
		t.Fatalf("inventory row raw length = %d, want 0x77", len(stack.RawEntry))
	}
	if got := binary.LittleEndian.Uint32(stack.RawEntry[0x0E:0x12]); got != cardID {
		t.Fatalf("inventory row raw 0x0E:0x12 = %d, want %d", got, cardID)
	}
	if stack.RawEntry[0x12] != 3 {
		t.Fatalf("inventory row raw 0x12 = %d, want 3", stack.RawEntry[0x12])
	}
	if stack.Extra["value_a"] != "10008668" || stack.Extra["pet_enchant_card_item_id"] != "10008668" {
		t.Fatalf("inventory row extra = %+v", stack.Extra)
	}
	if stack.Extra["enchant_upgrade_count"] != "3" || stack.Extra["byte_12"] != "3" {
		t.Fatalf("inventory row upgrade extra = %+v", stack.Extra)
	}
}

// 现场对照：普通装备的宝珠穿脱一直是正常的（操作者确认），所以这条链路
// "stack -> entry -> stack" 必须原样保留附魔，宠物侧的改动不许踩坏它。
func TestOrdinaryEquipmentEnchantSurvivesStackEntryRoundTrip(t *testing.T) {
	const cardID uint32 = 10008663
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], cardID)
	raw[0x12] = 2
	original := dnfrepo.ItemStack{
		ItemID:   700,
		Count:    1,
		RawEntry: raw,
		Extra: map[string]string{
			"item_kind":             "equipment",
			"value_a":               "10008663",
			"enchant_card_id":       "10008663",
			"enchant_upgrade_count": "2",
			"byte_12":               "2",
		},
	}

	entry, err := entryFromStack(original, wireListMain, 10, 0)
	if err != nil {
		t.Fatalf("entryFromStack: %v", err)
	}
	roundTripped := stackFromEntry(entry)
	if got := binary.LittleEndian.Uint32(roundTripped.RawEntry[0x0E:0x12]); got != cardID {
		t.Fatalf("round-trip raw card = %d, want %d", got, cardID)
	}
	if roundTripped.RawEntry[0x12] != 2 {
		t.Fatalf("round-trip raw upgrade = %d, want 2", roundTripped.RawEntry[0x12])
	}
	if roundTripped.Extra["enchant_card_id"] != "10008663" || roundTripped.Extra["enchant_upgrade_count"] != "2" {
		t.Fatalf("round-trip extra = %+v", roundTripped.Extra)
	}
}
