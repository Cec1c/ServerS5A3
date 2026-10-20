package equip

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场回归：**登录/重登时穿着的武器被卸下**。
//
// 操作者原话「登陆游戏会卸除身上武器」，触发角色 test78（character_id=6、
// account dnf:10011、job=0 grow_type=1 剑魂、Lv90），武器类型**光剑**。
//
// 真实现场（HK 线上 2026-09-25，packet_log.20260925-192600.txt，会话
// game-000013 pkt_seq=110 与 game-000016 pkt_seq=109 —— 两次选角字节完全相同，
// 都在 `type=1345` 场景就绪应答之后、`type=503` 之前）：
//
//	hex 01130029000000a6277cbe19000010000000000001000000030c006dc1050600000000ffffffff0000
//	src=(list0 主背包, slot16, instance=0) MoveCount=1
//	dst=(list3 装备,   slot12, instance=101040493)
//
// 服务端事件把这条件按"脱下"执行了：
//
//	equipMove=unequip requestA=(0,16) requestB=(3,12) finalA=(0,16) finalB=(3,12) item=101040493 swap=0 changed=true
//
// 权威库行（dnf_equipment_entries / dnf_equipment_entry_extra, character_id=6,
// slot_index=12）在这里逐字节复刻：item 101040493 = 「圣耀救赎光剑」
// （`equipment/character/swordman/weapon/beamsword/101040493.equ`，PVF
// `[durability] 48`、`[equipment type] \`[weapon]\` 25`、`[minimum level] 90`、
// `[usable job] \`[swordman]\`…`），耐久 48/48、quality_seed 203004656、
// raw[0x0A]=13（强化/打包字节）、raw[0x0B:0x0D]=48、raw[0x0E:0x12]=item id。
//
// 根因不在耐久、不在身份证据、不在 PVF 缺键（那三条都被现场证据否掉了，见
// equipmentEndpointIdentityEcho 的注释），而在于**方向判定只看占用**：
// 背包端点为空 + 装备端点被占用 ⇒ 一律反转成 `unequip`。于是
//  1. 客户端在场景就绪后复述穿戴态的那条包被当成"脱下"执行 ⇒ 登录瞬间就卸；
//  2. 紧接着那条"补穿"包如果到达背包端点仍为空（客户端槽位过期，或上一次
//     unequip 已把落点改到别的槽），同一分支会**再反转一次** ⇒ 再卸一次，
//     武器留在背包里，而客户端以为已经穿上、不会再发包 —— 这就是"重登后
//     武器不在身上"。
//
// 参考实现（ServerS4A21 InventoryMoveService.IsEquipmentClientEchoWhenSourceMissing
// → ClientEcho，InventoryHandler.Move.cs:96-101 回错误 ACK 且不落库）对同一族
// "源端点不存在、客户端用实例复述装备端点那件物品"的包就是拒绝不落库。
const (
	reloginBeamswordItemID     int64  = 101040493
	reloginBeamswordSeed       uint32 = 203004656
	reloginBeamswordDurability uint16 = 48
	reloginBeamswordSlot       int16  = 12
	reloginBeamswordBagSlot    int16  = 16
	// reloginBeamswordRawHex 是 dnf_equipment_entries.raw_entry 的现场前缀
	// （0x0E 之后的 101 个字节在库里全是 0，这里只钉非零前缀）。
	reloginBeamswordRawHex = "0c006dc10506f09a190c0d3000006dc10506"
)

func reloginBeamswordRaw(t *testing.T) []byte {
	t.Helper()
	prefix, err := hex.DecodeString(reloginBeamswordRawHex)
	if err != nil {
		t.Fatalf("decode live raw prefix: %v", err)
	}
	// 现场 119 字节行：槽 + item + quality_seed + 0x0A 强化/打包字节 +
	// 0x0B 耐久 + 0x0D 封印字节 + 0x0E op19 源实例（= item id），其余全 0。
	raw := make([]byte, currentEquipmentRawEntryWireSize)
	copy(raw, prefix)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(reloginBeamswordSlot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(reloginBeamswordItemID))
	binary.LittleEndian.PutUint32(raw[6:10], reloginBeamswordSeed)
	raw[0x0A] = 13
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], reloginBeamswordDurability)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(reloginBeamswordItemID))
	if len(raw) != currentEquipmentRawEntryWireSize {
		t.Fatalf("live raw entry is %d bytes, want 0x%X", len(raw), currentEquipmentRawEntryWireSize)
	}
	return raw
}

// reloginBeamswordEntry 复刻库里那一行（raw + Extra 全部按现场值）。
func reloginBeamswordEntry(t *testing.T) dnfrepo.EquipmentEntry {
	t.Helper()
	raw := reloginBeamswordRaw(t)
	return dnfrepo.EquipmentEntry{
		SlotIndex: reloginBeamswordSlot,
		ItemID:    reloginBeamswordItemID,
		RawEntry:  raw,
		Extra: map[string]string{
			"current_exe_equipment_type":  "12",
			"current_exe_inventory_count": "1",
			"current_exe_runtime_move":    "1",
			"durability":                  "48",
			"equipment_slot":              "12",
			"equipment_type":              "[weapon]",
			"equipped_slot":               "12",
			"ext_data0":                   "13",
			"item_kind":                   "equipment",
			"max_durability":              "48",
			"packed_flag_byte":            "13",
			"pvf_path":                    "equipment/character/swordman/weapon/beamsword/101040493.equ",
			"quality_seed":                "203004656",
			"raw_entry_hex":               reloginBeamswordRawHex,
			"reinforce":                   "13",
			"upgrade_level":               "13",
		},
	}
}

func reloginFixture(t *testing.T, slots map[string]dnfrepo.ItemStack) (context.Context, dnfrepo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if slots == nil {
		slots = map[string]dnfrepo.ItemStack{}
	}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "6", Slots: slots})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "6",
		Entries:     map[string]dnfrepo.EquipmentEntry{"12": reloginBeamswordEntry(t)},
	})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repos, owner
}

// 真正的"脱下"包必须一个字都不变地继续生效：穿着的那件在**源端装备槽**、目的端是
// 背包槽（身份写在**源**端 —— 与现场 :93127 那次手动穿上同一条口径，只是方向相反）。
//
// 2026-09-28 第五轮：本用例原来用的是 A 形状（身份写在**装备**端点、背包端点实例为 0）。
// 那个形状现在由**方向判定本身**判 noop：客户端在装备端点上指名的那件正是那一格
// 穿着的那件、源端又是空的 ⇒ 它是"这件物品属于这一格"的复述，不是脱下请求，与任何
// 时间窗无关（见 worn_slot_reassert_gate.go；现场 packet_log.txt:93098/:91884 是复述、
// :76912 是同一形状在旧口径下被真卸下的那次）。所以这里改用真正的脱下形状把这条绝对
// 约束钉住；A 形状的新口径见 equipment_worn_slot_reassert_test.go。
func TestLoginTimeWeaponMoveKeepsTheGenuineUnequipForm(t *testing.T) {
	ctx, repos, owner := reloginFixture(t, nil)

	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListEquipment,
		SourceSlotIndex:          reloginBeamswordSlot,
		SourceInstanceValue:      int32(reloginBeamswordItemID),
		MoveCount:                1,
		DestinationListType:      wireListMain,
		DestinationSlotIndex:     reloginBeamswordBagSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("Move error = %v, want the genuine unequip to keep working", err)
	}
	if result.Mode != "unequip" || !result.Changed || result.ItemID != reloginBeamswordItemID {
		t.Fatalf("result = %+v, want a committed unequip of the beamsword", result)
	}
	if _, still := loadTestEquipment(t, ctx, repos, "6").Entries["12"]; still {
		t.Fatal("genuine unequip left the weapon worn")
	}
	bag, ok := loadTestInventory(t, ctx, repos, "6").Slots["0:16"]
	if !ok || bag.ItemID != reloginBeamswordItemID {
		t.Fatalf("bag slot 16 = %+v (present=%t), want the unequipped beamsword", bag, ok)
	}
}

// 本条就是修复：客户端在装备端点上指名"槽 12 是这件武器"、背包端点为空时，
// 那是复述穿戴态，不是脱下指令 —— 必须**不落库**，武器仍在身上。
//
// 修复前：resolveMoveEndpoints 只看占用 ⇒ A 空 + B 占用 ⇒ 反转 ⇒ 再判成
// `unequip`，武器落进背包（现场"重登后武器不在身上"）。
func TestLoginTimeWeaponStateEchoDoesNotUnequip(t *testing.T) {
	ctx, repos, owner := reloginFixture(t, nil)
	before := loadTestEquipment(t, ctx, repos, "6").Entries["12"]

	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListMain,
		SourceSlotIndex:          reloginBeamswordBagSlot,
		SourceInstanceValue:      int32(reloginBeamswordItemID),
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     reloginBeamswordSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("Move error = %v, want a non-mutating state echo", err)
	}
	if result.Mode != "noop" || result.Changed {
		t.Fatalf("result = %+v, want Mode=noop Changed=false", result)
	}
	if result.ItemID != reloginBeamswordItemID {
		t.Fatalf("result.ItemID = %d, want the worn beamsword %d", result.ItemID, reloginBeamswordItemID)
	}

	after, worn := loadTestEquipment(t, ctx, repos, "6").Entries["12"]
	if !worn {
		t.Fatal("state echo unequipped the worn weapon: slot 12 is now empty")
	}
	if after.ItemID != before.ItemID || after.SlotIndex != before.SlotIndex {
		t.Fatalf("worn row = %+v, want it untouched (%+v)", after, before)
	}
	if got, want := after.RawEntry, before.RawEntry; string(got) != string(want) {
		t.Fatalf("worn raw entry changed: % X != % X", got, want)
	}
	if got := binary.LittleEndian.Uint16(after.RawEntry[0x0B:0x0D]); got != reloginBeamswordDurability {
		t.Fatalf("worn durability = %d, want %d", got, reloginBeamswordDurability)
	}
	if got := binary.LittleEndian.Uint32(after.RawEntry[6:10]); got != reloginBeamswordSeed {
		t.Fatalf("worn quality seed = %d, want %d", got, reloginBeamswordSeed)
	}
	if slots := loadTestInventory(t, ctx, repos, "6").Slots; len(slots) != 0 {
		t.Fatalf("state echo wrote inventory rows: %+v", slots)
	}
}

// 闸门只作用于"背包端点为空"的复述形状；背包端点真有东西时照旧按真实移动处理。
// 这条钉住"别把其它武器的正常穿上/替换挡掉"。
func TestLoginTimeWeaponStateEchoDoesNotBlockARealEquipSwap(t *testing.T) {
	const replacementID int64 = 101010912
	ctx, repos, owner := reloginFixture(t, map[string]dnfrepo.ItemStack{
		"0:16": {
			ItemID: replacementID,
			Count:  1,
			Extra: map[string]string{
				"item_kind":      "equipment",
				"pvf_path":       "equipment/character/swordman/weapon/katana/101010912.equ",
				"equipment_type": "[weapon]",
				"durability":     "45",
				"max_durability": "45",
				"quality_seed":   "70238363",
			},
		},
	})

	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListMain,
		SourceSlotIndex:          reloginBeamswordBagSlot,
		SourceInstanceValue:      int32(replacementID),
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     reloginBeamswordSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("Move error = %v, want a real equip swap", err)
	}
	if result.Mode != "equip_swap" || !result.Changed || result.ItemID != replacementID {
		t.Fatalf("result = %+v, want an equip_swap of the replacement weapon", result)
	}
	if result.SwappedItemID != reloginBeamswordItemID {
		t.Fatalf("swapped item = %d, want the beamsword %d", result.SwappedItemID, reloginBeamswordItemID)
	}
	worn := loadTestEquipment(t, ctx, repos, "6").Entries["12"]
	if worn.ItemID != replacementID {
		t.Fatalf("worn slot 12 = %+v, want the replacement", worn)
	}
	swapped, ok := loadTestInventory(t, ctx, repos, "6").Slots["0:16"]
	if !ok || swapped.ItemID != reloginBeamswordItemID {
		t.Fatalf("bag slot 16 = %+v (present=%t), want the swapped-out beamsword", swapped, ok)
	}
}

// 端到端（模块级）：GM 发放 → 领取落地（只有身份+耐久证据）→ 穿上 → 落库 →
// 重登时客户端再发一次复述包 → 武器仍在身上、耐久与品质种子不变。
func TestBeamswordReloginKeepsTheWeaponWorn(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	claimed := map[string]string{
		"item_kind":        "equipment",
		"pvf_path":         "equipment/character/swordman/weapon/beamsword/101040493.equ",
		"equipment_type":   "[weapon]",
		"durability":       "48",
		"max_durability":   "48",
		"quality_seed":     "203004656",
		"packed_flag_byte": "13",
	}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots: map[string]dnfrepo.ItemStack{
			"0:16": {ItemID: reloginBeamswordItemID, Count: 1, Extra: claimed},
		},
	})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "6", Entries: map[string]dnfrepo.EquipmentEntry{}})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	equipped, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListMain,
		SourceSlotIndex:          reloginBeamswordBagSlot,
		SourceInstanceValue:      int32(reloginBeamswordItemID),
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     reloginBeamswordSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("equip error = %v", err)
	}
	if equipped.Mode != "equip" || !equipped.Changed {
		t.Fatalf("equip result = %+v", equipped)
	}

	// 重登：从仓库重新读出这一行，必须还在槽 12、还是这件、耐久/种子完整。
	worn := loadTestEquipment(t, ctx, repos, "6").Entries["12"]
	if worn.ItemID != reloginBeamswordItemID || worn.SlotIndex != reloginBeamswordSlot {
		t.Fatalf("reloaded worn row = %+v", worn)
	}
	if got := binary.LittleEndian.Uint16(worn.RawEntry[0x0B:0x0D]); got != reloginBeamswordDurability {
		t.Fatalf("reloaded durability = %d, want %d", got, reloginBeamswordDurability)
	}
	if got := binary.LittleEndian.Uint32(worn.RawEntry[6:10]); got != reloginBeamswordSeed {
		t.Fatalf("reloaded quality seed = %d, want %d", got, reloginBeamswordSeed)
	}

	// 重登时客户端把穿戴态复述一遍（背包槽 16 现在是空的）⇒ 必须不落库。
	echo, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListMain,
		SourceSlotIndex:          reloginBeamswordBagSlot,
		SourceInstanceValue:      int32(reloginBeamswordItemID),
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     reloginBeamswordSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("state echo error = %v", err)
	}
	if echo.Mode != "noop" || echo.Changed {
		t.Fatalf("state echo result = %+v, want Mode=noop Changed=false", echo)
	}
	stillWorn := loadTestEquipment(t, ctx, repos, "6").Entries["12"]
	if stillWorn.ItemID != reloginBeamswordItemID {
		t.Fatalf("state echo moved the weapon: %+v", stillWorn)
	}
	if slots := loadTestInventory(t, ctx, repos, "6").Slots; len(slots) != 0 {
		t.Fatalf("state echo put the weapon back in the bag: %+v", slots)
	}
}

// 闸门必须**窄**：只有"背包端点空 + 实例指名装备端点那件物品 + 装备端点实例为 0"
// 这一种形状才算复述；其余任何组合都不许被它吞掉（否则会挡住真实移动）。
func TestEquipmentEndpointIdentityEchoOnlyMatchesTheEchoShape(t *testing.T) {
	worn := dnfrepo.EquipmentEntry{SlotIndex: 12, ItemID: reloginBeamswordItemID}
	base := MoveCommand{
		SourceListType:           wireListMain,
		SourceSlotIndex:          16,
		SourceInstanceValue:      int32(reloginBeamswordItemID),
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     12,
		DestinationInstanceValue: 0,
	}
	if !equipmentEndpointIdentityEcho(base, true, worn, true) {
		t.Fatal("the live echo shape must be recognised")
	}
	mutate := func(f func(*MoveCommand)) MoveCommand {
		cmd := base
		f(&cmd)
		return cmd
	}
	for name, test := range map[string]struct {
		cmd        MoveCommand
		aEmpty     bool
		equipped   dnfrepo.EquipmentEntry
		bOccupied  bool
		wantEcho   bool
		whyChanged string
	}{
		"the live echo":               {base, true, worn, true, true, ""},
		"inventory endpoint occupied": {base, false, worn, true, false, "there is a real source stack to move"},
		"equipment endpoint empty":    {base, true, worn, false, false, "nothing is worn to protect"},
		"no source identity":          {mutate(func(c *MoveCommand) { c.SourceInstanceValue = 0 }), true, worn, true, false, "the genuine unequip form"},
		"different source identity":   {mutate(func(c *MoveCommand) { c.SourceInstanceValue = 3037 }), true, worn, true, false, "the client names another item"},
		"destination identity set":    {mutate(func(c *MoveCommand) { c.DestinationInstanceValue = 1 }), true, worn, true, false, "the client claims the worn slot holds something else"},
		"equipment source endpoint":   {mutate(func(c *MoveCommand) { c.SourceListType = wireListEquipment }), true, worn, true, false, "both endpoints worn"},
		"non worn destination":        {mutate(func(c *MoveCommand) { c.DestinationListType = wireListMain }), true, worn, true, false, "not an equipment target"},
		"avatar echo": {mutate(func(c *MoveCommand) {
			c.SourceListType = 1
			c.DestinationSlotIndex = 8
			c.SourceInstanceValue = 101580163
		}), true, dnfrepo.EquipmentEntry{SlotIndex: 8, ItemID: 101580163}, true, true, "装扮同族形状（list1 → list3）"},
		"avatar echo with mismatched identity": {mutate(func(c *MoveCommand) {
			c.SourceListType = 1
			c.DestinationSlotIndex = 8
			c.SourceInstanceValue = 101580163
		}), true, dnfrepo.EquipmentEntry{SlotIndex: 8, ItemID: 999}, true, false, "identity must match the worn item exactly"},
	} {
		t.Run(name, func(t *testing.T) {
			got := equipmentEndpointIdentityEcho(test.cmd, test.aEmpty, test.equipped, test.bOccupied)
			if got != test.wantEcho {
				t.Fatalf("echo = %t, want %t (%s)", got, test.wantEcho, test.whyChanged)
			}
		})
	}
}
