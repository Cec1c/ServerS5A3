package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
	"testing"
	"time"
)

type raidCloseAfterExitAckConn struct {
	*bufferConn
	after func()
}

func (c *raidCloseAfterExitAckConn) Write(p []byte) (int, error) {
	n, e := c.bufferConn.Write(p)
	if e == nil && c.after != nil {
		f := c.after
		c.after = nil
		f()
	}
	return n, e
}
func TestReviewRaidRetryMapClosesAfterAckKeepsOldReturnOwner(t *testing.T) {
	s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	completeSettlementExitBarriersForTest(t, s, r, session)
	s.options.currentClientVersion = "official-24.9"
	repos, _ := s.repositoryGroup()
	character, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	session.accountID = character.AccountID
	session.loginPacketSeen, session.accountFromToken = true, true
	s.bindGameSessionCharacter(session, 99)
	session.channel = channelcatalog.Channel{ID: 23, Type: 23}
	s.bindGameSessionCharacter(session, 99)
	r.AccountID = character.AccountID
	e := &currentRaidDungeonEntry{RaidKey: 23<<16 | 99, InstanceID: "review-closed-after-ack", RunID: "owned-run", Attempt: 1, ChannelID: 23, ChannelType: 23, Participants: []uint16{99}}
	r.raidEntry = e
	b := &runtimeRaidBattle{InstanceID: e.InstanceID, Mode: 1, ChannelID: 23, ChannelType: 23, Deadline: time.Now().Add(time.Hour), Dungeons: []raid.DungeonState{{ID: uint32(r.Dungeon.ID), State: 0}}, MemberAccounts: map[uint16]string{99: character.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{e.RunID: {DungeonID: uint32(r.Dungeon.ID), Attempt: 1, Participants: []uint16{99}}}}
	s.raids = map[uint32]*runtimeRaidState{e.RaidKey: {RaidKey: e.RaidKey, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99}}}}
	if !s.currentRaidDungeonSelectionAllowed(session, uint32(r.Dungeon.ID)) {
		t.Fatal("pre-ACK map should be open")
	}
	conn.write.Reset()
	session.conn = &raidCloseAfterExitAckConn{bufferConn: conn, after: func() { b.Dungeons[0].State = 1; b.PhaseCompleted = true }}
	if err := s.handleDungeonEplpCommand(session, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != r {
		t.Fatalf("map closed after ACK retired old return owner: runtime=%p old=%p ACK=%v", session.dungeon.runtime, r, r.settlementCardExitAckSent)
	}
}

func TestReviewRaidRetryRejectedNewOp16RestoresOldReturnOwner(t *testing.T) {
	s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	completeSettlementExitBarriersForTest(t, s, r, session)
	s.options.currentClientVersion = "official-24.9"
	repos, _ := s.repositoryGroup()
	character, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	session.accountID = character.AccountID
	session.loginPacketSeen, session.accountFromToken = true, true
	s.bindGameSessionCharacter(session, 99)
	session.channel = channelcatalog.Channel{ID: 23, Type: 23}
	s.bindGameSessionCharacter(session, 99)
	r.AccountID = character.AccountID
	r.Dungeon.ID = 999999
	session.preDungeonContextPlayerStateSent = true
	e := &currentRaidDungeonEntry{RaidKey: 23<<16 | 99, InstanceID: "review-closed-after-ack", RunID: "owned-run", Attempt: 1, ChannelID: 23, ChannelType: 23, Participants: []uint16{99}}
	r.raidEntry = e
	b := &runtimeRaidBattle{InstanceID: e.InstanceID, Mode: 1, ChannelID: 23, ChannelType: 23, Deadline: time.Now().Add(time.Hour), Dungeons: []raid.DungeonState{{ID: uint32(r.Dungeon.ID), State: 0}}, MemberAccounts: map[uint16]string{99: character.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{e.RunID: {DungeonID: uint32(r.Dungeon.ID), Attempt: 1, Participants: []uint16{99}}}}
	s.raids = map[uint32]*runtimeRaidState{e.RaidKey: {RaidKey: e.RaidKey, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99}}}}
	if !s.currentRaidDungeonSelectionAllowed(session, uint32(r.Dungeon.ID)) {
		t.Fatal("pre-ACK map should be open")
	}
	conn.write.Reset()
	session.conn = &raidCloseAfterExitAckConn{bufferConn: conn, after: nil}
	if err := s.handleDungeonEplpCommand(session, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	stream := conn.write.Bytes()
	rejected := false
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID == 16 && len(packet.Body) > 0 && packet.Body[0] == 0 {
			rejected = true
		}
		stream = rest
	}
	if !rejected {
		t.Fatal("new op16 refusal not exercised")
	}
	if session.dungeon.runtime != r || !session.preDungeonContextPlayerStateSent {
		t.Fatalf("map closed after ACK retired old return owner: runtime=%p old=%p ACK=%v", session.dungeon.runtime, r, r.settlementCardExitAckSent)
	}
}
