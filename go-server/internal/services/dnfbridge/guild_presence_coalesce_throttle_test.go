package dnfbridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func countGuildOpcodePackets(t *testing.T, wire []byte, opcode uint16) int {
	t.Helper()
	count := 0
	for len(wire) > 0 {
		pkt, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if pkt.Header.MsgID == opcode {
			count++
		}
	}
	return count
}

func TestGuildMemberListRequestedExpiresAfterWindowAndReopensOnClientRequest(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s.guildRosterNowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	s.guildMemberListActiveWindow = 60 * time.Second
	s.guildPresenceCoalesceDelay = -1 // 聚焦验证活跃窗口与自动清理，不等待防抖

	peer := peers[0]
	conn := peer.conn.(*bufferConn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	// 1. 玩家主动打开公会窗口（发 op140），点亮 memberListRequested 及时间戳。
	if err := s.callGameSession(ctx, peer, "open-guild-window", func() error {
		conn.write.Reset()
		guildDispatch(t, s, peer, uint16(dnfenum.CmdPacketGuildAllMemberList), nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if countGuildOpcodePackets(t, conn.write.Bytes(), uint16(dnfenum.CmdPacketGuildAllMemberList)) == 0 {
		t.Fatal("initial client op140 did not return member list")
	}

	// 2. 活跃窗口期内（30s），presence 刷新正常推送全量成员列表，且被动刷新不续期时间戳。
	mu.Lock()
	now = now.Add(30 * time.Second)
	mu.Unlock()
	conn.write.Reset()
	bound, _ := s.boundGameSessionCharacterSnapshot(peer)
	s.requestCurrentGuildPresenceRefresh(bound, 1)
	if err := s.callGameSession(ctx, peer, "drain-active-presence", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if countGuildOpcodePackets(t, conn.write.Bytes(), uint16(dnfenum.CmdPacketGuildAllMemberList)) == 0 {
		t.Fatal("presence refresh within active window did not send member list")
	}

	// 3. 超过 60s 活跃窗口后（再过 35s，累计 65s > 60s），自动清理 memberListRequested：
	//    presence 刷新不再推送全量 op140（只发轻量管理摘要），membership 刷新直接跳过。
	mu.Lock()
	now = now.Add(35 * time.Second)
	mu.Unlock()
	conn.write.Reset()
	s.requestCurrentGuildPresenceRefresh(bound, 1)
	if err := s.callGameSession(ctx, peer, "drain-expired-presence", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := countGuildOpcodePackets(t, conn.write.Bytes(), uint16(dnfenum.CmdPacketGuildAllMemberList)); got != 0 {
		t.Fatalf("expired presence refresh unexpectedly sent %d full op140 packets", got)
	}
	peer.guild.mu.Lock()
	stillRequested := peer.guild.memberListRequested
	peer.guild.mu.Unlock()
	if stillRequested {
		t.Fatal("memberListRequested was not cleared after active window expired")
	}

	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	s.broadcastCurrentGuildMembers(reg.Guilds[1], 2)
	if err := s.callGameSession(ctx, peer, "drain-expired-membership", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("expired membership broadcast still wrote %d bytes", conn.write.Len())
	}

	// 4. 玩家再次主动打开公会窗（op140），立即恢复活跃推送。
	if err := s.callGameSession(ctx, peer, "reopen-guild-window", func() error {
		conn.write.Reset()
		guildDispatch(t, s, peer, uint16(dnfenum.CmdPacketGuildAllMemberList), nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	s.broadcastCurrentGuildMembers(reg.Guilds[1], 2)
	if err := s.callGameSession(ctx, peer, "drain-reopened-membership", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if countGuildOpcodePackets(t, conn.write.Bytes(), uint16(dnfenum.CmdPacketGuildAllMemberList)) == 0 {
		t.Fatal("reopened guild window did not resume membership refresh")
	}
}

func TestGuildMembershipRefreshCoalescesBurstWithinWindow(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	s.guildPresenceCoalesceDelay = 120 * time.Millisecond

	peer := peers[0]
	peer.guild.memberListRequested = true
	conn := peer.conn.(*bufferConn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	reg, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]

	// 首次 membership 变更立即执行；紧随其后的连续 8 次变更合并为防抖窗口结束后的 1 次刷新。
	conn.write.Reset()
	s.broadcastCurrentGuildMembers(g, 2)
	if err := s.callGameSession(ctx, peer, "drain-first-membership", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	firstCount := countGuildOpcodePackets(t, conn.write.Bytes(), uint16(dnfenum.CmdPacketGuildAllMemberList))
	if firstCount != 1 {
		t.Fatalf("first membership refresh sent %d op140 packets, want 1", firstCount)
	}

	conn.write.Reset()
	for i := 0; i < 8; i++ {
		s.broadcastCurrentGuildMembers(g, 2)
	}
	// 防抖窗口内不应立刻连续发送多次全量列表。
	var immediateSnap []byte
	if err := s.callGameSession(ctx, peer, "check-within-window", func() error {
		immediateSnap = append([]byte(nil), conn.write.Bytes()...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if immediate := countGuildOpcodePackets(t, immediateSnap, uint16(dnfenum.CmdPacketGuildAllMemberList)); immediate != 0 {
		t.Fatalf("burst within coalesce window sent %d immediate op140 packets, want 0", immediate)
	}

	// 等待防抖窗口到期后，8 次突发变更仅合并为 1 次刷新。
	var coalesced int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var snapshot []byte
		if err := s.callGameSession(ctx, peer, "poll-coalesced", func() error {
			snapshot = append([]byte(nil), conn.write.Bytes()...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if coalesced = countGuildOpcodePackets(t, snapshot, uint16(dnfenum.CmdPacketGuildAllMemberList)); coalesced > 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if coalesced != 1 {
		t.Fatalf("8 burst membership changes sent %d op140 packets after coalesce window, want 1", coalesced)
	}
}
