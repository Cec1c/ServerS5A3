package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

// 90CN 2026-11-18 23:47～23:51：队长退队 ⇒ 幸存者换新 PartyID 重建 ⇒ 旧实现 CloseParty
// 后从 portStart 重新分配，幸存者之间的端口整体洗牌；客户端继续往旧端口发 ⇒ 串线。
// 这里用现场的四个角色号与入队顺序（1 建队，125、57、54 依次加入，1 退队）复现。
func leaderTransferBindings() map[uint16]currentPartyUDPRelayBinding {
	out := map[uint16]currentPartyUDPRelayBinding{}
	for _, id := range []uint16{1, 54, 57, 125} {
		out[id] = currentPartyUDPRelayBinding{characterID: id, generation: 1, address: [4]byte{127, 0, 0, 1}}
	}
	return out
}

func leaderTransferRoster(b map[uint16]currentPartyUDPRelayBinding, ids ...uint16) []currentPartyUDPRelayBinding {
	out := make([]currentPartyUDPRelayBinding, 0, len(ids))
	for _, id := range ids {
		out = append(out, b[id])
	}
	return out
}

func leaderTransferPorts(t *testing.T, relay *currentPartyUDPRelay, partyID uint16, ids ...uint16) map[[2]uint16]uint16 {
	t.Helper()
	out := map[[2]uint16]uint16{}
	for _, owner := range ids {
		for _, peer := range ids {
			if owner == peer {
				continue
			}
			_, port, ok := relay.Endpoint(partyID, owner, peer)
			if !ok {
				t.Fatalf("party %d missing leg %d->%d", partyID, owner, peer)
			}
			out[[2]uint16{owner, peer}] = port
		}
	}
	return out
}

func newLeaderTransferRelay(t *testing.T) *currentPartyUDPRelay {
	t.Helper()
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	return relay
}

// 现场形状：1 建队，125、57、54 依次加入 ⇒ 1 退队，队长转给 125，新 ID ⇒ 1 再被邀请回来。
func formLeaderTransferParty(t *testing.T, relay *currentPartyUDPRelay, b map[uint16]currentPartyUDPRelayBinding, partyID uint16) {
	t.Helper()
	for _, roster := range [][]uint16{{1, 125}, {1, 125, 57}, {1, 125, 57, 54}} {
		if !relay.Sync(partyID, leaderTransferRoster(b, roster...)) {
			t.Fatalf("formation %v", roster)
		}
	}
}

func TestPartyUDPRelayLeaderTransferKeepsSurvivorPorts(t *testing.T) {
	relay := newLeaderTransferRelay(t)
	b := leaderTransferBindings()
	formLeaderTransferParty(t, relay, b, 4)
	before := leaderTransferPorts(t, relay, 4, 54, 57, 125)

	relay.TransferParty(4, 5)
	if _, _, ok := relay.Endpoint(4, 54, 57); ok {
		t.Fatal("retired party id is still routable")
	}
	if !relay.Sync(5, leaderTransferRoster(b, 54, 57, 125)) {
		t.Fatal("survivor sync")
	}
	after := leaderTransferPorts(t, relay, 5, 54, 57, 125)
	for pair, port := range before {
		if after[pair] != port {
			t.Fatalf("survivor pair %v moved %d -> %d; clients keep sending to the old port", pair, port, after[pair])
		}
	}

	// 原队长被重新邀请回来：幸存者之间的端口仍然不动，新成员各腿可路由且不与幸存者撞口。
	if !relay.Sync(5, leaderTransferRoster(b, 54, 57, 125, 1)) {
		t.Fatal("rejoin sync")
	}
	rejoined := leaderTransferPorts(t, relay, 5, 1, 54, 57, 125)
	seen := map[uint16][2]uint16{}
	for pair, port := range rejoined {
		if old, ok := before[pair]; ok && old != port {
			t.Fatalf("rejoin moved survivor pair %v %d -> %d", pair, old, port)
		}
		if other, dup := seen[port]; dup {
			t.Fatalf("port %d shared by %v and %v", port, other, pair)
		}
		seen[port] = pair
	}
}

// 对照：同一个形状走旧的 CloseParty，幸存者端口确实会被洗牌（保证上面的测试能抓到回退）。
func TestPartyUDPRelayCloseOnLeaderTransferShufflesSurvivorPorts(t *testing.T) {
	relay := newLeaderTransferRelay(t)
	b := leaderTransferBindings()
	formLeaderTransferParty(t, relay, b, 4)
	before := leaderTransferPorts(t, relay, 4, 54, 57, 125)
	relay.CloseParty(4)
	if !relay.Sync(5, leaderTransferRoster(b, 54, 57, 125)) {
		t.Fatal("survivor sync")
	}
	after := leaderTransferPorts(t, relay, 5, 54, 57, 125)
	moved := 0
	for pair, port := range before {
		if after[pair] != port {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("fixture no longer reproduces the live port shuffle")
	}
}

// 服务层：真实的 RuntimePartyManager 队长退队产出 Retired，transferRuntimePartyUDPRelay
// 必须把幸存者端口带到新 ID；新 ID 无效时退回旧的关闭语义。
func TestTransferRuntimePartyUDPRelayFollowsManagerLeaderLeave(t *testing.T) {
	relay := newLeaderTransferRelay(t)
	s := &Service{}
	s.partyUDPRelay = relay
	manager := s.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{UserID: 1, SessionGeneration: 1}, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatalf("create: %s", created.Reason)
	}
	for _, id := range []uint16{125, 57, 54} {
		if joined := manager.Join(created.Party.ID, party.RuntimePartyMember{UserID: id, SessionGeneration: 1}); !joined.OK {
			t.Fatalf("join %d: %s", id, joined.Reason)
		}
	}
	oldID := created.Party.ID
	b := leaderTransferBindings()
	formLeaderTransferParty(t, relay, b, oldID)
	before := leaderTransferPorts(t, relay, oldID, 54, 57, 125)

	left := manager.Leave(1, 1)
	if !left.OK || !left.LeaderMoved || left.Retired == nil || left.Party.ID == oldID {
		t.Fatalf("leader leave did not rebuild under a new id: %+v", left)
	}
	s.transferRuntimePartyUDPRelay(int(left.Retired.ID), int(left.Party.ID))
	if !relay.Sync(left.Party.ID, leaderTransferRoster(b, 54, 57, 125)) {
		t.Fatal("survivor sync")
	}
	after := leaderTransferPorts(t, relay, left.Party.ID, 54, 57, 125)
	for pair, port := range before {
		if after[pair] != port {
			t.Fatalf("pair %v moved %d -> %d", pair, port, after[pair])
		}
	}

	// 无效的新 ID ⇒ 与旧行为一致：直接关闭（manager 里不存在的队伍）。
	if !relay.Sync(77, leaderTransferRoster(b, 1, 54)) {
		t.Fatal("orphan sync")
	}
	s.transferRuntimePartyUDPRelay(77, 0)
	if _, _, ok := relay.Endpoint(77, 1, 54); ok {
		t.Fatal("fallback close left the party routable")
	}
	// 新 ID 仍是活队伍时旧 ID 守卫不得误伤它。
	s.transferRuntimePartyUDPRelay(int(left.Party.ID), 0)
	if _, _, ok := relay.Endpoint(left.Party.ID, 54, 57); !ok {
		t.Fatal("guard tore down a live party")
	}
}
