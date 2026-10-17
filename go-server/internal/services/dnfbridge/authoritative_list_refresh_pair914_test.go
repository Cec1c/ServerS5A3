package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场回归（2026-10-14 role 106 / conn game-000039 / op272）：模块侧那条
// class0/0x000D list 0 整表刷新只带角色 slots，缺 0:0 钱包行与账号共享的
// 0:354..0:365，并把装备行的 raw+0x06 写成堆叠数 1（品级种子应为 999999998）。
// 本文件钉住桥侧整条替换：
//
//  1. 被点名的 list 0 必须换成会话权威投影——逐字节等于
//     buildCurrentItemListBodyForSession 的输出，并带钱包行/账号共享行/品级/孔位/符文；
//  2. 未被点名时逐字节保持今天的行为（不扩散到其它 opcode）；
//  3. 权威投影建不出来时跳过刷新、**绝不**发模块快照，且 class1 ACK 照发。

const (
	pair914CoatItemID   = int64(100070550)
	pair914CardItemID   = int64(10007563)
	pair914GoldCubeID   = int64(3262)
	pair914GoldCubeSlot = int16(359)
	pair914RuneID       = uint16(1234)
)

// pair914ModuleSnapshotBody 复刻模块侧自己拼的那条 body 的关键形状：整表，但只有
// 一行 0:14、raw+0x06 = 1（堆叠数），而且没有 0:0 钱包行。
func pair914ModuleSnapshotBody() []byte {
	row := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(row[0x00:0x02], 14)
	binary.LittleEndian.PutUint32(row[0x02:0x06], uint32(pair914CoatItemID))
	binary.LittleEndian.PutUint32(row[0x06:0x0A], 1)
	return append([]byte{0, 0, 0, 1, 0}, row...)
}

func pair914ModuleResult(snapshot []byte, marked []byte) alignedcmd.Result {
	return alignedcmd.Result{
		Operation: "enchant_by_bead",
		UpperResponses: []alignedcmd.UpperResponse{
			{MsgID: uint16(dnfenum.CmdPacketLeaveParty), Body: snapshot, Classification: 0, AllowCodec: true},
			{MsgID: uint16(dnfenum.CmdPacketEnchantByBead), Body: []byte{1, 0, 14, 0}, Classification: dnfproto.DefaultChannelClassification, AllowCodec: true},
		},
		AuthoritativeListRefreshes: marked,
	}
}

func pair914SeedRows(t *testing.T, fixture *refreshCostFixture) {
	t.Helper()
	ctx := context.Background()
	record, found, err := fixture.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if record.Slots == nil {
		record.Slots = map[string]dnfrepo.ItemStack{}
	}
	record.Slots["0:14"] = dnfrepo.ItemStack{ItemID: pair914CoatItemID, Count: 1, Extra: map[string]string{
		"item_kind":             "equipment",
		"equipment_type":        "[coat]",
		"quality_seed":          "999999998",
		"reinforce":             "6",
		"ext_data0":             "6",
		"value_a":               "10007563",
		"equipment_emblem_data": "02ffffffffffffffff",
		"equipment_effect_id":   "1234",
	}}
	if err := fixture.group.Inventory.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	account, _, err := fixture.group.AccountInventory.Load(ctx, "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	if account.Slots == nil {
		account.Slots = map[string]dnfrepo.ItemStack{}
	}
	account.Slots["0:359"] = dnfrepo.ItemStack{ItemID: pair914GoldCubeID, Count: 5000, Extra: map[string]string{"item_kind": "stackable"}}
	if err := fixture.group.AccountInventory.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
}

func pair914Row(t *testing.T, body []byte, slot int16) ([]byte, bool) {
	t.Helper()
	if len(body) < 5 || body[0] != dnfrepo.MainInventoryListType {
		t.Fatalf("list-0 body header = %x", body)
	}
	count := int(binary.LittleEndian.Uint16(body[3:5]))
	if len(body) < 5+count*currentItemListEntryWireSize {
		t.Fatalf("body declares %d rows but is %d bytes", count, len(body))
	}
	for index := 0; index < count; index++ {
		row := body[5+index*currentItemListEntryWireSize : 5+(index+1)*currentItemListEntryWireSize]
		if int16(binary.LittleEndian.Uint16(row[0x00:0x02])) == slot {
			return row, true
		}
	}
	return nil, false
}

func TestOp272AuthoritativeList0RefreshReplacesModuleSnapshot(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	pair914SeedRows(t, fixture)
	fixture.buffer.write.Reset()

	snapshot := pair914ModuleSnapshotBody()
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, pair914ModuleResult(snapshot, []byte{dnfrepo.MainInventoryListType})); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	raw := append([]byte(nil), fixture.buffer.write.Bytes()...)
	refresh, rest := splitGameServerUpperPacketWithHeader(t, raw, dnfproto.GameServerUpperHeaderSize16)
	if refresh.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) || refresh.Header.Classification != 0 {
		t.Fatalf("first frame = class%d msg0x%04X, want class0 op13", refresh.Header.Classification, refresh.Header.MsgID)
	}
	ack, rest := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketEnchantByBead) {
		t.Fatalf("second frame = msg0x%04X, want the op272 ACK", ack.Header.MsgID)
	}
	if !bytes.Equal(ack.Body, []byte{1, 0, 14, 0}) {
		t.Fatalf("ack body = %x, want 01000e00", ack.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes: %d", len(rest))
	}

	// 1. 发出去的 body 必须逐字节等于会话权威投影。
	want, _, wantCount, ok := fixture.service.buildCurrentItemListBodyForSession(context.Background(), fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("authoritative projection build failed")
	}
	if !bytes.Equal(refresh.Body, want) {
		t.Fatalf("list-0 refresh is not the session projection: rows=%d len(got)=%d len(want)=%d", wantCount, len(refresh.Body), len(want))
	}
	if bytes.Equal(refresh.Body, snapshot) {
		t.Fatal("module snapshot body still reached the client")
	}

	// 2. 三处现场回归在权威 body 里必须都正确。
	wallet, found := pair914Row(t, refresh.Body, 0)
	if !found {
		t.Fatal("authoritative list-0 refresh dropped the reserved 0:0 wallet row")
	}
	if item := binary.LittleEndian.Uint32(wallet[0x02:0x06]); item != 0 {
		t.Fatalf("wallet row item = %d, want 0", item)
	}
	if amount := binary.LittleEndian.Uint32(wallet[0x06:0x0A]); amount != 123456 {
		t.Fatalf("wallet row amount = %d, want 123456 (fixture character gold)", amount)
	}

	coat, found := pair914Row(t, refresh.Body, 14)
	if !found {
		t.Fatal("authoritative list-0 refresh dropped the coat row")
	}
	if seed := binary.LittleEndian.Uint32(coat[0x06:0x0A]); seed != 999999998 {
		t.Fatalf("coat raw+0x06 = %d, want the quality seed 999999998 (module snapshot wrote 1)", seed)
	}
	if level := coat[0x0A]; level != 6 {
		t.Fatalf("coat raw+0x0A reinforce = %d, want 6", level)
	}
	if card := binary.LittleEndian.Uint32(coat[0x0E:0x12]); card != uint32(pair914CardItemID) {
		t.Fatalf("coat raw+0x0E enchant card = %d, want %d", card, pair914CardItemID)
	}
	if sockets := coat[currentEquipmentVectorOffset]; sockets != 2 {
		t.Fatalf("coat raw+0x3C socket count = %d, want 2", sockets)
	}
	if first := binary.LittleEndian.Uint32(coat[currentEquipmentVectorOffset+1 : currentEquipmentVectorOffset+5]); first != 0xFFFFFFFF {
		t.Fatalf("coat first emblem dword = 0x%08X, want 0xFFFFFFFF", first)
	}
	if rune := binary.LittleEndian.Uint16(coat[currentEquipmentEffectRuneWireOffset : currentEquipmentEffectRuneWireOffset+2]); rune != pair914RuneID {
		t.Fatalf("coat effect rune = %d, want %d", rune, pair914RuneID)
	}

	cube, found := pair914Row(t, refresh.Body, pair914GoldCubeSlot)
	if !found {
		t.Fatal("authoritative list-0 refresh dropped the account-shared crystal row")
	}
	if item := binary.LittleEndian.Uint32(cube[0x02:0x06]); item != uint32(pair914GoldCubeID) {
		t.Fatalf("crystal row item = %d, want %d", item, pair914GoldCubeID)
	}
	if count := binary.LittleEndian.Uint32(cube[0x06:0x0A]); count != 5000 {
		t.Fatalf("crystal row count = %d, want 5000", count)
	}
}

// 不扩散：没有被点名时，模块给的 body 逐字节上线（= 今天的行为，其它 opcode 不受影响）。
func TestUnmarkedList0RefreshBodyStaysVerbatim(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	fixture.buffer.write.Reset()

	snapshot := pair914ModuleSnapshotBody()
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, pair914ModuleResult(snapshot, nil)); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	raw := append([]byte(nil), fixture.buffer.write.Bytes()...)
	refresh, _ := splitGameServerUpperPacketWithHeader(t, raw, dnfproto.GameServerUpperHeaderSize16)
	if refresh.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) || refresh.Header.Classification != 0 {
		t.Fatalf("first frame = class%d msg0x%04X, want class0 op13", refresh.Header.Classification, refresh.Header.MsgID)
	}
	if !bytes.Equal(refresh.Body, snapshot) {
		t.Fatalf("unmarked refresh body changed: got=%x want=%x", refresh.Body, snapshot)
	}
	if _, found := pair914Row(t, refresh.Body, 0); found {
		t.Fatal("the unmarked body must stay exactly as the module built it (no wallet row injected)")
	}
}

// 权威投影建不出来时：跳过这一条刷新（绝不用模块快照兜底），但 class1 ACK 必须照发。
func TestAuthoritativeRefreshBuildFailureSkipsRefreshButKeepsAck(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	fixture.buffer.write.Reset()
	fixture.session.selectedCharacterID = 0

	if err := fixture.service.sendAlignedUpperResponses(fixture.session, pair914ModuleResult(pair914ModuleSnapshotBody(), []byte{dnfrepo.MainInventoryListType})); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	raw := append([]byte(nil), fixture.buffer.write.Bytes()...)
	frame, rest := splitGameServerUpperPacketWithHeader(t, raw, dnfproto.GameServerUpperHeaderSize16)
	if frame.Header.MsgID != uint16(dnfenum.CmdPacketEnchantByBead) {
		t.Fatalf("module snapshot body must not reach the client: first frame = msg0x%04X", frame.Header.MsgID)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes: %d", len(rest))
	}
}
