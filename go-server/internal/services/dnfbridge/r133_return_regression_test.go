package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// R133 return regression tests.
//
// They are intentionally behavior tests over existing R132 owners:
//   - dispatchPartyDungeonPeerFinalRoomCompletion
//   - completeCurrentDungeonBossDieCheckLocked (op115 + bodyless op31)
//   - currentRaidCompletedMapTownReturnReady (shared clear + personal result)
//   - the real public op42/op72/op132 handlers.
//
// No reward, completion, identity or ownership gate is relaxed.  The tests only
// prove where the personal result entry is delivered and where it is lost.

type r133RegressionFailOp31Conn struct {
	bufferConn
	remaining int
	failures  int
}

func (connection *r133RegressionFailOp31Conn) Write(data []byte) (int, error) {
	if connection.remaining > 0 {
		rest := data
		for len(rest) >= dnfproto.GameServerUpperHeaderSize {
			total := int(binary.LittleEndian.Uint32(rest[3:7]))
			if total < dnfproto.GameServerUpperHeaderSize || total > len(rest) {
				break
			}
			packet, err := dnfproto.ParseChannelPacket(rest[:total])
			if err == nil && packet.Header.MsgID == currentDungeonSettlementEntryMsgID {
				connection.remaining--
				connection.failures++
				return 0, errors.New("injected member op31 write failure")
			}
			rest = rest[total:]
		}
	}
	return connection.bufferConn.Write(data)
}

func r133RegressionCountMsg(frames []upperFrame, msgID uint16) int {
	count := 0
	for _, frame := range frames {
		if frame.msgID == msgID {
			count++
		}
	}
	return count
}

func r133RegressionOnlyMsgBody(t *testing.T, frames []upperFrame, msgID uint16) []byte {
	t.Helper()
	var body []byte
	for _, frame := range frames {
		if frame.msgID != msgID {
			continue
		}
		if body != nil {
			t.Fatalf("duplicate op%d frames=%+v", msgID, frames)
		}
		body = append([]byte(nil), frame.body...)
	}
	return body
}

func r133RegressionSharedFinalRoomDeathBody(characterID uint16, objectKey uint32) []byte {
	body := make([]byte, 62)
	binary.LittleEndian.PutUint32(body[0:4], objectKey)
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(characterID))
	return body
}

// The normal shared-final-room path must deliver one personal op31 to each
// member.  op31 is only the result-entry handshake; it must not mark the reward
// or the op46/op123 barriers complete.
func TestR133RegressionPeerFinalRoomReceivesPersonalOp31(t *testing.T) {
	service, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t)
	monster := leaderRuntime.Room.Snapshot().Monsters[0]
	leaderConn := leader.conn.(*bufferConn)
	memberConn := member.conn.(*bufferConn)
	leaderConn.write.Reset()
	memberConn.write.Reset()

	body := r133RegressionSharedFinalRoomDeathBody(leader.selectedCharacterID, monster.ObjectKey)
	if err := service.handleDungeonMonsterDeath(leader, body); err != nil {
		t.Fatalf("leader final-room death: %v", err)
	}

	for _, entry := range []struct {
		name    string
		conn    *bufferConn
		runtime *runtimeDungeonState
	}{
		{name: "leader", conn: leaderConn, runtime: leaderRuntime},
		{name: "peer", conn: memberConn, runtime: memberRuntime},
	} {
		frames := splitUpperFrames(t, entry.conn.write.Bytes())
		op31 := r133RegressionOnlyMsgBody(t, frames, currentDungeonSettlementEntryMsgID)
		if op31 == nil {
			t.Fatalf("%s received no op31 result entry; frames=%+v", entry.name, frames)
		}
		if !bytes.Equal(op31, []byte{0}) {
			t.Fatalf("%s op31 body=% X want the current-EXE zero byte", entry.name, op31)
		}
		if got := r133RegressionCountMsg(frames, currentDungeonSettlementEntryMsgID); got != 1 {
			t.Fatalf("%s received %d op31 frames; want one; frames=%+v", entry.name, got, frames)
		}
		if !entry.runtime.bossDieCheckAccepted || !entry.runtime.settlementEntrySent {
			t.Fatalf("%s settlement state boss=%t entry=%t; want both true",
				entry.name, entry.runtime.bossDieCheckAccepted, entry.runtime.settlementEntrySent)
		}
		if entry.runtime.settlementPlayResultReceived || entry.runtime.settlementStatisticReceived ||
			entry.runtime.settlementClearRewardSent {
			t.Fatalf("%s op31 forged a later settlement/reward barrier: %+v", entry.name, entry.runtime)
		}
	}
}

// Red window in R132: the peer's final room and op115 commit, but the first
// bodyless op31 write fails.  The ordinary final-room owner now returns early
// because bossDieCheckAccepted is set, so no retry exists.  The expected state
// is exactly one op31 after the same dispatch owner retries.
func TestR133RegressionPeerOp31WriteFailureMustResume(t *testing.T) {
	service, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t)
	monster := leaderRuntime.Room.Snapshot().Monsters[0]
	memberConn := &r133RegressionFailOp31Conn{remaining: 1}
	member.conn = memberConn
	leader.conn.(*bufferConn).write.Reset()

	body := r133RegressionSharedFinalRoomDeathBody(leader.selectedCharacterID, monster.ObjectKey)
	if err := service.handleDungeonMonsterDeath(leader, body); err != nil {
		t.Fatalf("leader final-room death: %v", err)
	}
	if memberConn.failures != 1 {
		t.Fatalf("injected op31 failures=%d want 1", memberConn.failures)
	}
	frames := splitUpperFrames(t, memberConn.write.Bytes())
	if got := r133RegressionCountMsg(frames, currentDungeonSettlementEntryMsgID); got != 1 {
		t.Fatalf("peer received %d op31 entries after dispatch retry; want one; frames=%+v", got, frames)
	}
	if !memberRuntime.settlementEntrySent {
		t.Fatal("peer runtime did not record the retried op31")
	}
	if memberRuntime.settlementClearRewardSent || memberRuntime.settlementPlayResultReceived ||
		memberRuntime.settlementStatisticReceived {
		t.Fatal("op31 retry crossed the reward/result barriers")
	}
	if member.dungeon.runtime != memberRuntime {
		t.Fatal("op31 retry replaced the peer runtime")
	}
}

// Red window in R132: both the dispatch attempt and an immediate dispatch
// retry lose op31. A later authoritative duplicate op39 must resume the same
// settlement owner.  R132 only resumes replay final-room handling for friendly
// AI characters; an ordinary final-room monster remains stranded.
func TestR133RegressionAuthoritativeDeathReplayResumesPeerOp31(t *testing.T) {
	service, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t)
	monster := leaderRuntime.Room.Snapshot().Monsters[0]
	memberConn := &r133RegressionFailOp31Conn{remaining: 2}
	member.conn = memberConn
	leader.conn.(*bufferConn).write.Reset()

	body := r133RegressionSharedFinalRoomDeathBody(leader.selectedCharacterID, monster.ObjectKey)
	if err := service.handleDungeonMonsterDeath(leader, body); err != nil {
		t.Fatalf("leader final-room death: %v", err)
	}
	if memberConn.failures < 1 || memberConn.failures > 2 {
		t.Fatalf("injected op31 failures=%d want one or two dispatch attempts", memberConn.failures)
	}
	if memberRuntime.settlementEntrySent {
		t.Fatal("fixture unexpectedly delivered op31 before the authoritative replay")
	}
	if !memberRuntime.bossDieCheckAccepted {
		t.Fatal("fixture did not commit the peer final-room completion")
	}

	// The peer reports the same authoritative death again. The shared room is
	// already defeated, so this is the replay path, not a second kill.
	memberConn.write.Reset()
	replay := r133RegressionSharedFinalRoomDeathBody(member.selectedCharacterID, monster.ObjectKey)
	if err := service.handleDungeonMonsterDeath(member, replay); err != nil {
		t.Fatalf("peer authoritative replay: %v", err)
	}
	frames := splitUpperFrames(t, memberConn.write.Bytes())
	if got := r133RegressionCountMsg(frames, currentDungeonSettlementEntryMsgID); got != 1 {
		t.Fatalf("authoritative replay delivered %d op31 entries; want one; frames=%+v", got, frames)
	}
	if !memberRuntime.settlementEntrySent {
		t.Fatal("authoritative replay did not recover the personal result entry")
	}
	if memberRuntime.settlementClearRewardSent {
		t.Fatal("authoritative replay bypassed the reward barrier")
	}
	if member.dungeon.runtime != memberRuntime {
		t.Fatal("authoritative replay replaced the peer runtime")
	}
}

const r133RegressionRaidRunID = "r133-regression-shared-clear"

// A shared ClearedRuns row proves that the instance completed, not that this
// character consumed its personal op31/op46/op123/clear-reward handshake.
func newR133RegressionSharedClearFixture(t *testing.T) (completionReturnFixture, *runtimeRaidState) {
	t.Helper()
	fixture := newCompletionReturnPartyFixture(t)
	fixture.service.options.currentClientVersion = "official-24.9"

	for _, session := range []*gameSession{fixture.leader, fixture.member} {
		session.accountID = "account-1"
		session.accountFromToken = true
		session.loginPacketSeen = true
	}
	for _, runtime := range []*runtimeDungeonState{fixture.leaderRuntime, fixture.memberRuntime} {
		runtime.Dungeon.ID = 215
		runtime.AccountID = "account-1"
		if err := runtime.Session.CompleteCurrentRoom(); err != nil {
			t.Fatalf("complete dungeon 215 runtime: %v", err)
		}
	}

	participants := []uint16{fixture.leader.selectedCharacterID, fixture.member.selectedCharacterID}
	entry := currentRaidDungeonEntry{
		RaidKey:      uint32(fixture.leader.channel.ID)<<16 | 99,
		InstanceID:   "r133-regression-dungeon-215-instance",
		RunID:        r133RegressionRaidRunID,
		Phase:        0,
		Attempt:      1,
		ChannelID:    fixture.leader.channel.ID,
		ChannelType:  fixture.leader.channel.Type,
		Participants: append([]uint16(nil), participants...),
	}
	leaderEntry, memberEntry := entry, entry
	leaderEntry.Participants = append([]uint16(nil), participants...)
	memberEntry.Participants = append([]uint16(nil), participants...)
	fixture.leaderRuntime.raidEntry = &leaderEntry
	fixture.memberRuntime.raidEntry = &memberEntry

	battle := &runtimeRaidBattle{
		InstanceID:  entry.InstanceID,
		Mode:        1,
		ChannelID:   entry.ChannelID,
		ChannelType: entry.ChannelType,
		MemberAccounts: map[uint16]string{
			fixture.leader.selectedCharacterID: "account-1",
			fixture.member.selectedCharacterID: "account-1",
		},
		ClearedRuns: map[string]currentRaidClearedRun{
			entry.RunID: {
				DungeonID:    215,
				Phase:        entry.Phase,
				Attempt:      entry.Attempt,
				Participants: append([]uint16(nil), participants...),
			},
		},
	}
	state := &runtimeRaidState{
		RaidKey:  entry.RaidKey,
		LeaderID: fixture.leader.selectedCharacterID,
		Started:  true,
		Members: []runtimeRaidMemberState{
			{CharID: fixture.leader.selectedCharacterID},
			{CharID: fixture.member.selectedCharacterID},
		},
		Battle: battle,
	}
	fixture.service.raids = map[uint32]*runtimeRaidState{entry.RaidKey: state}

	// Leader control: every personal settlement barrier is complete.
	fixture.leaderRuntime.settlementEntrySent = true
	fixture.leaderRuntime.settlementPlayResultReceived = true
	fixture.leaderRuntime.settlementStatisticReceived = true
	fixture.leaderRuntime.settlementClearRewardSent = true
	fixture.leaderRuntime.advanceSettlementPhase(currentDungeonSettlementPhaseResultShown)

	// Member has op31/op46/op123 but has not consumed the clear reward.
	fixture.memberRuntime.settlementEntrySent = true
	fixture.memberRuntime.settlementPlayResultReceived = true
	fixture.memberRuntime.settlementStatisticReceived = true
	fixture.memberRuntime.settlementClearRewardSent = false
	fixture.memberRuntime.advanceSettlementPhase(currentDungeonSettlementPhaseResultShown)

	return fixture, state
}

func TestR133RegressionSharedClearIsNotPersonalSettlement(t *testing.T) {
	fixture, state := newR133RegressionSharedClearFixture(t)
	run, found := state.Battle.ClearedRuns[r133RegressionRaidRunID]
	if !found || !sameRaidParticipants(run.Participants, []uint16{
		fixture.leader.selectedCharacterID,
		fixture.member.selectedCharacterID,
	}) {
		t.Fatalf("shared clear run=%+v found=%t", run, found)
	}
	if !fixture.service.currentRaidCompletedMapTownReturnReady(fixture.leader, fixture.leaderRuntime) {
		t.Fatal("leader control with a complete personal result did not pass the return gate")
	}
	if fixture.service.currentRaidCompletedMapTownReturnReady(fixture.member, fixture.memberRuntime) {
		t.Fatal("shared ClearedRuns alone authorized the member's return")
	}
	if completedDungeonSettlementResultReady(fixture.memberRuntime, fixture.member.selectedCharacterID) {
		t.Fatal("member personal settlement was reported ready without the clear reward")
	}
	if fixture.memberRuntime.settlementClearRewardSent {
		t.Fatal("shared clear record mutated the member's reward barrier")
	}

	fixture.memberRuntime.settlementClearRewardSent = true
	if !fixture.service.currentRaidCompletedMapTownReturnReady(fixture.member, fixture.memberRuntime) {
		t.Fatal("member with a complete personal result still could not use the shared completed-map return")
	}
}

// Pin the three return entrances while the shared clear exists but the member's
// personal settlement is incomplete.  op42 and op72 are rejected without a
// packet; op132 writes only the minimal typed op24 and keeps the runtime.
func TestR133RegressionUnsettledMemberReturnEntrances(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Service, *gameSession) error
	}{
		{
			name: "op42_completed_exit",
			invoke: func(s *Service, session *gameSession) error {
				return s.handleDungeonGiveupGame(session, nil)
			},
		},
		{
			name: "op72_return_town",
			invoke: func(s *Service, session *gameSession) error {
				return s.handleDungeonEplpCommand(session, []byte{1, 2})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, state := newR133RegressionSharedClearFixture(t)
			fixture.leaderConn.write.Reset()
			fixture.memberConn.write.Reset()
			if len(state.Battle.ClearedRuns) != 1 {
				t.Fatalf("fixture shared clear count=%d want 1", len(state.Battle.ClearedRuns))
			}
			if err := test.invoke(fixture.service, fixture.member); err != nil {
				t.Fatal(err)
			}
			if fixture.memberRuntime.townReturnOp24Sent || fixture.memberConn.write.Len() != 0 {
				t.Fatalf("blocked return emitted op24=%t bytes=%x",
					fixture.memberRuntime.townReturnOp24Sent, fixture.memberConn.write.Bytes())
			}
			if fixture.member.dungeon.runtime != fixture.memberRuntime {
				t.Fatal("blocked return replaced the member runtime")
			}
			if fixture.memberRuntime.settlementClearRewardSent {
				t.Fatal("blocked return bypassed the reward barrier")
			}
		})
	}

	t.Run("op132_minimal_op24", func(t *testing.T) {
		fixture, _ := newR133RegressionSharedClearFixture(t)
		fixture.leaderConn.write.Reset()
		fixture.memberConn.write.Reset()
		if err := fixture.service.handleDungeonBackToVillage(fixture.member, nil); err != nil {
			t.Fatal(err)
		}
		if !fixture.memberRuntime.townReturnOp24Sent || fixture.memberConn.write.Len() == 0 {
			t.Fatal("op132 did not emit the typed town transition")
		}
		packets := splitAllCurrentGameServerUpperPackets(t, fixture.memberConn.write.Bytes())
		op24 := 0
		for _, packet := range packets {
			switch {
			case packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID:
				op24++
			case packet.Header.MsgID == currentDungeonSettlementEntryMsgID:
				t.Fatal("op132 fabricated a personal op31")
			case packet.Header.MsgID == 2:
				t.Fatal("op132 used the completed-town actor rebuild route")
			}
		}
		if op24 != 1 {
			t.Fatalf("op132 emitted %d class0 op%d frames; want one; bytes=%x",
				op24, currentSceneTransitionMsgID, fixture.memberConn.write.Bytes())
		}
		if fixture.member.dungeon.runtime != fixture.memberRuntime {
			t.Fatal("op132 detached the unsettled member runtime")
		}
		if fixture.memberRuntime.settlementClearRewardSent {
			t.Fatal("op132 bypassed the reward barrier")
		}
	})
}

// A later peer-dispatch invocation is a separate authoritative retry point.
// The current ordinary-final-room helper returns early when bossDieCheckAccepted
// is already set, so an implementation that retries only in the first call's
// error branch cannot recover here. The expected result is one recovered op31
// from the same settlement owner, with the reward/result barriers untouched.
func TestR133RegressionSecondPeerDispatchResumesLostOp31(t *testing.T) {
	service, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t)
	monster := leaderRuntime.Room.Snapshot().Monsters[0]
	memberConn := &r133RegressionFailOp31Conn{remaining: 2}
	member.conn = memberConn
	leader.conn.(*bufferConn).write.Reset()

	body := r133RegressionSharedFinalRoomDeathBody(leader.selectedCharacterID, monster.ObjectKey)
	if err := service.handleDungeonMonsterDeath(leader, body); err != nil {
		t.Fatalf("leader final-room death: %v", err)
	}
	if memberConn.failures < 1 || memberConn.failures > 2 {
		t.Fatalf("injected op31 failures=%d want one or two first-dispatch attempts", memberConn.failures)
	}
	if memberRuntime.settlementEntrySent {
		t.Fatal("fixture unexpectedly delivered op31 before the second dispatch")
	}
	if !memberRuntime.bossDieCheckAccepted {
		t.Fatal("fixture did not commit the peer final-room completion")
	}
	if memberRuntime.partyInstance == nil {
		t.Fatal("fixture lost the shared party instance")
	}

	memberConn.write.Reset()
	service.dispatchPartyDungeonPeerFinalRoomCompletion(
		member,
		memberRuntime.partyInstance.id,
		monster.ObjectKey,
	)
	frames := splitUpperFrames(t, memberConn.write.Bytes())
	if got := r133RegressionCountMsg(frames, currentDungeonSettlementEntryMsgID); got != 1 {
		t.Fatalf("second peer dispatch delivered %d op31 entries; want one; frames=%+v", got, frames)
	}
	if !memberRuntime.settlementEntrySent {
		t.Fatal("second peer dispatch did not record the recovered op31")
	}
	if memberRuntime.settlementClearRewardSent || memberRuntime.settlementPlayResultReceived ||
		memberRuntime.settlementStatisticReceived {
		t.Fatal("second peer dispatch crossed the reward/result barriers")
	}
	if member.dungeon.runtime != memberRuntime {
		t.Fatal("second peer dispatch replaced the peer runtime")
	}
}
