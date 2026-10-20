package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是 operator ⑨「克隆装扮未显示克隆对象时装外观」**第二落点**排查的失败关闭证据，
// 它不引入任何新的协议投影，只把"克隆来源该写在哪一行的哪一个偏移"这件事钉成可执行的字节级断言。
//
// 结论（详见交付报告）：
//
//  1. 本仓库的**物品列表行**（`currentItemListEntry`，0x77 字节，随 msg13 整容器 / msg14 单行下发）
//     其头部逐字段等于厂商 `ItemListProtocolWriter.WriteEntry84`
//     （Network/Builders/Inventory/ItemListProtocolWriter.cs:145-170）：
//     slot u16@0x00、itemId i32@0x02、value i32@0x06、Attr u8@0x0A、durability u16@0x0B、
//     **SealFlag u8@0x0D**（同文件:162）、**EnchantCardId i32@0x0E**（同文件:183）、
//     EnchantUpgradeCount u8@0x12（:184）、AmplifyType u8@0x13（:185）、AmplifyValue u16@0x14（:186）。
//     本仓库自己的持久 0x77 行构造器（internal/modules/dnf/equip/owner.go:1679-1687）同样把
//     `raw[0x0D]` 写成 1 字节 seal、把 `raw[0x0E:0x12]` 写成 u32 实例值；
//     0x54 单行更新行（currentEquipmentUpdateEntry）的封印字节同样只有 1 字节
//     （current_item_list_equipment_projection.go:59），其 expire 偏移 0x2B 又正好等于
//     WriteEntry84 里 chronicle 块之后的 expire 偏移（14+8+4+17=43=0x2B）。
//     ⇒ **0x77/0x54 行里没有"ClearAvatarId 的 u32"这个位置**：0x0D 只有 1 字节，
//     0x0E 起是一个已被证明的独立 u32 字段（客户端按 raw+0x0E 读道具身份，
//     见 equipment_quality.go:79-83 的 op44 现场结论）。
//
//  2. 厂商那条「ClearAvatarId（克隆来源）u32 @ 12」属于**另一个记录**：
//     `WriteNoti2EquippedEntry`（同文件:19-54，第 38 行）只被 USERINFO（NotiPacketTypeA21.USERINFO
//     = 0x0002 = msg_id 2，Network/Core/PacketTypesA21.cs:9）的 subtype1/subtype3 使用
//     （Init/UserInfoSubtype1Builder.cs:44、Init/UserInfoSubtype3Builder.cs:108）。
//     本仓库对应的写入器是 userinfo_current_equipment_wire.go:11-19 的
//     `writeCurrentMode1EquipmentCreateRow`：slot u8@0、itemID u32@1、instance u32@5、extData u8@9、
//     durability u16@10、**u32 @12**，并且该 u32 已经由既有改动接到克隆来源键上
//     （userinfo_current_equipment_projection.go:130 → clone_avatar.go:177
//     `currentMode1EquipmentLinkedItemID`）。现场取证（packet_log.20260927-094833.txt，
//     conn game-000020 / 角色 6）里这些创建行正好是 64 字节步长、耐久字在 +10
//     （日志 row_summary`3:42609:...durability=47` 与行内 0x2F 一致）、改动前 +12 恒为 0。
//
//  所以"把克隆来源写成 0x77 行 +0x0D 的 u32"这条路线在本仓库的行形状里**没有位置**：
//  它会同时覆盖 1 字节封印位和 +0x0E 那个已被证明的 u32 的前 3 个字节。本文件把这条
//  "失败关闭"决定钉住：任何人再想这么写，都会让下面的断言变红。

const (
	// cloneAvatarRowLayoutSourceID 是"被克隆的那件普通装扮"，也就是克隆键的值。
	cloneAvatarRowLayoutSourceID uint32 = 101500759
	// cloneAvatarRowLayoutValueA 是 0x77 行 +0x0E 的 u32 独立字段现场值（本用例任意取值，
	// 只要与封印字节、与克隆来源都不同即可区分）。
	cloneAvatarRowLayoutValueA uint32 = 0x11223344
)

// cloneAvatarRowLayoutUpdatePrefix 是 0x54 单行更新行 +0x0E 起的 8 字节"附魔块"
// （厂商 WriteEntry84 的 EnchantCardId i32 + EnchantUpgradeCount u8 + AmplifyType u8 +
// AmplifyValue u16）。
var cloneAvatarRowLayoutUpdatePrefix = []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04}

// cloneAvatarRowLayoutWornEntry 造一条"穿着稀有克隆装扮"的穿戴行：ItemID = 克隆件本身，
// Extra 里带克隆来源键，同时带封印位与实例值。
func cloneAvatarRowLayoutWornEntry() dnfrepo.EquipmentEntry {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0x00:0x02], 3)
	binary.LittleEndian.PutUint32(raw[0x02:0x06], uint32(cloneAvatarProjectionCloneID))
	copy(raw[0x0E:0x16], cloneAvatarRowLayoutUpdatePrefix)
	return dnfrepo.EquipmentEntry{
		SlotIndex: 3,
		ItemID:    cloneAvatarProjectionCloneID,
		RawEntry:  raw,
		Extra: map[string]string{
			"current_exe_equipment_type":          "3",
			dnfrepo.CloneAvatarAppearanceExtraKey: "101500759",
			"seal_flag":                           "1",
			"equipment_update_seal_flag":          "1",
			"value_a":                             "287454020", // 0x11223344
		},
	}
}

// TestCurrentItemListWornRowKeepsSealByteAt0x0DAndValueADWordAt0x0E 钉住 0x77 穿戴行与
// 0x54 单行更新行的宽度事实：即使这一行是带克隆来源的克隆装扮，
// +0x0D 仍然只有 1 字节封印位、+0x0E 起的 u32 仍然是它自己的独立字段。
// 这条断言就是"0x0D 当 u32 写克隆来源"这条失败关闭路线的闸门。
func TestCurrentItemListWornRowKeepsSealByteAt0x0DAndValueADWordAt0x0E(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	entry := cloneAvatarRowLayoutWornEntry()

	listRow, ok := currentItemListEntryFromEquipment(entry)
	if !ok {
		t.Fatal("cloned worn avatar row was dropped by the 0x77 equipment projection")
	}
	if got := binary.LittleEndian.Uint16(listRow.data[0x00:0x02]); got != 3 {
		t.Fatalf("0x77 row slot = %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint32(listRow.data[0x02:0x06]); got != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("0x77 row item id = %d, want the clone itself %d", got, cloneAvatarProjectionCloneID)
	}
	if got := listRow.data[0x0D]; got != 1 {
		t.Fatalf("0x77 row +0x0D (1-byte seal/bind) = %#x, want 0x01", got)
	}
	if got := binary.LittleEndian.Uint32(listRow.data[0x0E:0x12]); got != cloneAvatarRowLayoutValueA {
		t.Fatalf("0x77 row +0x0E u32 = %#x, want valueA %#x", got, cloneAvatarRowLayoutValueA)
	}
	// 如果把克隆来源当成 +0x0D 的 u32 写进去，这个 4 字节窗口就会变成 101500759。
	if got := binary.LittleEndian.Uint32(listRow.data[0x0D:0x11]); got == cloneAvatarRowLayoutSourceID {
		t.Fatalf("0x77 row +0x0D..0x10 reads as the clone source %d: the 1-byte seal field and the "+
			"proved +0x0E u32 were overwritten by a clear-avatar dword that this row shape has no room for",
			cloneAvatarRowLayoutSourceID)
	}

	updateRow := currentEquipmentUpdateEntryFromEquipment(entry)
	if got := binary.LittleEndian.Uint16(updateRow.data[0x00:0x02]); got != 3 {
		t.Fatalf("0x54 update row slot = %d, want 3", got)
	}
	if got := updateRow.data[0x0D]; got != 1 {
		t.Fatalf("0x54 update row +0x0D (1-byte seal) = %#x, want 0x01", got)
	}
	if got := updateRow.data[0x0E:0x16]; !bytes.Equal(got, cloneAvatarRowLayoutUpdatePrefix) {
		t.Fatalf("0x54 update row +0x0E..0x15 = % x, want the 8-byte enchant block % x",
			got, cloneAvatarRowLayoutUpdatePrefix)
	}
	if got := binary.LittleEndian.Uint32(updateRow.data[0x0D:0x11]); got == cloneAvatarRowLayoutSourceID {
		t.Fatalf("0x54 update row +0x0D..0x10 reads as the clone source %d: same width conflict as the 0x77 row",
			cloneAvatarRowLayoutSourceID)
	}
}

// TestCurrentMode1CreateRowCarriesCloneSourceAtVendorNoti2Offset12 是跨层接线用例
// （穿戴记录 → 行投影 → op 行字节）：厂商在 USERINFO(msg_id=2) 的装备 entry 里把
// 克隆来源写在 **+12 的 u32**（WriteNoti2EquippedEntry:38 →
// ResolveNoti2ClearAvatarOrSeal:299-307）。本仓库承载这条规则的记录是 mode1 创建行，
// 且偏移与 64 字节行宽与现场抓包一致（packet_log.20260927-094833.txt）。
func TestCurrentMode1CreateRowCarriesCloneSourceAtVendorNoti2Offset12(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	record := cloneAvatarProjectionFixture(t, map[string]string{
		dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
	})

	createRows := currentMode1EquipmentCreateRows(newCloneAvatarMode1Rows(t, record))
	if len(createRows) != 1 {
		t.Fatalf("mode1 create rows = %d, want 1 (the cloned avatar row)", len(createRows))
	}
	var writer packetWriter
	writeCurrentMode1EquipmentCreateRow(&writer, createRows[0])
	row := writer.bytes()

	// 现场行宽：avatar 行 readsRawBlocks=true（userinfo_current_equipment_rules.go:150-164，
	// actor slot <= 0x0b），基行 56 字节 + 两段 4 字节空 raw 块 = 64 字节。
	if len(row) != 64 {
		t.Fatalf("mode1 create row bytes = %d, want 64 (live packet_log stride for avatar rows)", len(row))
	}
	if row[0] != 3 {
		t.Fatalf("mode1 create row slot byte = %d, want 3", row[0])
	}
	if got := binary.LittleEndian.Uint32(row[1:5]); got != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("mode1 create row item id @+1 = %d, want the clone itself %d", got, cloneAvatarProjectionCloneID)
	}
	if got := binary.LittleEndian.Uint32(row[12:16]); got != cloneAvatarProjectionAppearanceID {
		t.Fatalf("mode1 create row @+12 = %d, want the cloned source %d (vendor clear-avatar dword offset)",
			got, cloneAvatarProjectionAppearanceID)
	}

	// 回退档：同一个夹具在 DNFBRIDGE_CLONE_AVATAR_APPEARANCE=off 下，@+12 逐字节回到改动前的 0。
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOff)
	rolled := currentMode1EquipmentCreateRows(newCloneAvatarMode1Rows(t, record))
	if len(rolled) != 1 {
		t.Fatalf("rollback mode1 create rows = %d, want 1", len(rolled))
	}
	var rollbackWriter packetWriter
	writeCurrentMode1EquipmentCreateRow(&rollbackWriter, rolled[0])
	rollbackRow := rollbackWriter.bytes()
	if got := binary.LittleEndian.Uint32(rollbackRow[12:16]); got != 0 {
		t.Fatalf("rollback mode1 create row @+12 = %d, want 0 (pre-change bytes)", got)
	}
	if got := binary.LittleEndian.Uint32(rollbackRow[1:5]); got != uint32(cloneAvatarProjectionCloneID) {
		t.Fatalf("rollback mode1 create row item id @+1 = %d, want the clone itself %d", got, cloneAvatarProjectionCloneID)
	}
}
