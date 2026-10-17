package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// DOC-067 即时刷新回归（真实桥接，2026-10-15）：713/714 的回包必须是两条——
//
//  1. class1/713（或 class1/714）成功 ACK `01 + u8 list + u16 slot`：当前客户端 handler
//     0x1faee80/0x1faeee0 直接写 item+0x2aa，是图标即时刷新的原生路径；但当前下发
//     客户端的入站准入位图（EXE .rebuild 0x6E83610 / 位表 0x6E92300）没有 713/714 位，
//     这条 ACK 会在派发前被丢掉（见 Docs/D33_SORT_LOCK_20261014.md 第 7 节）；
//  2. class0/0x000D 整容器刷新，body 逐字节 = 会话权威投影（buildCurrentItemListBodyForSession，
//     与登录整表/整理/搬运同一条 builder）；模块侧那条占位包体（只有列表号一个字节）
//     永远不上线。这条走准入位图里为真的 op13，未打补丁的客户端也能立刻看到蓝锁。
//
// 旧代码（只回 ACK、不排任何容器刷新）在本用例上 RED：第一次切帧后就没有第二条了。
func TestSortLockReplyCarriesAuthoritativeContainerRefresh(t *testing.T) {
	ctx := context.Background()
	f := newRefreshCostFixture(t)
	f.warmUp(t)
	pair914SeedRows(t, f)

	// 713：legacy cmd=1 真上行 → ACK + 整容器刷新。
	f.buffer.write.Reset()
	packet := buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketRequestItemSortLock), 901, []byte{0, 14, 0})
	if err := f.service.handleGameStreamPacket(f.session, dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamLegacy, Data: packet}); err != nil {
		t.Fatalf("legacy sort-lock dispatch: %v", err)
	}
	lockFrames := splitSortLockFrames(t, f.buffer.write.Bytes())
	if len(lockFrames) != 2 {
		t.Fatalf("lock reply frames = %d, want ACK + authoritative container refresh", len(lockFrames))
	}
	ack := lockFrames[0]
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketRequestItemSortLock) || ack.Header.Classification != 1 ||
		!bytes.Equal(ack.Body, []byte{1, 0, 14, 0}) {
		t.Fatalf("lock ACK header=%+v body=%x", ack.Header, ack.Body)
	}
	lockPage := findMainListRefreshBody(t, lockFrames)
	requireSortLockPageWithPairIntact(t, lockPage, 14, 1, "sort_lock_refresh_lock")
	requireSortLockRefreshIsSessionProjection(t, f, lockPage, "lock")

	// 714：aligned 入口 → 同一形状，0x60 显式回 0。
	f.buffer.write.Reset()
	handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketRequestItemSortUnlock), []byte{0, 14, 0})
	if err != nil || !handled {
		t.Fatalf("unlock dispatch handled=%t err=%v", handled, err)
	}
	unlockFrames := splitSortLockFrames(t, f.buffer.write.Bytes())
	if len(unlockFrames) != 2 {
		t.Fatalf("unlock reply frames = %d, want ACK + authoritative container refresh", len(unlockFrames))
	}
	if unlockFrames[0].Header.MsgID != uint16(dnfenum.CmdPacketRequestItemSortUnlock) ||
		unlockFrames[0].Header.Classification != 1 || !bytes.Equal(unlockFrames[0].Body, []byte{1, 0, 14, 0}) {
		t.Fatalf("unlock ACK header=%+v body=%x", unlockFrames[0].Header, unlockFrames[0].Body)
	}
	unlockPage := findMainListRefreshBody(t, unlockFrames)
	requireSortLockPageWithPairIntact(t, unlockPage, 14, 0, "sort_lock_refresh_unlock")
	requireSortLockRefreshIsSessionProjection(t, f, unlockPage, "unlock")
	_ = ctx
}

// requireSortLockRefreshIsSessionProjection 钉住「模块占位包体永远不上线」：
// 发出去的整容器 body 必须逐字节等于会话权威投影；投影不可用时桥接跳过这条刷新
// （绝不退回模块快照，那正是 2026-10-14 抹金币/降品级的来源）。
func requireSortLockRefreshIsSessionProjection(t *testing.T, f *refreshCostFixture, sent []byte, label string) {
	t.Helper()
	want, _, count, ok := f.service.buildCurrentItemListBodyForSession(context.Background(), f.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatalf("%s: authoritative list-0 projection unavailable", label)
	}
	if !bytes.Equal(sent, want) {
		t.Fatalf("%s: refresh is not the session projection (rows=%d sent=%d want=%d)", label, count, len(sent), len(want))
	}
	if len(sent) == 1 {
		t.Fatalf("%s: module placeholder body reached the wire", label)
	}
}
