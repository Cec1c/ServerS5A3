package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住 **双端点同名**（`srcIV == dstIV != 0`）这一类 op19 在桥侧的完整往返：
// 包体原文取自线上 `/opt/dnf90/logs/packet_log.txt`，ACK 字节与**今天线上发出去的**
// 那 11 字节逐字节比对。闸门本身在域层（equip/owner.go 的
// equipmentDuplicateEndpointIdentity），桥侧这里只用真解码器 + 真应答编码器复核。
//
// 现场对照（packet_log.txt，角色 6，conn game-000003）：
//
//	75187 RECV -> equipMove=equip     -> SEND msg_id=19 body=01000a0001000000031100
//	75197 RECV -> equipMove=unequip   -> SEND msg_id=19 body=01000b0001000000030c00   <= 今天
//	75197 RECV -> equipMove=noop      -> SEND msg_id=19 body=01000b0000000000030c00   <= 修复后
//	75012 SEND（登录复述 noop 的 ACK，同形）= 01000b0000000000030c00                 <= 既有零结果形状
const (
	duplicateIdentityPacketWeaponID  int32 = 101040493 // 角色 6 槽 12 的武器
	duplicateIdentityPacketArmorID   int32 = 100270515 // 75187 装回槽 17 的那件
	duplicateIdentityPacketCharOneID int32 = 101010856 // 角色 1 槽 12 的武器（71705）

	duplicateIdentityPacketWornSlot  int16 = 12
	duplicateIdentityPacketArmorSlot int16 = 17

	duplicateIdentityPacketFrameDoubleIdent   = "01130029000000baaed1b90c01000b006dc1050601000000030c006dc1050600000000ffffffff0000"
	duplicateIdentityPacketFrameEquip         = "01130029000000ea5d16dc0b01000a00b301fa05010000000311000000000000000000ffffffff0000"
	duplicateIdentityPacketFrameOccupied      = "01130029000000cd3b79f46e00001300a84d050601000000030c00a84d050600000000ffffffff0000"
	duplicateIdentityPacketFrameLoginReassert = "0113002900000092928abb3000000b000000000001000000030c006dc1050600000000ffffffff0000"

	duplicateIdentityPacketAckToday = "01000b0001000000030c00" // 今天（75197 的真卸下）
	duplicateIdentityPacketAckNoop  = "01000b0000000000030c00" // 修复后（= 75012 既有零结果形状）
	duplicateIdentityPacketAckEquip = "01000a0001000000031100" // 75187 的真穿戴
)

func duplicateIdentityPacketBody(t *testing.T, frameHex string) []byte {
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

func duplicateIdentityPacketBagStack(slot int16, itemID int32) dnfrepo.ItemStack {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	return dnfrepo.ItemStack{
		ItemID:   int64(itemID),
		Count:    1,
		RawEntry: raw,
		Extra: map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       "equipment/character/swordman/weapon/beamsword/x.equ",
			"equipment_type": "[weapon]",
			"durability":     "48",
			"max_durability": "48",
			"quality_grade":  "0",
			"equipment_slot": "9",
		},
	}
}

// duplicateIdentityPacketSeedBag 往夹具里塞背包行（主背包 0:<slot>）。
func duplicateIdentityPacketSeedBag(t *testing.T, f *loginWearBridgeFixture, slot int16, itemID int32) {
	t.Helper()
	ctx := context.Background()
	if err := f.group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:" + strconv.Itoa(int(slot)): duplicateIdentityPacketBagStack(slot, itemID)},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatalf("seed bag: %v", err)
	}
}

func duplicateIdentityPacketWornID(t *testing.T, f *loginWearBridgeFixture, slot int16) int64 {
	t.Helper()
	record, ok, err := f.group.Equipment.Load(context.Background(), "19")
	if err != nil || !ok {
		t.Fatalf("equipment load ok=%t err=%v", ok, err)
	}
	if slot == duplicateIdentityPacketWornSlot {
		return record.Entries["12"].ItemID
	}
	return record.Entries["17"].ItemID
}

func duplicateIdentityPacketBagHas(t *testing.T, f *loginWearBridgeFixture, itemID int64) bool {
	t.Helper()
	record, ok, err := f.group.Inventory.Load(context.Background(), "19")
	if err != nil || !ok {
		t.Fatalf("inventory load ok=%t err=%v", ok, err)
	}
	for _, stack := range record.Slots {
		if stack.ItemID == itemID {
			return true
		}
	}
	return false
}

// TestEquipDuplicateIdentityPacketDecodesVerbatim 用**真解码器**核对原文（无环）：
// 这一条同时是域层测试里那份本地偏移量解码的交叉校验。
func TestEquipDuplicateIdentityPacketDecodesVerbatim(t *testing.T) {
	request, err := dnfinventory.DecodeMoveItemspaceRequest(duplicateIdentityPacketBody(t, duplicateIdentityPacketFrameDoubleIdent))
	if err != nil {
		t.Fatalf("decode live 75197 body: %v", err)
	}
	if request.SourceListType != 0 || request.SourceSlotIndex != 11 ||
		request.SourceInstanceValue != duplicateIdentityPacketWeaponID ||
		request.MoveCount != 1 || request.DestinationListType != 3 ||
		request.DestinationSlotIndex != duplicateIdentityPacketWornSlot ||
		request.DestinationInstanceValue != duplicateIdentityPacketWeaponID {
		t.Fatalf("live 75197 decoded = %s, want the double-ident shape", request.String())
	}
}

// TestEquipDuplicateIdentityRefusesTheLiveUnequipPacket 默认档：75197 原文必须只回
// 一条 moveValue=0 的 ACK，武器仍在槽 12，且这条包**不吃**登录复述窗口（第 4 轮那三个
// 收口对它一个都不会被调用）。
func TestEquipDuplicateIdentityRefusesTheLiveUnequipPacket(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	fixture := newLoginWearBridgeFixture(t)
	fixture.service.openEquipLoginWearWindow(fixture.session, "test")

	round := fixture.runOp19(t, duplicateIdentityPacketBody(t, duplicateIdentityPacketFrameDoubleIdent))
	if round.windowOpen {
		t.Fatal("the double-ident packet must not be treated as the login-reassert family (the 4th-round gates never see it)")
	}
	if !strings.Contains(round.result.Reason, "equipMove=noop") ||
		!strings.Contains(round.result.Reason, "noopGate=duplicate_endpoint_identity") {
		t.Fatalf("reason = %q, want equipMove=noop noopGate=duplicate_endpoint_identity", round.result.Reason)
	}
	if len(round.msgIDs) != 1 || round.msgIDs[0] != uint16(dnfenum.CmdPacketMoveItemspace) {
		t.Fatalf("packets = %v, want exactly one msg_id=19 ACK", round.msgIDs)
	}
	if got := hex.EncodeToString(firstPacketBody(t, round.stream)); got != duplicateIdentityPacketAckNoop {
		t.Fatalf("ACK body = %s, want %s (moveValue=0)", got, duplicateIdentityPacketAckNoop)
	}
	if got := duplicateIdentityPacketWornID(t, fixture, duplicateIdentityPacketWornSlot); got != int64(duplicateIdentityPacketWeaponID) {
		t.Fatalf("slot 12 item = %d, want %d still worn", got, duplicateIdentityPacketWeaponID)
	}
	if duplicateIdentityPacketBagHas(t, fixture, int64(duplicateIdentityPacketWeaponID)) {
		t.Fatal("the weapon was moved into the bag: this is the operator symptom (weapon auto-unequipped)")
	}
}

// TestEquipDuplicateIdentityRollbackKeepsTodaysBytes 回退字面量：同一条原文逐字节复现
// 今天的真卸下（ACK = 线上 75201 的 11 字节原文）。
func TestEquipDuplicateIdentityRollbackKeepsTodaysBytes(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "off")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	fixture := newLoginWearBridgeFixture(t)
	fixture.service.openEquipLoginWearWindow(fixture.session, "test")

	round := fixture.runOp19(t, duplicateIdentityPacketBody(t, duplicateIdentityPacketFrameDoubleIdent))
	if !strings.Contains(round.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback reason = %q, want today's unequip", round.result.Reason)
	}
	if got := hex.EncodeToString(firstPacketBody(t, round.stream)); got != duplicateIdentityPacketAckToday {
		t.Fatalf("rollback ACK body = %s, want today's live bytes %s", got, duplicateIdentityPacketAckToday)
	}
	if got := duplicateIdentityPacketWornID(t, fixture, duplicateIdentityPacketWornSlot); got != 0 {
		t.Fatalf("rollback tier must leave slot 12 empty (today), item = %d", got)
	}
	if !duplicateIdentityPacketBagHas(t, fixture, int64(duplicateIdentityPacketWeaponID)) {
		t.Fatal("rollback tier must move the weapon into the bag (today)")
	}
}

// TestEquipDuplicateIdentityLeavesTheLiveEquipPacketAlone：75187 原文（真穿戴）在
// 开关两侧都必须照旧：ACK 逐字节等于线上 75191 的 11 字节。
func TestEquipDuplicateIdentityLeavesTheLiveEquipPacketAlone(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	for _, tier := range []string{"", "off"} {
		t.Run("tier="+tier, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", tier)
			fixture := newLoginWearBridgeFixture(t)
			duplicateIdentityPacketSeedBag(t, fixture, 10, duplicateIdentityPacketArmorID)

			round := fixture.runOp19(t, duplicateIdentityPacketBody(t, duplicateIdentityPacketFrameEquip))
			if !strings.Contains(round.result.Reason, "equipMove=equip") {
				t.Fatalf("reason = %q, want equipMove=equip", round.result.Reason)
			}
			if got := hex.EncodeToString(firstPacketBody(t, round.stream)); got != duplicateIdentityPacketAckEquip {
				t.Fatalf("ACK body = %s, want the live 75191 bytes %s", got, duplicateIdentityPacketAckEquip)
			}
			if got := duplicateIdentityPacketWornID(t, fixture, duplicateIdentityPacketArmorSlot); got != int64(duplicateIdentityPacketArmorID) {
				t.Fatalf("slot 17 item = %d, want %d worn", got, duplicateIdentityPacketArmorID)
			}
			if duplicateIdentityPacketBagHas(t, fixture, int64(duplicateIdentityPacketArmorID)) {
				t.Fatal("the armour must have left the bag")
			}
		})
	}
}

// TestEquipDuplicateIdentityKeepsTheOccupiedBagEndpointSwap：71705 原文（双端点同名但
// 背包端点被占）今天判 equip_swap，本闸门不许改它。
func TestEquipDuplicateIdentityKeepsTheOccupiedBagEndpointSwap(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	fixture := newLoginWearBridgeFixture(t)
	ctx := context.Background()
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(duplicateIdentityPacketWornSlot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(duplicateIdentityPacketCharOneID))
	if err := fixture.group.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: duplicateIdentityPacketWornSlot, ItemID: int64(duplicateIdentityPacketCharOneID), RawEntry: raw,
				Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]", "equipped_slot": "12"}},
		},
	}); err != nil {
		t.Fatalf("seed equipment: %v", err)
	}
	duplicateIdentityPacketSeedBag(t, fixture, 19, 101010857)

	round := fixture.runOp19(t, duplicateIdentityPacketBody(t, duplicateIdentityPacketFrameOccupied))
	if !strings.Contains(round.result.Reason, "equipMove=equip_swap") {
		t.Fatalf("reason = %q, want today's equip_swap (the occupied-bag-endpoint half must not regress)", round.result.Reason)
	}
}

// TestEquipDuplicateIdentityKeepsTheLoginReassertFamily：A 族（75008 原文）在窗口内
// 仍然走第 4 轮的 login_wear_reassert_noop —— 两个闸门互不顶替。
func TestEquipDuplicateIdentityKeepsTheLoginReassertFamily(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_MOVE_DUPLICATE_IDENTITY_GUARD", "")
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	fixture := newLoginWearBridgeFixture(t)
	fixture.service.openEquipLoginWearWindow(fixture.session, "test")

	round := fixture.runOp19(t, duplicateIdentityPacketBody(t, duplicateIdentityPacketFrameLoginReassert))
	if !round.windowOpen {
		t.Fatal("the A shape must still be recognized as the login-reassert family")
	}
	if !strings.Contains(round.result.Reason, "noopGate=login_wear_reassert_noop") {
		t.Fatalf("reason = %q, want the pre-existing login_wear_reassert_noop", round.result.Reason)
	}
	if bytes.Contains([]byte(round.result.Reason), []byte("duplicate_endpoint_identity")) {
		t.Fatal("the new gate must not claim the A-shape family")
	}
}
