package equip

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 线上真行（dnf_equipment_entries 6/12，item 101040493，PVF `[durability] 48`）：
//
//	0C00 6DC10506 8335302C 00 30 00 00 6DC10506 …
//	槽12 itemID   实例     ^^ ^^ ^^ ^^
//	                       |  |  |  └ +0x0D 封印
//	                       |  |  └ +0x0C 耐久高字节 0
//	                       |  └ +0x0B 耐久低字节 0x30 = 48（真值）
//	                       └ +0x0A ext/强化字节 0（该角色未强化）
const liveRepairRaw77Hex = "0c006dc105068335302c003000006dc105060000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// 同族 +15 行（dnf_equipment_entries 15/12，item 101040493）：+0x0A=0x0F 是 +15
// 强化字节，+0x0B..0x0C=0x0030=48 才是耐久。线上日志逐字
// `item=101040493 durability=12303->12303 cost=0 changed=false` 的 12303 =
// 0x0F | 0x30<<8，就是"读 +0x0A 起 u16"的结果。
const livePlusFifteenWeaponHex = "0c006dc10506fec99a3b0f300000a2d198000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

func mustDecodeRepairRaw(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode raw row: %v", err)
	}
	if len(raw) != currentEquipmentRawEntryWireSize {
		t.Fatalf("raw row size = %d, want %d", len(raw), currentEquipmentRawEntryWireSize)
	}
	return raw
}

// damagedRepairRaw77Weapon 用线上那一行的字节形状造一把"当前耐久 20 / 上限 48、+15"
// 的 0x77 武器行，供修理断言使用。
func damagedRepairRaw77Weapon(itemID int64, durability uint16, packedExt byte) []byte {
	raw := make([]byte, currentEquipmentRawEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], 12)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[6:10], 999999998)
	raw[0x0A] = packedExt
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], durability)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(itemID))
	return raw
}

func repairPlusFifteenResolver() alignedcmd.RepairCostResolver {
	// MaxDurability 48、RepairPrice 4800、Grade 20、rate 0.08、加成档全 1：
	// lost=28 → 4800*(20+5)/10=12000 → 12000*0.08/48*28 = 560。
	return repairTestResolver(48, 4800, 20)
}

// TestRepairDurabilityWordOffsetForksOnRaw77Row 是字节级判据测试：
// 同一个 raw 前 12 字节，0x77 行的耐久字在 +0x0B，legacy 行在 +10。
func TestRepairDurabilityWordOffsetForksOnRaw77Row(t *testing.T) {
	raw := mustDecodeRepairRaw(t, livePlusFifteenWeaponHex)

	offset, ok := RepairDurabilityWordOffset(raw, 101040493)
	if !ok || offset != RepairDurabilityOffsetCurrentRaw77 {
		t.Fatalf("0x77 row offset = (%d,%t), want (%d,true)", offset, ok, RepairDurabilityOffsetCurrentRaw77)
	}
	word, ok := RepairDurabilityWord(raw, 101040493)
	if !ok || word != 48 {
		t.Fatalf("0x77 row durability = (%d,%t), want (48,true)", word, ok)
	}
	// 同一串字节按"读 +0x0A 起 u16"读出来就是线上那条日志的 12303。
	if legacy := int64(uint16(raw[10]) | uint16(raw[11])<<8); legacy != 12303 {
		t.Fatalf("legacy offset read = %d, want 12303 (the live log value)", legacy)
	}

	// itemID 与 +2 不符（同长度的占位/异形行）必须退回 legacy 偏移，不能按 0x77 读。
	alias := mustDecodeRepairRaw(t, livePlusFifteenWeaponHex)
	if offset, ok := RepairDurabilityWordOffset(alias, 999); !ok || offset != RepairDurabilityOffsetLegacySummary {
		t.Fatalf("mismatched itemID offset = (%d,%t), want (%d,true)", offset, ok, RepairDurabilityOffsetLegacySummary)
	}
	// legacy 摘要行（43/82 字节）继续按 +10 读。
	legacyRow := make([]byte, 43)
	binary.LittleEndian.PutUint32(legacyRow[1:5], 101040493)
	binary.LittleEndian.PutUint16(legacyRow[10:12], 31)
	if word, ok := RepairDurabilityWord(legacyRow, 101040493); !ok || word != 31 {
		t.Fatalf("legacy summary durability = (%d,%t), want (31,true)", word, ok)
	}
	// 放不下 +10..11 的行维持"证据不足"。
	if _, ok := RepairDurabilityWordOffset([]byte{0, 1, 2}, 101040493); ok {
		t.Fatal("3-byte row must not produce a durability offset")
	}
}

// TestOwnerRepairPlusFifteenWeaponKeepsExtByteAndRestoresDurabilityWord 是本条修复的
// 验收断言：+15 武器修理后 +0x0A（强化等级字节）**不变**、+0x0B..0x0C 等于 PVF 上限，
// 并且真的扣了金币。
func TestOwnerRepairPlusFifteenWeaponKeepsExtByteAndRestoresDurabilityWord(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	raw := damagedRepairRaw77Weapon(101040493, 20, 0x0F)
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101040493, RawEntry: raw},
		},
	})

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	result, err := owner.Repair(ctx, RepairCommand{SelectedCharacterID: 77, SlotIndex: 12}, repairPlusFifteenResolver())
	if err != nil {
		t.Fatalf("Repair error = %v", err)
	}
	if !result.Changed || result.OldDurability != 20 || result.NewDurability != 48 || result.Cost != 560 ||
		result.UpdatedGold != 4440 || result.ItemID != 101040493 {
		t.Fatalf("result = %+v, want changed old=20 new=48 cost=560 gold=4440", result)
	}

	entry := loadTestEquipment(t, ctx, repos, "77").Entries["12"]
	if got := entry.RawEntry[0x0A]; got != 0x0F {
		t.Fatalf("raw[0x0A] (强化/ext 字节) = 0x%02X, want 0x0F unchanged", got)
	}
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != 48 {
		t.Fatalf("raw[0x0B:0x0D] durability = %d, want 48 (PVF [durability])", got)
	}
	if got := entry.Extra["durability"]; got != "48" {
		t.Fatalf("extra durability = %q, want 48", got)
	}
	// 写入侧绝不允许再碰 +0x0A/+0x0B 之外的形状：+0x0D 封印位与 +0x0E 源实例保持原样。
	if got := binary.LittleEndian.Uint32(entry.RawEntry[0x0E:0x12]); got != 101040493 {
		t.Fatalf("raw[0x0E:0x12] = %d, want 101040493 untouched", got)
	}
	if got := raw[0x0A]; got != 0x0F {
		t.Fatalf("caller raw was mutated: raw[0x0A]=0x%02X", got)
	}

	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 4440 {
		t.Fatalf("gold = %d, want 4440", character.Stats["gold"])
	}
}

// TestOwnerRepairZeroDurabilityWeaponRestoresWordWithoutTouchingExtByte 覆盖最坏形状：
// 耐久字已经是 0（客户端显示损坏）。旧写入会把上限塞进 +0x0A、把 +0x0B 清零
// ——"收钱不修、还改强化等级"；修好后 +0x0A 必须原样、+0x0B..0x0C 必须拉满。
func TestOwnerRepairZeroDurabilityWeaponRestoresWordWithoutTouchingExtByte(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	raw := damagedRepairRaw77Weapon(101040493, 0, 0x0F)
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101040493, RawEntry: raw},
		},
	})

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	result, err := owner.Repair(ctx, RepairCommand{SelectedCharacterID: 77, SlotIndex: 12}, repairPlusFifteenResolver())
	if err != nil {
		t.Fatalf("Repair error = %v", err)
	}
	if !result.Changed || result.OldDurability != 0 || result.NewDurability != 48 {
		t.Fatalf("result = %+v, want changed old=0 new=48", result)
	}
	entry := loadTestEquipment(t, ctx, repos, "77").Entries["12"]
	if got := entry.RawEntry[0x0A]; got != 0x0F {
		t.Fatalf("raw[0x0A] = 0x%02X, want 0x0F (old write would have put 0x30 here)", got)
	}
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != 48 {
		t.Fatalf("raw[0x0B:0x0D] = %d, want 48", got)
	}
}

// TestOwnerRepairAllRepairsPlusFifteenWeaponAndChargesGold 是端到端（op23 repair-all →
// equip owner）断言：损坏的 +15 0x77 武器被修满并扣钱，已满的行不动。
func TestOwnerRepairAllRepairsPlusFifteenWeaponAndChargesGold(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101040493, RawEntry: damagedRepairRaw77Weapon(101040493, 20, 0x0F)},
			"14": {SlotIndex: 14, ItemID: 101040494, RawEntry: damagedRepairRaw77Weapon(101040494, 48, 0x03)},
		},
	})

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	result, err := owner.RepairAll(ctx, RepairCommand{SelectedCharacterID: 77, SlotIndex: -1}, repairPlusFifteenResolver())
	if err != nil {
		t.Fatalf("RepairAll error = %v", err)
	}
	if !result.Changed || result.RepairedCount != 1 || result.Cost != 560 || result.UpdatedGold != 4440 {
		t.Fatalf("result = %+v, want repaired=1 cost=560 gold=4440", result)
	}
	entries := loadTestEquipment(t, ctx, repos, "77").Entries
	if got := binary.LittleEndian.Uint16(entries["12"].RawEntry[0x0B:0x0D]); got != 48 {
		t.Fatalf("slot 12 durability = %d, want 48", got)
	}
	if got := entries["12"].RawEntry[0x0A]; got != 0x0F {
		t.Fatalf("slot 12 raw[0x0A] = 0x%02X, want 0x0F", got)
	}
	// 已满的那一行一个字节都不许动。
	if got := entries["14"].RawEntry[0x0A]; got != 0x03 {
		t.Fatalf("slot 14 raw[0x0A] = 0x%02X, want 0x03 untouched", got)
	}
	if got := binary.LittleEndian.Uint16(entries["14"].RawEntry[0x0B:0x0D]); got != 48 {
		t.Fatalf("slot 14 durability = %d, want 48 untouched", got)
	}
}

// TestOwnerRepairLegacyOffsetModeIsByteForByteTheOldBehavior 是回退档断言：
// `DNFBRIDGE_REPAIR_DURABILITY_OFFSET=legacy` 必须逐字节复现改动前的行为 ——
// 读 +0x0A 起 u16（= 12303）⇒ 恒 >= 上限 ⇒ 不修、不扣钱、raw 一个字节不动。
func TestOwnerRepairLegacyOffsetModeIsByteForByteTheOldBehavior(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	raw := damagedRepairRaw77Weapon(101040493, 20, 0x0F)
	before := append([]byte(nil), raw...)
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101040493, RawEntry: raw},
		},
	})

	t.Setenv(RepairDurabilityOffsetEnvKey, "legacy")

	if word, ok := RepairDurabilityWord(raw, 101040493); !ok || word != 5135 {
		t.Fatalf("legacy mode durability = (%d,%t), want (5135,true) = 0x0F|0x14<<8", word, ok)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	result, err := owner.Repair(ctx, RepairCommand{SelectedCharacterID: 77, SlotIndex: 12}, repairPlusFifteenResolver())
	if err != nil {
		t.Fatalf("Repair error = %v", err)
	}
	if result.Changed || result.OldDurability != 5135 || result.NewDurability != 5135 ||
		result.Cost != 0 || result.UpdatedGold != 5000 {
		t.Fatalf("legacy result = %+v, want changed=false old=new=5135 cost=0 gold=5000", result)
	}
	entry := loadTestEquipment(t, ctx, repos, "77").Entries["12"]
	if string(entry.RawEntry) != string(before) {
		t.Fatalf("legacy mode mutated the row: % X", entry.RawEntry)
	}
	if _, found := entry.Extra["durability"]; found {
		t.Fatalf("legacy mode wrote durability evidence: %+v", entry.Extra)
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 5000 {
		t.Fatalf("legacy mode charged gold: %d", character.Stats["gold"])
	}
}

// TestOwnerRepairFullLivePlusFifteenWeaponReportsItsRealDurability 用线上真行的字节：
// 已满的 +15 武器（48/48）在修好后应该报 `durability=48->48 changed=false`，
// 而不是改动前那条 `durability=12303->12303`。
func TestOwnerRepairFullLivePlusFifteenWeaponReportsItsRealDurability(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	raw := mustDecodeRepairRaw(t, livePlusFifteenWeaponHex)
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 110852507}})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101040493, RawEntry: raw},
		},
	})

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	result, err := owner.Repair(ctx, RepairCommand{SelectedCharacterID: 77, SlotIndex: 12}, repairPlusFifteenResolver())
	if err != nil {
		t.Fatalf("Repair error = %v", err)
	}
	if result.Changed || result.OldDurability != 48 || result.NewDurability != 48 || result.Cost != 0 {
		t.Fatalf("result = %+v, want changed=false 48->48 cost=0", result)
	}

	// 同一行字节在未强化的角色（+0x0A=0）上同样读 48，证明判据不是"强化字节恰好为 0"。
	unenhanced := mustDecodeRepairRaw(t, liveRepairRaw77Hex)
	if word, ok := RepairDurabilityWord(unenhanced, 101040493); !ok || word != 48 {
		t.Fatalf("unenhanced live row durability = (%d,%t), want (48,true)", word, ok)
	}
	if legacy := int64(uint16(unenhanced[10]) | uint16(unenhanced[11])<<8); legacy != 12288 {
		t.Fatalf("unenhanced legacy read = %d, want 12288", legacy)
	}
}
