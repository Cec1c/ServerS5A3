package equip

import (
	"encoding/binary"
	"os"
	"strings"
)

// 修理链路的耐久字偏移 —— 0x77 行与 legacy 摘要行相差 1 个字节。
//
// 现场铁证（线上 DB + packet_log.txt 原文，见 _dist/repair-durability-offset.md）：
//
//	武器 101040493（PVF `[durability] 48`）的 0x77 行
//	  0C00 6DC10506 311FBC2A 0F 30 00 00 A2D19800 …
//	  槽   itemID   实例       ^^ ^^ ^^ ^^
//	                            |  |  |  └ +0x0D 封印字节
//	                            |  |  └ +0x0C 耐久字高字节
//	                            |  └ +0x0B 耐久字低字节 = 0x30 = 48（真值）
//	                            └ +0x0A 强化/ext 字节 = 0x0F（+15）
//
// 修理链路读的是 `raw[10] | raw[11]<<8` = 0x0F | 0x30<<8 = 12303，写的是
// `raw[10]/raw[11]` —— 那是 **legacy 摘要行的偏移**（43 字节 create-equipment 摘要：
// 强化 @+9、耐久 @+10；0x77 行是它整体 +1 的布局：强化 @+0x0A、耐久 @+0x0B）。
// 同一结论在本树里有四处独立互证：
//   - dnfbridge/avatar_attribute_row.go:20-37（参考实现 `AttrOffset=9` /
//     `DurabilityOffset=10` / `SealFlagOffset=12`，且 43 字节摘要整体 +1 即 0x77 行）；
//   - dnfbridge/current_item_list_stack_projection.go:161-171（43 字节摘要被当成
//     0x77 行时，耐久低字节会落到客户端渲染为强化等级的 +0x0A）；
//   - dnfbridge/current_item_list_equipment_projection.go:26,57,186（耐久写在 +0x0B）；
//   - dnfbridge/equipment_quality.go:30-34 与 current_item_list_types.go:3
//     （0x77 行判据：整行 0x77 字节 + itemID @+2）。
//
// 因此这里按**行格式分叉**：0x77 行读/写 +0x0B，legacy 行维持 +10（逐字节不变）。
// 一刀切改偏移会把 legacy 行弄坏（legacy 行的 +0x0B 是别的字段）。
const (
	// RepairDurabilityOffsetCurrentRaw77 是 0x77 行里耐久字的字节偏移（+0x0B..0x0C）。
	RepairDurabilityOffsetCurrentRaw77 = 0x0B
	// RepairDurabilityOffsetLegacySummary 是 legacy 摘要行（43/82 字节）里的耐久字偏移。
	RepairDurabilityOffsetLegacySummary = 10
	// repairDurabilityWordSize 是耐久字宽度（u16）。
	repairDurabilityWordSize = 2
)

// RepairDurabilityOffsetEnvKey 是这条修复的回退开关。默认（缺省/空/其它值）= 修好后的
// 行为：0x77 行分叉到 +0x0B。写 `legacy` = 逐字节回到改动前的行为（一律按 legacy 偏移
// 读/写 raw[10:12]，包括 0x77 行）。
const RepairDurabilityOffsetEnvKey = "DNFBRIDGE_REPAIR_DURABILITY_OFFSET"

func repairDurabilityOffsetLegacyMode() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(RepairDurabilityOffsetEnvKey)), "legacy")
}

// RepairDurabilityRawEntryIsCurrentRaw77 与 dnfbridge.currentEquipmentEntryHasCurrentRaw77
// 同一判据（长度整 0x77 + itemID 落在 +2），供 modules 层复用而不反向依赖 dnfbridge。
func RepairDurabilityRawEntryIsCurrentRaw77(raw []byte, itemID int64) bool {
	return len(raw) == currentEquipmentRawEntryWireSize &&
		itemID > 0 &&
		binary.LittleEndian.Uint32(raw[2:6]) == uint32(itemID)
}

// RepairDurabilityWordOffset 返回 raw 里耐久字的字节偏移：
//   - 0x77 行（长度 0x77 且 itemID @+2 相符）→ +0x0B；
//   - 其余（legacy 摘要行 / 未定形行）→ +10（与改动前逐字节相同）；
//   - 连 +10 都放不下（len < 12）→ ok=false（调用方维持原有的"证据不足"失败关闭）。
//
// `DNFBRIDGE_REPAIR_DURABILITY_OFFSET=legacy` 让 0x77 行也走 legacy 偏移（回退档）。
func RepairDurabilityWordOffset(raw []byte, itemID int64) (int, bool) {
	if !repairDurabilityOffsetLegacyMode() && RepairDurabilityRawEntryIsCurrentRaw77(raw, itemID) {
		return RepairDurabilityOffsetCurrentRaw77, true
	}
	if len(raw) >= RepairDurabilityOffsetLegacySummary+repairDurabilityWordSize {
		return RepairDurabilityOffsetLegacySummary, true
	}
	return 0, false
}

// RepairDurabilityWord 读当前耐久字。与 SetRepairDurabilityWord 同一个偏移（读写一致）。
func RepairDurabilityWord(raw []byte, itemID int64) (int64, bool) {
	offset, ok := RepairDurabilityWordOffset(raw, itemID)
	if !ok {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint16(raw[offset : offset+repairDurabilityWordSize])), true
}

// SetRepairDurabilityWord 把耐久字写回 raw —— 与 RepairDurabilityWord 同一个偏移，
// 且**绝不触碰 +0x0A**（客户端把 +0x0A 渲染成强化等级：旧写法把上限写进 +0x0A，
// 会让 +15 武器显示成别的强化等级，同时把耐久字的低字节清零）。
// 返回 false = 这一行放不下耐久字，调用方按原逻辑跳过/失败关闭。
func SetRepairDurabilityWord(raw []byte, itemID int64, value int64) bool {
	offset, ok := RepairDurabilityWordOffset(raw, itemID)
	if !ok {
		return false
	}
	binary.LittleEndian.PutUint16(raw[offset:offset+repairDurabilityWordSize], uint16(value))
	return true
}
