package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// DOC-067 端到端（真实桥接，不把 handler 结果手工拼回去）：
//
//  1. legacy cmd=1/op713 经 handleGameStreamPacket → handleAlignedGameCommand → inventory owner 落库；
//  2. 回包只有一条 class1/713 `1 + list + slot`（客户端自己应用状态，服务端不再补发容器）；
//  3. 冷登录/整表 op13 与增量 op14 都必须把 0x77 行 0x60 投影出去，同时钱包（0:0 金币）、
//     账号共享晶块（0:359）、品级种子与装备属性逐项不回退；
//  4. 解锁后同一投影必须显式回到 0。
func TestSortLockBridgePersistsAndProjectsLoginAndIncrementalRows(t *testing.T) {
	ctx := context.Background()
	f := newRefreshCostFixture(t)
	f.warmUp(t)
	pair914SeedRows(t, f)

	// 1) 真实 legacy 上行：cmd=1 type=713，list 0 slot 14（pair914 的带品级/属性外套）。
	f.buffer.write.Reset()
	packet := buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketRequestItemSortLock), 901, []byte{0, 14, 0})
	if err := f.service.handleGameStreamPacket(f.session, dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamLegacy, Data: packet}); err != nil {
		t.Fatalf("legacy sort-lock dispatch: %v", err)
	}
	frames := splitSortLockFrames(t, f.buffer.write.Bytes())
	if len(frames) != 1 {
		t.Fatalf("lock reply frames = %d, want exactly the class1 ACK", len(frames))
	}
	ack := frames[0]
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketRequestItemSortLock) || ack.Header.Classification != 1 ||
		string(ack.Body) != string([]byte{1, 0, 14, 0}) {
		t.Fatalf("lock ACK header=%+v body=%x", ack.Header, ack.Body)
	}

	// 2) 落库：0:14 带 sort_lock，其余 Extra 不变。
	inventory, found, err := f.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	locked := inventory.Slots["0:14"]
	if locked.Extra[dnfinventory.SortLockExtraKey] != "1" {
		t.Fatalf("lock not durable: %+v", locked.Extra)
	}
	if locked.Extra["quality_seed"] != "999999998" || locked.Extra["reinforce"] != "6" {
		t.Fatalf("lock write clobbered durable extras: %+v", locked.Extra)
	}

	// 3) 冷登录/整表 op13：0x60=1，钱包/晶块/品级/属性不变。
	f.buffer.write.Reset()
	if err := f.service.sendSelectedCurrentContainerListsWithRefresh(f.session, "sort_lock_login_projection", true); err != nil {
		t.Fatal(err)
	}
	requireSortLockPageWithPairIntact(t, findMainListRefreshBody(t, splitSortLockFrames(t, f.buffer.write.Bytes())), 14, 1, "login_op13")

	// 4) 714 解锁（走桥接 aligned 入口）→ 增量 op14 单行显式回 0。
	f.buffer.write.Reset()
	handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketRequestItemSortUnlock), []byte{0, 14, 0})
	if err != nil || !handled {
		t.Fatalf("unlock dispatch handled=%t err=%v", handled, err)
	}
	unlock := splitSortLockFrames(t, f.buffer.write.Bytes())
	if len(unlock) != 1 || unlock[0].Header.MsgID != uint16(dnfenum.CmdPacketRequestItemSortUnlock) || unlock[0].Header.Classification != 1 {
		t.Fatalf("unlock reply = %+v", unlock)
	}
	inventory, _, err = f.group.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := inventory.Slots["0:14"].Extra[dnfinventory.SortLockExtraKey]; present {
		t.Fatalf("unlock left the key: %+v", inventory.Slots["0:14"].Extra)
	}

	f.buffer.write.Reset()
	if err := f.service.sendSelectedIncrementalItemSlotRefreshes(f.session, "sort_lock_incremental",
		[]alignedcmd.ItemSlotRefresh{{ListType: dnfrepo.MainInventoryListType, SlotIndex: 14}}); err != nil {
		t.Fatal(err)
	}
	incremental := splitSortLockFrames(t, f.buffer.write.Bytes())
	if len(incremental) != 1 {
		t.Fatalf("incremental frames = %d want 1", len(incremental))
	}
	body := incremental[0].Body
	if incremental[0].Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) || incremental[0].Header.Classification != 0 ||
		len(body) != 3+currentItemListEntryWireSize || body[0] != dnfrepo.MainInventoryListType || binary.LittleEndian.Uint16(body[1:3]) != 1 {
		t.Fatalf("incremental op14 frame = %+v body=%x", incremental[0].Header, body)
	}
	requireSortLockRowIntact(t, sortLockRowFromUpdateBody(t, body, 14), 0, "incremental_op14")
}

// splitSortLockFrames 按当前 16 字节 upper 头切帧（splitAllCurrentGameServerUpperPackets 用
// 的是 13 字节旧头，会把 16 字节头的尾部 3 字节并进 Body）。
func splitSortLockFrames(t *testing.T, raw []byte) []dnfproto.ChannelPacket {
	t.Helper()
	frames := make([]dnfproto.ChannelPacket, 0, 4)
	for rest := raw; len(rest) > 0; {
		frame, tail := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
		frames = append(frames, frame)
		rest = tail
	}
	return frames
}

// findMainListRefreshBody 取整表 op13 里 list 0 的容器页包体：页头是
// listType + u16 容器档位 + u16 行数（5 字节）、行固定 0x77；用长度校验把同 msg id 的
// 容器状态帧排除掉。
func findMainListRefreshBody(t *testing.T, frames []dnfproto.ChannelPacket) []byte {
	t.Helper()
	for _, frame := range frames {
		if frame.Header.Classification != 0 || frame.Header.MsgID != 13 || len(frame.Body) < 5 {
			continue
		}
		if frame.Body[0] != dnfrepo.MainInventoryListType {
			continue
		}
		count := int(binary.LittleEndian.Uint16(frame.Body[3:5]))
		if len(frame.Body) == 5+count*currentItemListEntryWireSize {
			return frame.Body
		}
	}
	t.Fatalf("no list-0 op13 container page among %d frames", len(frames))
	return nil
}

// sortLockRowFromUpdateBody 在 op14 增量体（listType + u16 行数 + 行）里取指定槽的行。
func sortLockRowFromUpdateBody(t *testing.T, body []byte, slot int16) []byte {
	t.Helper()
	if len(body) < 3 {
		t.Fatalf("op14 body too short: % x", body)
	}
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	if len(body) != 3+count*currentItemListEntryWireSize {
		t.Fatalf("op14 body declares %d rows but is %d bytes", count, len(body))
	}
	for index := 0; index < count; index++ {
		row := body[3+index*currentItemListEntryWireSize : 3+(index+1)*currentItemListEntryWireSize]
		if int16(binary.LittleEndian.Uint16(row[0:2])) == slot {
			return row
		}
	}
	t.Fatalf("op14 body has no slot %d row", slot)
	return nil
}

// requireSortLockRowIntact 断言单个 0x77 行的位置锁字节、品级种子、强化字节、属性与附魔字节。
func requireSortLockRowIntact(t *testing.T, row []byte, wantLock byte, label string) {
	t.Helper()
	if row[dnfinventory.SortLockRowOffset] != wantLock {
		t.Fatalf("%s: row 0x60 = %d want %d (row=%x)", label, row[dnfinventory.SortLockRowOffset], wantLock, row)
	}
	if seed := binary.LittleEndian.Uint32(row[6:10]); seed != 999999998 {
		t.Fatalf("%s: quality seed = %d want 999999998", label, seed)
	}
	if row[10] != 6 || binary.LittleEndian.Uint32(row[14:18]) != 10007563 ||
		!bytes.Equal(row[0x3c:0x45], []byte{2, 255, 255, 255, 255, 255, 255, 255, 255}) {
		t.Fatalf("%s: coat attributes changed: %x", label, row)
	}
}

// requireSortLockPageWithPairIntact 断言 list 0 容器页里该槽的行，外加油钱包行与账号共享晶块行。
func requireSortLockPageWithPairIntact(t *testing.T, body []byte, slot int16, wantLock byte, label string) {
	t.Helper()
	row, found := pair914Row(t, body, slot)
	if !found {
		t.Fatalf("%s: slot %d row missing", label, slot)
	}
	requireSortLockRowIntact(t, row, wantLock, label)
	wallet, found := pair914Row(t, body, 0)
	if !found || binary.LittleEndian.Uint32(wallet[6:10]) != 123456 {
		t.Fatalf("%s: wallet row missing/wrong: found=%t row=%x", label, found, wallet)
	}
	crystal, found := pair914Row(t, body, 359)
	if !found || binary.LittleEndian.Uint32(crystal[6:10]) != 5000 {
		t.Fatalf("%s: shared crystal row missing/wrong: found=%t row=%x", label, found, crystal)
	}
}
