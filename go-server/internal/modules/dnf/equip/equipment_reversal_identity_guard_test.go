package equip

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住反转端点兜底上的**身份矛盾闸门**（owner.go 的
// equipmentLoginWearUndeclaredWearReversal /
// DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD），以及它的**范围边界**。
//
// 现场形状与判据边界（2026-09-28 第三轮，`_dist/weapon-unequip-stutter-round4.md`
// §2.3/§2.6 与 `_dist/weapon-unequip-diagnosis.md` §1.5）：
//
//	角色 6 槽 17 护甲 100270515（09:09:20.875025325Z 真卸，那件现在躺在背包 0:10）
//	角色 1 槽 12 武器 101010856（09:01:49.314243779Z 真卸，1.05 s 后又被客户端装回）
//	角色 6 槽 12 武器 101040493（09:03:09.939 / 09:08:13.371 窗内两次 noop）
//
// **不许把本闸门当成"登录复述闸门"**：同一会话里 game-001200 的 61423 与 61575 两条
// op19 body **逐字节相同**，一条判 noop、一条真卸 —— 复述与"玩家真实脱下"在包体上
// 不可区分。所以本闸门只堵身份**不一致**的那一半：反转端点会卸下一件客户端从未点名的装备。
const (
	reversalGuardCharTest78         = "6"
	reversalGuardCharOne            = "1"
	reversalGuardWeaponItemID int64 = 101040493 // 角色 6 的圣耀救赎光剑（现场）
	reversalGuardWornSlot     int16 = 12
	reversalGuardBagSlot      int16 = 16
	// 角色 1 的武器：与角色 6 那把**不同的** item id。既用来构造"客户端那一格视图过期、
	// 点名了另一件武器"的形状，也是现场角色 1 槽 12 的真实 item id。
	reversalGuardOtherWeaponItemID int64 = 101010856
	// 角色 6 槽 17 的护甲（现场被真卸的那件）。
	reversalGuardArmorItemID int64 = 100270515
	reversalGuardArmorSlot   int16 = 17
)

func reversalGuardItemKey(value int64) string {
	return strconv.FormatInt(value, 10)
}

func reversalGuardRawEntry(slot int16, itemID int64) []byte {
	raw := make([]byte, currentEquipmentRawEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[6:10], 203004656)
	raw[0x0A] = 13
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 48)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(itemID))
	return raw
}

func reversalGuardEntry(slot int16, itemID int64) dnfrepo.EquipmentEntry {
	return dnfrepo.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		RawEntry:  reversalGuardRawEntry(slot, itemID),
		Extra: map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       "equipment/character/swordman/weapon/beamsword/" + reversalGuardItemKey(itemID) + ".equ",
			"equipped_slot":  reversalGuardItemKey(int64(slot)),
			"equipment_slot": reversalGuardItemKey(int64(slot)),
			"equipment_type": "[weapon]",
			"durability":     "48",
			"max_durability": "48",
			"quality_seed":   "203004656",
		},
	}
}

func reversalGuardFixture(t *testing.T, characterKey string, entries ...dnfrepo.EquipmentEntry) (context.Context, dnfrepo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: characterKey, Slots: map[string]dnfrepo.ItemStack{}})
	worn := make(map[string]dnfrepo.EquipmentEntry, len(entries))
	for _, entry := range entries {
		worn[reversalGuardItemKey(int64(entry.SlotIndex))] = entry
	}
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: characterKey, Entries: worn})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repos, owner
}

// reversalGuardMove 是现场那条反转形状：背包端点实例 0 + 装备端点实例 = 客户端点名的 item。
func reversalGuardMove(characterID uint16, bagSlot, wornSlot int16, declaredItemID int64) MoveCommand {
	return MoveCommand{
		SelectedCharacterID:      characterID,
		SourceListType:           wireListMain,
		SourceSlotIndex:          bagSlot,
		SourceInstanceValue:      0,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     wornSlot,
		DestinationInstanceValue: int32(declaredItemID),
	}
}

// reversalGuardLoginMove 是上面那条，但落在**登录复述上下文**里（桥侧锚点/每槽收口
// 判定为真时 game_aligned.go 填的就是这个标记）。
func reversalGuardLoginMove(characterID uint16, bagSlot, wornSlot int16, declaredItemID int64) MoveCommand {
	cmd := reversalGuardMove(characterID, bagSlot, wornSlot, declaredItemID)
	cmd.LoginWearWindowOpen = true
	return cmd
}

func reversalGuardBagHolds(t *testing.T, ctx context.Context, repos dnfrepo.Group, characterKey string, itemID int64) (string, bool) {
	t.Helper()
	for key, stack := range loadTestInventory(t, ctx, repos, characterKey).Slots {
		if stack.ItemID == itemID {
			return key, true
		}
	}
	return "", false
}

// 默认档 + 登录复述上下文：客户端在装备端点上点名的是**另一件**物品时，反转端点不许把
// 当前穿着的那件卸下来（它从未被点名）。本条是**证伪靶**：把开关写回 off，或把
// LoginWearWindowOpen 判据去掉范围限制后该方向，都必然让这里失败。
func TestEquipmentLoginWearUndeclaredReversalRefusesToUnequipAnUnnamedItem(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD", "")
	ctx, repos, owner := reversalGuardFixture(t, reversalGuardCharTest78,
		reversalGuardEntry(reversalGuardWornSlot, reversalGuardWeaponItemID))

	result, err := owner.Move(ctx, reversalGuardLoginMove(6, reversalGuardBagSlot, reversalGuardWornSlot, reversalGuardOtherWeaponItemID))
	if err != nil {
		t.Fatalf("Move error = %v, want a non-mutating refusal", err)
	}
	if result.Mode != "noop" || result.Changed {
		t.Fatalf("result = %+v, want Mode=noop Changed=false (the client never named %d)",
			result, reversalGuardWeaponItemID)
	}
	if result.NoopGate != equipmentLoginWearUndeclaredReversalGate {
		t.Fatalf("noopGate = %q, want %q", result.NoopGate, equipmentLoginWearUndeclaredReversalGate)
	}
	if got := loadTestEquipment(t, ctx, repos, reversalGuardCharTest78).Entries["12"].ItemID; got != reversalGuardWeaponItemID {
		t.Fatalf("slot 12 item = %d, want %d still worn", got, reversalGuardWeaponItemID)
	}
	if slot, inBag := reversalGuardBagHolds(t, ctx, repos, reversalGuardCharTest78, reversalGuardWeaponItemID); inBag {
		t.Fatalf("the unnamed item was moved into bag %s", slot)
	}
}

// 回退档（off = 今天）：同一条包逐字节复现"卸下一件客户端没点名的装备"。
func TestEquipmentLoginWearUndeclaredReversalRollbackReproducesTodaysUnequip(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD", "off")
	ctx, repos, owner := reversalGuardFixture(t, reversalGuardCharTest78,
		reversalGuardEntry(reversalGuardWornSlot, reversalGuardWeaponItemID))

	result, err := owner.Move(ctx, reversalGuardLoginMove(6, reversalGuardBagSlot, reversalGuardWornSlot, reversalGuardOtherWeaponItemID))
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "unequip" || !result.Changed || result.ItemID != reversalGuardWeaponItemID {
		t.Fatalf("rollback result = %+v, want today's unequip of the unnamed item %d", result, reversalGuardWeaponItemID)
	}
	if _, still := loadTestEquipment(t, ctx, repos, reversalGuardCharTest78).Entries["12"]; still {
		t.Fatal("rollback tier must leave slot 12 empty (today's behaviour)")
	}
	if _, inBag := reversalGuardBagHolds(t, ctx, repos, reversalGuardCharTest78, reversalGuardWeaponItemID); !inBag {
		t.Fatal("rollback tier must move the unnamed item into the bag (today's behaviour)")
	}
}

// **范围边界**（这条是"宁可少拦不可多拦"的落地）：不在登录复述上下文里时，本闸门完全
// 惰性 —— 今天那条"客户端选中的背包落点过期（被占）时改落到下一个真空槽"的特性
// （owner_test.go 的 TestOwnerMoveUnequipUsesNextEmptyMainSlotFromStaleClientDestination）
// 一个字节都不许变。
func TestEquipmentLoginWearUndeclaredReversalStaysInertOutsideTheLoginContext(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD", "")
	ctx, repos, owner := reversalGuardFixture(t, reversalGuardCharTest78,
		reversalGuardEntry(reversalGuardWornSlot, reversalGuardWeaponItemID))

	result, err := owner.Move(ctx, reversalGuardMove(6, reversalGuardBagSlot, reversalGuardWornSlot, reversalGuardOtherWeaponItemID))
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "unequip" || !result.Changed || result.ItemID != reversalGuardWeaponItemID {
		t.Fatalf("result = %+v, want today's behaviour outside the login context", result)
	}
	if _, inBag := reversalGuardBagHolds(t, ctx, repos, reversalGuardCharTest78, reversalGuardWeaponItemID); !inBag {
		t.Fatal("the stale-destination unequip must keep working outside the login context (today)")
	}
}

// 约束三的回归：**玩家真实脱下必须继续生效**。现场全部 65 次 unequip 都是这个形状
// （客户端点名的那件正是穿着的那件），所以本闸门对它们必须完全惰性 —— 开关两侧都真卸。
//
// 2026-09-28 第五轮：这个形状（装备端点指名穿着的那件 + 源端为空）现在由**方向判定
// 本身**判 noop（worn_slot_reassert_gate.go），与窗口无关。本用例的主题是
// equipmentLoginWearUndeclaredWearReversal 的惰性，因此把方向判定显式写到回退字面量：
// 这句话（"本闸门不改这些包"）只有在"旧口径真的会卸"的前提下才可证。
// 默认档下同一条包判 `noopGate=worn_slot_reassert`，见
// equipment_worn_slot_reassert_test.go。
func TestEquipmentLoginWearUndeclaredReversalLeavesTheGenuineUnequipAlone(t *testing.T) {
	t.Setenv(wornSlotReassertSwitchEnv, "off")
	cases := []struct {
		name         string
		characterKey string
		characterID  uint16
		slot         int16
		itemID       int64
		tier         string
	}{
		{"char 6 slot 12 beamsword (live 101040493)", reversalGuardCharTest78, 6, reversalGuardWornSlot, reversalGuardWeaponItemID, ""},
		{"char 6 slot 17 armour (live 09:09:20.875)", reversalGuardCharTest78, 6, reversalGuardArmorSlot, reversalGuardArmorItemID, ""},
		{"char 1 slot 12 beamsword (live 101010856)", reversalGuardCharOne, 1, reversalGuardWornSlot, reversalGuardOtherWeaponItemID, ""},
		{"char 6 slot 12 under the rollback literal", reversalGuardCharTest78, 6, reversalGuardWornSlot, reversalGuardWeaponItemID, "off"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
			t.Setenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD", testCase.tier)
			ctx, repos, owner := reversalGuardFixture(t, testCase.characterKey,
				reversalGuardEntry(testCase.slot, testCase.itemID))

			result, err := owner.Move(ctx, reversalGuardMove(testCase.characterID, reversalGuardBagSlot, testCase.slot, testCase.itemID))
			if err != nil {
				t.Fatalf("Move error = %v", err)
			}
			if result.Mode != "unequip" || !result.Changed || result.ItemID != testCase.itemID {
				t.Fatalf("result = %+v, want the genuine unequip of %d to go through", result, testCase.itemID)
			}
			if _, still := loadTestEquipment(t, ctx, repos, testCase.characterKey).Entries[reversalGuardItemKey(int64(testCase.slot))]; still {
				t.Fatal("the genuine unequip left the item worn")
			}
			if _, inBag := reversalGuardBagHolds(t, ctx, repos, testCase.characterKey, testCase.itemID); !inBag {
				t.Fatal("the genuine unequip did not move the item into the bag")
			}
		})
	}
}

// 判据边界表（两侧都钉）：只有"登录复述上下文 + 反转分支真会执行 + 客户端点名了别的 item"
// 才为真。
func TestEquipmentLoginWearUndeclaredWearReversalBoundary(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD", "")
	worn := dnfrepo.EquipmentEntry{SlotIndex: reversalGuardWornSlot, ItemID: reversalGuardWeaponItemID}
	live := reversalGuardLoginMove(6, reversalGuardBagSlot, reversalGuardWornSlot, reversalGuardOtherWeaponItemID)
	mutate := func(f func(*MoveCommand)) MoveCommand {
		cmd := live
		f(&cmd)
		return cmd
	}
	for name, testCase := range map[string]struct {
		cmd        MoveCommand
		occupied   bool
		want       bool
		whyChanged string
	}{
		"login context, client names another item": {live, true, true, ""},
		"client names the worn item": {mutate(func(c *MoveCommand) {
			c.DestinationInstanceValue = int32(reversalGuardWeaponItemID)
		}), true, false, "现场全部 65 次真实脱下就是这个形状（走 login_wear_reassert_noop）"},
		"outside the login context": {mutate(func(c *MoveCommand) {
			c.LoginWearWindowOpen = false
		}), true, false, "普通时段一个字节都不许变（见 StaysInertOutsideTheLoginContext）"},
		"no destination identity": {mutate(func(c *MoveCommand) {
			c.DestinationInstanceValue = 0
		}), true, false, "客户端什么都没点名，不属于本闸门"},
		"source identity present": {mutate(func(c *MoveCommand) {
			c.SourceInstanceValue = int32(reversalGuardWeaponItemID)
		}), true, false, "那是 identity_echo 那一族"},
		"destination not occupied": {live, false, false, "反转分支根本不会执行"},
	} {
		t.Run(name, func(t *testing.T) {
			got := equipmentLoginWearUndeclaredWearReversal(testCase.cmd, worn, testCase.occupied)
			if got != testCase.want {
				t.Fatalf("guard = %t, want %t (%s)", got, testCase.want, testCase.whyChanged)
			}
		})
	}

	// 回退字面量必须让它彻底惰性（= 今天）。
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD", "off")
	if equipmentLoginWearUndeclaredWearReversal(live, worn, true) {
		t.Fatal("the rollback literal must make the guard inert (today's behaviour)")
	}
	// 主开关关闭时同样惰性（本闸门只在登录复述那一套开关成立时才存在）。
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_REVERSAL_IDENTITY_GUARD", "")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "0")
	if equipmentLoginWearUndeclaredWearReversal(live, worn, true) {
		t.Fatal("the guard must stay inert while DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP is off")
	}
}
