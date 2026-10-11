package equip

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住 **双端点同名**（`srcIV == dstIV != 0`）这一类 op19 的闸门
// （owner.go 的 equipmentDuplicateEndpointIdentity /
// DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD）。
//
// 现场（`_dist/spontaneous-equip-1758-investigation.md` 2.2/3.3；线上
// `/opt/dnf90/logs/packet_log.txt`，角色 6，conn `game-000003`）：
//
//	75197 17:58:24.624 双端点同名 -> 反转端点兜底（resolveMoveEndpoints 末尾那一支）
//	      -> equipMove=unequip changed=true  => 武器 101040493 **真的**离开槽 12
//	75187 17:58:23.372 普通 EQUIP 形状 -> equipMove=equip（这条是对的：槽 17）
//	71705 17:01:53.623 双端点同名但**背包端点被占** -> equipMove=equip_swap（本闸门不碰）
//	75008 17:43:56.445 A 族（srcIV==0）-> 登录窗口内 noop（第 4 轮那三个收口覆盖的那一类）
//
// **包体逐字节取自线上日志**：下面每条都是 `packet_log.txt` 里那一行的 hex 原文。
// 前 13 字节是帧头，body 是其后 28 字节；字段偏移的唯一真相在
// `internal/modules/dnf/inventory/request.go:233-253` 的 DecodeMoveItemspaceRequest。
// 本包**不能** import inventory（inventory 依赖 equip，会成环），所以这里按同一份偏移量
// 本地解 28 字节；dnfbridge 侧的用例会用真解码器交叉核对同一批原文。
const (
	duplicateIdentityWeaponItemID    int64 = 101040493 // 角色 6 的圣耀救赎光剑（槽 12）
	duplicateIdentityArmorItemID     int64 = 100270515 // 角色 6 槽 17 那件（75187 装回的那件）
	duplicateIdentityCharOneWeaponID int64 = 101010856 // 角色 1 槽 12 的武器（71705）

	duplicateIdentityWornSlot  int16 = 12
	duplicateIdentityArmorSlot int16 = 17

	// 75197：双端点同名（背包 0:11 与装备 3:12 都写 101040493）。
	duplicateIdentityFrameDoubleIdent = "01130029000000baaed1b90c01000b006dc1050601000000030c006dc1050600000000ffffffff0000"
	// 75187：普通 EQUIP（背包 0:10 有 100270515、装备 3:17 为空）。
	duplicateIdentityFrameEquip = "01130029000000ea5d16dc0b01000a00b301fa05010000000311000000000000000000ffffffff0000"
	// 71705：双端点同名、但背包端点**被占**（角色 1）。
	duplicateIdentityFrameDoubleIdentOccupied = "01130029000000cd3b79f46e00001300a84d050601000000030c00a84d050600000000ffffffff0000"
	// 75008：A 族登录复述（背包端点实例 0、装备端点点名那件）。
	duplicateIdentityFrameLoginReassert = "0113002900000092928abb3000000b000000000001000000030c006dc1050600000000ffffffff0000"
	// 72750（17:03:13.264）：identity_echo 那一族（身份写在背包端点、装备端点为 0）。
	duplicateIdentityFrameIdentityEcho = "011300290000002c8e0ae82200000a006dc1050601000000030c000000000000000000ffffffff0000"
)

// duplicateIdentityWireFrameBody 去掉线上帧的 13 字节头，返回 28 字节 body。
func duplicateIdentityWireFrameBody(t *testing.T, frameHex string) []byte {
	t.Helper()
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("decode frame %q: %v", frameHex, err)
	}
	const currentMoveItemspaceFrameHeaderSize = 13
	if len(frame) != currentMoveItemspaceFrameHeaderSize+28 {
		t.Fatalf("frame %q = %d bytes, want header+28", frameHex, len(frame))
	}
	return frame[currentMoveItemspaceFrameHeaderSize:]
}

// duplicateIdentityMoveCommand 按 DecodeMoveItemspaceRequest 的偏移量解出域层命令。
func duplicateIdentityMoveCommand(t *testing.T, frameHex string) MoveCommand {
	t.Helper()
	body := duplicateIdentityWireFrameBody(t, frameHex)
	return MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           body[0],
		SourceSlotIndex:          int16(binary.LittleEndian.Uint16(body[1:3])),
		SourceInstanceValue:      int32(binary.LittleEndian.Uint32(body[3:7])),
		MoveCount:                int32(binary.LittleEndian.Uint32(body[7:11])),
		DestinationListType:      body[11],
		DestinationSlotIndex:     int16(binary.LittleEndian.Uint16(body[12:14])),
		DestinationInstanceValue: int32(binary.LittleEndian.Uint32(body[14:18])),
	}
}

// duplicateIdentityRawEntry 造一条 0x77 的当前 EXE 行（与现场 raw_entry 同长同布局：
// [0:2]=槽号、[2:6]=item id、[0x0E:0x12]=item id）。
func duplicateIdentityRawEntry(slot int16, itemID int64) []byte {
	raw := make([]byte, currentEquipmentRawEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[6:10], 203004656)
	raw[0x0A] = 13
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 48)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(itemID))
	return raw
}

func duplicateIdentityWornEntry(slot int16, itemID int64) dnfrepo.EquipmentEntry {
	return dnfrepo.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		RawEntry:  duplicateIdentityRawEntry(slot, itemID),
		Extra: map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       "equipment/character/swordman/weapon/beamsword/x.equ",
			"equipped_slot":  strconv.Itoa(int(slot)),
			"equipment_slot": strconv.Itoa(int(slot)),
			"equipment_type": "[weapon]",
			"durability":     "48",
			"max_durability": "48",
			"quality_seed":   "203004656",
		},
	}
}

func duplicateIdentityBagStack(slot int16, itemID int64) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID:   itemID,
		Count:    1,
		RawEntry: duplicateIdentityRawEntry(slot, itemID),
		Extra: map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       "equipment/character/swordman/weapon/beamsword/x.equ",
			"equipment_type": "[weapon]",
			"durability":     "48",
			"max_durability": "48",
			"quality_seed":   "203004656",
			"equipment_slot": strconv.Itoa(int(slot)),
		},
	}
}

// duplicateIdentityFixture 造一个最小角色：装备槽按 worn 给定，主背包按 bag 给定
// （bag 的 key 是主背包槽号）。
func duplicateIdentityFixture(
	t *testing.T,
	characterKey string,
	worn []dnfrepo.EquipmentEntry,
	bag map[int16]int64,
) (context.Context, dnfrepo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	slots := make(map[string]dnfrepo.ItemStack, len(bag))
	for slot, itemID := range bag {
		slots[inventoryKey(wireListMain, slot)] = duplicateIdentityBagStack(slot, itemID)
	}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: characterKey, Slots: slots})
	entries := make(map[string]dnfrepo.EquipmentEntry, len(worn))
	for _, entry := range worn {
		entries[entryKey(entry.SlotIndex)] = entry
	}
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: characterKey, Entries: entries})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repos, owner
}

// duplicateIdentityBagSlotOf 回答某件物品现在落在背包的哪个 key（"" = 不在背包里）。
func duplicateIdentityBagSlotOf(t *testing.T, ctx context.Context, repos dnfrepo.Group, characterKey string, itemID int64) string {
	t.Helper()
	for key, stack := range loadTestInventory(t, ctx, repos, characterKey).Slots {
		if stack.ItemID == itemID {
			return key
		}
	}
	return ""
}

// TestEquipmentDuplicateEndpointIdentityRefusesTheLiveUnequip 是**现场回归本身**：
// packet_log.txt:75197 那条逐字节原文在默认档下不许再卸下任何东西。
func TestEquipmentDuplicateEndpointIdentityRefusesTheLiveUnequip(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	ctx, repos, owner := duplicateIdentityFixture(t, "6",
		[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)},
		nil)

	cmd := duplicateIdentityMoveCommand(t, duplicateIdentityFrameDoubleIdent)
	if cmd.SourceListType != wireListMain || cmd.SourceSlotIndex != 11 ||
		cmd.SourceInstanceValue != int32(duplicateIdentityWeaponItemID) ||
		cmd.DestinationListType != wireListEquipment || cmd.DestinationSlotIndex != duplicateIdentityWornSlot ||
		cmd.DestinationInstanceValue != int32(duplicateIdentityWeaponItemID) || cmd.MoveCount != 1 {
		t.Fatalf("the live 75197 packet decoded to src=(%d,%d,IV=%d) dst=(%d,%d,IV=%d) count=%d, want the double-ident shape",
			cmd.SourceListType, cmd.SourceSlotIndex, cmd.SourceInstanceValue,
			cmd.DestinationListType, cmd.DestinationSlotIndex, cmd.DestinationInstanceValue, cmd.MoveCount)
	}

	result, err := owner.Move(ctx, cmd)
	if err != nil {
		t.Fatalf("Move error = %v, want a non-mutating refusal", err)
	}
	if result.Mode != "noop" || result.Changed {
		t.Fatalf("result = %+v, want Mode=noop Changed=false (the request is already satisfied)", result)
	}
	if result.NoopGate != equipmentDuplicateEndpointIdentityGate {
		t.Fatalf("noopGate = %q, want %q", result.NoopGate, equipmentDuplicateEndpointIdentityGate)
	}
	if got := loadTestEquipment(t, ctx, repos, "6").Entries["12"].ItemID; got != duplicateIdentityWeaponItemID {
		t.Fatalf("slot 12 item = %d, want %d still worn", got, duplicateIdentityWeaponItemID)
	}
	if slot := duplicateIdentityBagSlotOf(t, ctx, repos, "6", duplicateIdentityWeaponItemID); slot != "" {
		t.Fatalf("the weapon was moved into bag %s: this is the operator symptom (weapon auto-unequipped)", slot)
	}
}

// TestEquipmentDuplicateEndpointIdentityRollbackReproducesTodaysUnequip：把开关写成
// 回退字面量，同一条包逐字节复现今天的真卸下（含落点 = 客户端点名的 0:11）。
func TestEquipmentDuplicateEndpointIdentityRollbackReproducesTodaysUnequip(t *testing.T) {
	for _, literal := range []string{"off", "0", "false", "no", "legacy", "disable", "disabled", "none"} {
		t.Run(literal, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", literal)
			t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
			ctx, repos, owner := duplicateIdentityFixture(t, "6",
				[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)},
				nil)

			result, err := owner.Move(ctx, duplicateIdentityMoveCommand(t, duplicateIdentityFrameDoubleIdent))
			if err != nil {
				t.Fatalf("Move error = %v", err)
			}
			if result.Mode != "unequip" || !result.Changed || result.ItemID != duplicateIdentityWeaponItemID {
				t.Fatalf("rollback result = %+v, want today's unequip of %d", result, duplicateIdentityWeaponItemID)
			}
			// 回退档的结果端点必须与今天**逐字段**一致：反转分支会把端点改回请求的
			// A/B 方向（owner.go 的 `if reversed` 那一段），所以现场日志里是
			// `finalA=(0,11) finalB=(3,12)` —— 它**不能**用来判断是否走了兜底
			// （见 `_dist/spontaneous-equip-1758-investigation.md` 3.3 的同一条提示）。
			if result.SourceListType != wireListMain || result.SourceSlotIndex != 11 ||
				result.DestinationListType != wireListEquipment || result.DestinationSlotIndex != duplicateIdentityWornSlot {
				t.Fatalf("rollback result endpoints = (%d,%d)->(%d,%d), want today's (0,11)->(3,12)",
					result.SourceListType, result.SourceSlotIndex, result.DestinationListType, result.DestinationSlotIndex)
			}
			if _, still := loadTestEquipment(t, ctx, repos, "6").Entries["12"]; still {
				t.Fatal("rollback tier must leave slot 12 empty (today's behaviour)")
			}
			if slot := duplicateIdentityBagSlotOf(t, ctx, repos, "6", duplicateIdentityWeaponItemID); slot != inventoryKey(wireListMain, 11) {
				t.Fatalf("rollback tier must land the weapon on %s, got %q (today's behaviour)",
					inventoryKey(wireListMain, 11), slot)
			}
		})
	}
}

// TestEquipmentDuplicateEndpointIdentityBoundary 判据边界表（两侧都钉）：
// 只有"两端同名非零 + 背包端点权威为空 + 装备端点确实穿着那件"才为真。
func TestEquipmentDuplicateEndpointIdentityBoundary(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "")
	worn := duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)
	live := duplicateIdentityMoveCommand(t, duplicateIdentityFrameDoubleIdent)
	mutate := func(f func(*MoveCommand)) MoveCommand {
		cmd := live
		f(&cmd)
		return cmd
	}
	for name, testCase := range map[string]struct {
		cmd        MoveCommand
		empty      bool
		occupied   bool
		want       bool
		whyChanged string
	}{
		"live 75197": {live, true, true, true, ""},
		"bag endpoint occupied (71705 class)": {live, false, true, false,
			"契约上先命中 if aOccupied 分支 -> equip_swap，本闸门不碰"},
		"worn slot empty": {live, true, false, false, "反转分支拿不到装备端点的身份"},
		"A shape (srcIV==0)": {mutate(func(c *MoveCommand) {
			c.SourceInstanceValue = 0
		}), true, true, false, "那是 identity_echo / 登录复述那一族"},
		"echo shape (dstIV==0)": {mutate(func(c *MoveCommand) {
			c.DestinationInstanceValue = 0
		}), true, true, false, "那是身份写在背包端点的 identity_echo"},
		"two different instances": {mutate(func(c *MoveCommand) {
			c.SourceInstanceValue = int32(duplicateIdentityArmorItemID)
		}), true, true, false, "两端不同名 = 可能是真的换装请求，不属于本闸门"},
		"destination names another item": {mutate(func(c *MoveCommand) {
			c.DestinationInstanceValue = int32(duplicateIdentityArmorItemID)
		}), true, true, false, "装备端点没点名穿着的那件"},
		"batch move count": {mutate(func(c *MoveCommand) {
			c.MoveCount = 2
		}), true, true, false, "搬运件数不是 1"},
		"source is the cargo list": {mutate(func(c *MoveCommand) {
			c.SourceListType = 2
		}), true, true, false, "只覆盖主背包 0 / 装扮栏 1（同现场形状）"},
		"destination is not the worn list": {mutate(func(c *MoveCommand) {
			c.DestinationListType = wireListMain
		}), true, true, false, "同容器搬运与本闸门无关"},
	} {
		t.Run(name, func(t *testing.T) {
			got := equipmentDuplicateEndpointIdentity(testCase.cmd, testCase.empty, worn, testCase.occupied)
			if got != testCase.want {
				t.Fatalf("guard = %t, want %t (%s)", got, testCase.want, testCase.whyChanged)
			}
		})
	}

	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "off")
	if equipmentDuplicateEndpointIdentity(live, true, worn, true) {
		t.Fatal("the rollback literal must make the guard inert (today's behaviour)")
	}
}

// TestEquipmentDuplicateEndpointIdentityLeavesGenuineMovesAlone：把"玩家真实动作"
// 三条形状在**开关两侧**都钉死 —— 本闸门对它们必须完全惰性。
//
// 2026-09-28 第五轮：步骤 (2) 那条 A 形状（客户端把身份写在**装备端点**、背包端点为 0，
// 现场 75008/76912 原文）的语义已经由**方向判定本身**接管：客户端点名的那件正是这一格
// 穿着的那件 ⇒ 判 noop，与窗口无关（见 worn_slot_reassert_gate.go 与
// equipment_worn_slot_reassert_test.go）。本用例的主题是双端点闸门的惰性，所以这里把
// 方向判定显式写到回退字面量，"今天会真卸"这句话才成立；默认档下同一条包判 noop
// 由 TestEquipmentDuplicateEndpointIdentityKeepsTheLoginWearFamilyBehaviour 钉住。
func TestEquipmentDuplicateEndpointIdentityLeavesGenuineMovesAlone(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(wornSlotReassertSwitchEnv, "off")
	for _, tier := range []string{"", "off"} {
		t.Run("tier="+tier, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", tier)

			// (1) 真实穿戴：75187 原文（背包 0:10 -> 装备槽 17），普通时段。
			ctx, repos, owner := duplicateIdentityFixture(t, "6", nil,
				map[int16]int64{10: duplicateIdentityArmorItemID})
			result, err := owner.Move(ctx, duplicateIdentityMoveCommand(t, duplicateIdentityFrameEquip))
			if err != nil {
				t.Fatalf("genuine equip error = %v", err)
			}
			if result.Mode != "equip" || !result.Changed || result.ItemID != duplicateIdentityArmorItemID {
				t.Fatalf("genuine equip = %+v, want Mode=equip ItemID=%d", result, duplicateIdentityArmorItemID)
			}
			if got := loadTestEquipment(t, ctx, repos, "6").Entries[strconv.Itoa(int(duplicateIdentityArmorSlot))].ItemID; got != duplicateIdentityArmorItemID {
				t.Fatalf("slot 17 item = %d, want %d worn", got, duplicateIdentityArmorItemID)
			}

			// (2) 真实脱下：A 形状（客户端把身份写在装备端点、背包端点为 0）——
			// 75008 原文，窗口**不开**（普通时段）⇒ 必须真卸。
			ctx, repos, owner = duplicateIdentityFixture(t, "6",
				[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)},
				nil)
			result, err = owner.Move(ctx, duplicateIdentityMoveCommand(t, duplicateIdentityFrameLoginReassert))
			if err != nil {
				t.Fatalf("genuine unequip error = %v", err)
			}
			if result.Mode != "unequip" || !result.Changed || result.ItemID != duplicateIdentityWeaponItemID {
				t.Fatalf("genuine unequip = %+v, want today's unequip of %d", result, duplicateIdentityWeaponItemID)
			}
			if slot := duplicateIdentityBagSlotOf(t, ctx, repos, "6", duplicateIdentityWeaponItemID); slot != inventoryKey(wireListMain, 11) {
				t.Fatalf("genuine unequip landed on %q, want %s", slot, inventoryKey(wireListMain, 11))
			}

			// (3) 反形（identity_echo，72750 原文）：背包端点写那件、装备端点为 0
			// ⇒ 仍由 identity_echo 那条老闸门判 noop（不属于本闸门）。
			ctx, repos, owner = duplicateIdentityFixture(t, "6",
				[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)},
				nil)
			result, err = owner.Move(ctx, duplicateIdentityMoveCommand(t, duplicateIdentityFrameIdentityEcho))
			if err != nil {
				t.Fatalf("identity echo error = %v", err)
			}
			if result.Mode != "noop" || result.Changed || result.NoopGate != "identity_echo" {
				t.Fatalf("identity echo = %+v, want the pre-existing identity_echo noop", result)
			}
		})
	}
}

// TestEquipmentDuplicateEndpointIdentityKeepsOccupiedBagEndpointSwap 是"被占背包端点"
// 那一半的**回归钉子**（packet_log.txt:71705）：现场判定是 equip_swap，本闸门不许改它。
func TestEquipmentDuplicateEndpointIdentityKeepsOccupiedBagEndpointSwap(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	// 背包 0:19 上**真的**有另一件（现场那条包却声明它也是 101010856）。
	const otherWeapon int64 = 101010857
	ctx, repos, owner := duplicateIdentityFixture(t, "1",
		[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityCharOneWeaponID)},
		map[int16]int64{19: otherWeapon})

	cmd := duplicateIdentityMoveCommand(t, duplicateIdentityFrameDoubleIdentOccupied)
	cmd.SelectedCharacterID = 1
	result, err := owner.Move(ctx, cmd)
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "equip_swap" || !result.Changed ||
		result.SwappedItemID != duplicateIdentityCharOneWeaponID || result.ItemID != otherWeapon {
		t.Fatalf("result = %+v, want today's equip_swap (bag %s <-> worn 12)", result, inventoryKey(wireListMain, 19))
	}
	if got := loadTestEquipment(t, ctx, repos, "1").Entries["12"].ItemID; got != otherWeapon {
		t.Fatalf("slot 12 item = %d, want %d (the bag item went onto the body)", got, otherWeapon)
	}
	if slot := duplicateIdentityBagSlotOf(t, ctx, repos, "1", duplicateIdentityCharOneWeaponID); slot != inventoryKey(wireListMain, 19) {
		t.Fatalf("the previously worn weapon landed on %q, want %s", slot, inventoryKey(wireListMain, 19))
	}
}

// TestEquipmentDuplicateEndpointIdentityKeepsTheLoginWearFamilyBehaviour：A 族（srcIV==0）
// 的行为里，**双端点闸门一个字都不许变**：窗口开 ⇒ noop（login_wear_reassert_noop，
// 具名口径不变）。窗口**不开**那一半的语义已由方向判定接管（客户端点名穿着的那件 ⇒
// noop worn_slot_reassert），因此这里同时钉住"默认档 = 复述判 noop"与"把方向判定写回
// 回退字面量 ⇒ 逐字节回到今天的真卸"。本闸门仍然只占 `srcIV==dstIV!=0` 那一格。
func TestEquipmentDuplicateEndpointIdentityKeepsTheLoginWearFamilyBehaviour(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")

	t.Run("window open keeps the reassert gate", func(t *testing.T) {
		ctx, repos, owner := duplicateIdentityFixture(t, "6",
			[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)},
			nil)
		cmd := duplicateIdentityMoveCommand(t, duplicateIdentityFrameLoginReassert)
		cmd.LoginWearWindowOpen = true
		result, err := owner.Move(ctx, cmd)
		if err != nil {
			t.Fatalf("Move error = %v", err)
		}
		if result.Mode != "noop" || result.Changed || result.NoopGate != LoginWearReassertNoopGate {
			t.Fatalf("result = %+v, want the login_wear_reassert_noop", result)
		}
		if got := loadTestEquipment(t, ctx, repos, "6").Entries["12"].ItemID; got != duplicateIdentityWeaponItemID {
			t.Fatalf("slot 12 item = %d, want %d", got, duplicateIdentityWeaponItemID)
		}
	})

	t.Run("window closed is absorbed by the direction judgement", func(t *testing.T) {
		ctx, repos, owner := duplicateIdentityFixture(t, "6",
			[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)},
			nil)
		result, err := owner.Move(ctx, duplicateIdentityMoveCommand(t, duplicateIdentityFrameLoginReassert))
		if err != nil {
			t.Fatalf("Move error = %v", err)
		}
		if result.Mode != "noop" || result.Changed || result.NoopGate != WornSlotReassertGate {
			t.Fatalf("result = %+v, want the window-independent %s noop", result, WornSlotReassertGate)
		}
		if got := loadTestEquipment(t, ctx, repos, "6").Entries["12"].ItemID; got != duplicateIdentityWeaponItemID {
			t.Fatalf("slot 12 item = %d, want %d still worn", got, duplicateIdentityWeaponItemID)
		}
	})

	t.Run("window closed under the judgement rollback keeps today's unequip", func(t *testing.T) {
		t.Setenv(wornSlotReassertSwitchEnv, "off")
		ctx, _, owner := duplicateIdentityFixture(t, "6",
			[]dnfrepo.EquipmentEntry{duplicateIdentityWornEntry(duplicateIdentityWornSlot, duplicateIdentityWeaponItemID)},
			nil)
		result, err := owner.Move(ctx, duplicateIdentityMoveCommand(t, duplicateIdentityFrameLoginReassert))
		if err != nil {
			t.Fatalf("Move error = %v", err)
		}
		if result.Mode != "unequip" || !result.Changed || result.NoopGate != "" {
			t.Fatalf("result = %+v, want today's unequip outside the window (the round-4 scope boundary)", result)
		}
	})
}
