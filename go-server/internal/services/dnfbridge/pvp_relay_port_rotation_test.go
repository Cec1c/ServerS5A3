package dnfbridge

import (
	"testing"
	"time"
)

// TestPVPRelayRotatesPortBetweenConsecutiveRooms 锁定「连续两局决斗不能拿到同一个中继端口」。
//
// 现场（2026-10-31 90CN）：决斗结束约 20 秒内再匹配，客户端黑屏约 12 秒后主动退座回广场；
// 隔 2 分钟以上再匹配则正常。服务端下发的报文逐字节一致、中继两局都在正常转发，
// 唯一跨局完全不变的量是**通告给客户端的对端端点**：端口池"取最小空闲"，而自动匹配的
// 房间号恒为 1、两名角色也恒为同一对，所以连续两局都落到 30001/30002。
// 客户端看到的 (对端 IP, 对端端口) 一字不差 ⇒ 它的 UDP 会话四元组跨局不变。
//
// 修复前：三局的腿端口完全相同 → 本用例直接报红。
func TestPVPRelayRotatesPortBetweenConsecutiveRooms(t *testing.T) {
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", r137FreeRange(t), 64, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pair := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 102, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	legPort := func() uint16 {
		r.mu.Lock()
		defer r.mu.Unlock()
		leg := r.legs[currentPartyUDPRelayLegKey{pvp: true, partyID: 1, ownerID: 101, peerID: 102}]
		if leg == nil {
			t.Fatal("决斗腿不存在")
		}
		return leg.port
	}

	// 自动匹配的房间号恒为 1、角色对也恒定 —— 复现现场就是「同 key 反复建/拆」。
	used := map[uint16]bool{}
	for round := 1; round <= 4; round++ {
		if !r.syncGroup(true, 1, pair) {
			t.Fatalf("第 %d 局中继不完整", round)
		}
		port := legPort()
		if used[port] {
			t.Fatalf("第 %d 局复用了端口 %d：客户端 UDP 四元组跨局不变，第二局会黑屏", round, port)
		}
		used[port] = true
		r.closePVPRoom(1)
	}
}

// TestPVPRelayPortQuarantineNeverBlocksAllocation 锁定「静默期不能把端口池抽干就开不了局」。
//
// 静默期只是**优先避开**：端口全在静默期时 newLegLocked 必须忽略静默期再扫一遍，
// 否则一次端口紧张就会让所有决斗都建不起来（比黑屏更糟）。
func TestPVPRelayPortQuarantineNeverBlocksAllocation(t *testing.T) {
	start := r137FreeRange(t)
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", start, 64, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	r.mu.Lock()
	for offset := 0; offset < 64; offset++ {
		r.pvpReleasedAt[start+offset] = time.Now()
	}
	r.mu.Unlock()

	pair := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 102, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !r.syncGroup(true, 1, pair) {
		t.Fatal("端口全部处于静默期时仍然必须能建出决斗中继")
	}
}
