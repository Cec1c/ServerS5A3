package equip

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 兜底闸门的三件事：
//  1. 默认档（开关未设/为 0）= 今天的行为：这条包照旧按 unequip 执行；
//  2. 开关打开但**窗口未开**时同样不生效（窗口是唯一的范围限定）；
//  3. 只有现场那一种形状（背包端点空 + 背包端点实例 0 + 装备端点实例 == 槽上那件
//     物品的 item id）才被吞掉，任何形变都必须落回旧口径。
//
// 现场包（HK 2026-09-27 09:44 会话 game-000020 pkt_seq=127）：
//
//	hex 01130029000000b82c62ba1d000012000000000001000000030c006dc1050600000000ffffffff0000
//	src=(list0,18,IV=0) dst=(list3,12,IV=101040493) count=1
func TestEquipmentLoginWearReassertNoopIsOffByDefault(t *testing.T) {
	worn := dnfrepo.EquipmentEntry{SlotIndex: 12, ItemID: reloginBeamswordItemID}
	live := MoveCommand{
		SourceListType:           wireListMain,
		SourceSlotIndex:          18,
		SourceInstanceValue:      0,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     12,
		DestinationInstanceValue: int32(reloginBeamswordItemID),
		LoginWearWindowOpen:      true,
	}
	if equipmentLoginWearReassertNoop(live, true, worn, true) {
		t.Fatal("the gate must be inert while DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP is unset")
	}
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "0")
	if equipmentLoginWearReassertNoop(live, true, worn, true) {
		t.Fatal("the gate must be inert when the switch is 0")
	}
	// 厂商同源闸门（equipmentEndpointIdentityEcho）对这条包同样必须保持惰性：
	// 它的判据要求 SourceInstanceValue > 0，不许为了迎合这条包放宽。
	if equipmentEndpointIdentityEcho(live, true, worn, true) {
		t.Fatal("the vendor echo gate must stay inert for the live login shape")
	}
}

func TestEquipmentLoginWearReassertNoopRequiresSwitchWindowAndShape(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	worn := dnfrepo.EquipmentEntry{SlotIndex: 12, ItemID: reloginBeamswordItemID}
	live := MoveCommand{
		SourceListType:           wireListMain,
		SourceSlotIndex:          18,
		SourceInstanceValue:      0,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     12,
		DestinationInstanceValue: int32(reloginBeamswordItemID),
		LoginWearWindowOpen:      true,
	}
	if !equipmentLoginWearReassertNoop(live, true, worn, true) {
		t.Fatal("switch on + window open + live shape must be recognised")
	}
	mutate := func(f func(*MoveCommand)) MoveCommand {
		cmd := live
		f(&cmd)
		return cmd
	}
	for name, test := range map[string]struct {
		cmd        MoveCommand
		aEmpty     bool
		equipped   dnfrepo.EquipmentEntry
		bOccupied  bool
		wantNoop   bool
		whyChanged string
	}{
		"live login shape": {live, true, worn, true, true, ""},
		"window closed (switch on, packet outside the window)": {mutate(func(c *MoveCommand) {
			c.LoginWearWindowOpen = false
		}), true, worn, true, false, "the window is the only scope limiter"},
		"operator unequip keeps its identity on the worn endpoint": {mutate(func(c *MoveCommand) {
			c.SourceInstanceValue = int32(reloginBeamswordItemID)
		}), true, worn, true, false, "identity on the source = the real unequip/equip family"},
		"destination identity is another item": {mutate(func(c *MoveCommand) {
			c.DestinationInstanceValue = 3037
		}), true, worn, true, false, "the client names an item that is not worn there"},
		"destination identity missing": {mutate(func(c *MoveCommand) {
			c.DestinationInstanceValue = 0
		}), true, worn, true, false, "no identity at all"},
		"source endpoint occupied":     {live, false, worn, true, false, "there is a real source stack to move"},
		"worn endpoint empty":          {live, true, worn, false, false, "nothing is worn to protect"},
		"worn slot empty":              {live, true, dnfrepo.EquipmentEntry{SlotIndex: 12}, true, false, "no worn item id to match"},
		"source is the worn container": {mutate(func(c *MoveCommand) { c.SourceListType = wireListEquipment }), true, worn, true, false, "both endpoints worn"},
		"destination is not the worn container": {mutate(func(c *MoveCommand) {
			c.DestinationListType = wireListMain
		}), true, worn, true, false, "not an equipment target"},
		"multi count": {mutate(func(c *MoveCommand) { c.MoveCount = 2 }), true, worn, true, false, "op19 move count is always 1"},
		"avatar bag endpoint": {mutate(func(c *MoveCommand) {
			c.SourceListType = wireListAvatar
			c.SourceSlotIndex = 8
			c.DestinationSlotIndex = 8
		}), true, dnfrepo.EquipmentEntry{SlotIndex: 8, ItemID: reloginBeamswordItemID}, true, true, "same family on list1 → list3"},
	} {
		t.Run(name, func(t *testing.T) {
			got := equipmentLoginWearReassertNoop(test.cmd, test.aEmpty, test.equipped, test.bOccupied)
			if got != test.wantNoop {
				t.Fatalf("noop = %t, want %t (%s)", got, test.wantNoop, test.whyChanged)
			}
			// 兜底闸门打开时，厂商同源闸门对同一族包仍必须保持惰性（判据未被改写）。
			if equipmentEndpointIdentityEcho(test.cmd, test.aEmpty, test.equipped, test.bOccupied) {
				t.Fatal("equipmentEndpointIdentityEcho must not absorb the srcIV==0 family")
			}
		})
	}
}
