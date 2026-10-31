package dnfbridge

import (
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// This file pins the v158 town HUD repair at the wire boundary. The native
// class0/op153 handler resolves each row through the scene actor lookup and
// skips rows whose actor is absent. The refresh therefore has to follow the
// remote actor's mode0/mode1 projection, and the existing final roster must
// remain a separate, later op153 -> op9 pair.

func requireTownPartyActorHPFrameIDs(t *testing.T, frames []upperFrame, want ...uint16) {
	t.Helper()
	got := townPartyActorHPFrameIDs(frames)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("town actor HP frame ids=%v want=%v frames=%+v", got, want, frames)
	}
}

func requireTownPartyActorHPNoLinkPackets(t *testing.T, frames []upperFrame) {
	t.Helper()
	for _, frame := range frames {
		if frame.msgID == 0x000b {
			t.Fatalf("town actor HP refresh restarted op11: frames=%+v", frames)
		}
	}
}

func requireTownPartyActorHPFrame(t *testing.T, frame upperFrame, want ...realtimeInfoRow) {
	t.Helper()
	if frame.msgID != 0x0099 {
		t.Fatalf("town actor HP frame msg=%d want op153 body=% X", frame.msgID, frame.body)
	}
	rows := parseRealtimeInfoRows(t, frame.body)
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("town actor HP rows=%+v want=%+v body=% X", rows, want, frame.body)
	}
}

func townPartyActorHPRows29And17() []realtimeInfoRow {
	return []realtimeInfoRow{
		{uid: 29, hp: 100, helper: 0, slot: 0},
		{uid: 17, hp: 100, helper: 0, slot: 1},
	}
}

func townPartyActorHPRows1001And1002() []realtimeInfoRow {
	return []realtimeInfoRow{
		{uid: 1001, hp: 100, helper: 0, slot: 0},
		{uid: 1002, hp: 100, helper: 0, slot: 1},
	}
}

func requireTownPartyActorModes(t *testing.T, frames []upperFrame, actorID uint16) {
	t.Helper()
	if len(frames) < 2 {
		t.Fatalf("town actor mode frames=%d want mode0/mode1", len(frames))
	}
	mode0, mode1 := frames[0], frames[1]
	key := currentSceneActorObjectKey(actorID)
	if mode0.msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || len(mode0.body) < 0x4e ||
		mode0.body[0] != 0 || mode0.body[3] != currentSceneObjectRoute ||
		binary.LittleEndian.Uint16(mode0.body[0x4c:0x4e]) != key {
		t.Fatalf("town actor mode0 header body=% X want actor=%d key=%d", mode0.body, actorID, key)
	}
	if mode1.msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || len(mode1.body) < currentMode1BaseWireSize ||
		mode1.body[0] != 1 || mode1.body[3] != currentSceneObjectRoute ||
		binary.LittleEndian.Uint16(mode1.body[0x15:0x17]) != key {
		t.Fatalf("town actor mode1 header body=% X want actor=%d key=%d", mode1.body, actorID, key)
	}
}

func requireTownPartyActorOp9(t *testing.T, frame upperFrame) {
	t.Helper()
	if frame.msgID != uint16(dnfenum.CmdPacketRecoverStamina) || len(frame.body) <= 9 ||
		frame.body[9] != currentSceneOp9ActorDisplayKind {
		t.Fatalf("town actor op9 body=% X want class0 kind0", frame.body)
	}
}

// TestTownPartyActorHPFollowsActorProjection exercises the real
// sendTownRemoteActorState path. With the official same-party op9 skip active,
// the mid refresh is exactly mode0 -> mode1 -> op153 and sends no op11. The
// existing roster helper still appends a separate op153 -> op9 pair.
func TestTownPartyActorHPFollowsActorProjection(t *testing.T) {
	t.Run("official default skips peer op9", func(t *testing.T) {
		t.Setenv(townCopresenceSkipSamePartyFrameEnv, "")
		fixture := newTownPartyActorHPFixture(t, true)
		fixture.reset()

		if err := fixture.service.sendTownRemoteActorState(fixture.target, fixture.actorInfo, "test_town_actor_hp"); err != nil {
			t.Fatalf("send town remote actor: %v", err)
		}
		frames := splitUpperFrames(t, fixture.targetConn.write.Bytes())
		requireTownPartyActorHPFrameIDs(t, frames, 0x0002, 0x0002, 0x0099)
		requireTownPartyActorModes(t, frames, 17)
		requireTownPartyActorHPFrame(t, frames[2], townPartyActorHPRows29And17()...)
		requireTownMode0InitialHP(t, frames[0].body, 100)
		requireTownPartyActorHPNoLinkPackets(t, frames)

		// The pre-existing final roster is still emitted and still owns its
		// own complete realtime table followed by the scene party record.
		state := fixture.service.runtimePartyStateForSession(fixture.target)
		if err := fixture.service.sendRuntimePartyRosterLocal(fixture.target, state, "test_town_actor_hp_final_roster"); err != nil {
			t.Fatalf("send final town party roster: %v", err)
		}
		frames = splitUpperFrames(t, fixture.targetConn.write.Bytes())
		requireTownPartyActorHPFrameIDs(t, frames, 0x0002, 0x0002, 0x0099, 0x0099, 0x0009)
		requireTownPartyActorHPFrame(t, frames[2], townPartyActorHPRows29And17()...)
		requireTownPartyActorHPFrame(t, frames[3], townPartyActorHPRows29And17()...)
		requireTownPartyActorHPNoLinkPackets(t, frames)
	})

	t.Run("peer op9 stays after the HP refresh", func(t *testing.T) {
		t.Setenv(townCopresenceSkipSamePartyFrameEnv, "off")
		fixture := newTownPartyActorHPFixture(t, true)
		fixture.reset()

		if err := fixture.service.sendTownRemoteActorState(fixture.target, fixture.actorInfo, "test_town_actor_hp_with_op9"); err != nil {
			t.Fatalf("send town remote actor: %v", err)
		}
		frames := splitUpperFrames(t, fixture.targetConn.write.Bytes())
		requireTownPartyActorHPFrameIDs(t, frames, 0x0002, 0x0002, 0x0099, 0x0009)
		requireTownPartyActorModes(t, frames, 17)
		requireTownPartyActorHPFrame(t, frames[2], townPartyActorHPRows29And17()...)
		requireTownPartyActorOp9(t, frames[3])
		requireTownPartyActorHPNoLinkPackets(t, frames)
	})
}

// TestTownPartyActorHPUsesManagerNotCachedRoster pins the authorization
// source: the session cache alone is not enough; an authoritative manager
// party containing both characters is required.
func TestTownPartyActorHPUsesManagerNotCachedRoster(t *testing.T) {
	fixture := newTownPartyActorHPFixture(t, true)
	storeRuntimePartyState(fixture.target, alignedcmd.PartyState{})
	fixture.reset()

	if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
		t.Fatalf("refresh HP from manager: %v", err)
	}
	frames := splitUpperFrames(t, fixture.targetConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, frames, 0x0099)
	requireTownPartyActorHPFrame(t, frames[0], townPartyActorHPRows29And17()...)
	if fixture.actorConn.write.Len() != 0 {
		t.Fatalf("HP refresh wrote to the actor connection: %x", fixture.actorConn.write.Bytes())
	}
}

func TestTownPartyActorHPPreformationDoesNotPublishRealtime(t *testing.T) {
	fixture := newTownPartyActorHPFixture(t, true)
	if err := fixture.service.sendTownRemoteActorStateWithPartyFrame(fixture.target, fixture.actorInfo, "test_preformation", false); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, fixture.targetConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, frames, 2, 2)
	requireTownPartyActorModes(t, frames, 17)
}

type townPartyHPFailConn struct {
	bufferConn
	writes  int
	partial int
}

func (c *townPartyHPFailConn) Write(b []byte) (int, error) {
	c.writes++
	if c.writes == 3 {
		if c.partial != 0 {
			_, _ = c.bufferConn.Write(b[:c.partial])
		}
		return c.partial, io.ErrClosedPipe
	}
	return c.bufferConn.Write(b)
}

func TestTownPartyActorHPWriteFailureStopsSubsequentFrames(t *testing.T) {
	for _, partial := range []int{0, 5} {
		t.Run(string(rune('0'+partial)), func(t *testing.T) {
			fixture := newTownPartyActorHPFixture(t, true)
			t.Setenv(townCopresenceSkipSamePartyFrameEnv, "off")
			conn := &townPartyHPFailConn{partial: partial}
			fixture.target.conn = conn
			err := fixture.service.sendTownRemoteActorState(fixture.target, fixture.actorInfo, "test_town_hp_write_failure")
			if !errors.Is(err, io.ErrClosedPipe) || conn.writes != 3 {
				t.Fatalf("HP write error=%v attempts=%d, want ErrClosedPipe and no later op9", err, conn.writes)
			}
			wire := conn.write.Bytes()
			frames := splitUpperFrames(t, wire[:len(wire)-partial])
			requireTownPartyActorHPFrameIDs(t, frames, 2, 2)
			requireTownPartyActorModes(t, frames, 17)
		})
	}
}

// TestTownPartyActorHPGateControls keeps the official-only, same-channel,
// same-party, non-dungeon, non-FiendWar and current-binding
// gates executable without replacing the real fixtures with hand-built mocks.
func TestTownPartyActorHPGateControls(t *testing.T) {
	t.Run("legacy profile", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		fixture.service.options.currentClientVersion = "90cn-decode-bypass-v1"
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
			t.Fatalf("legacy HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 || fixture.actorConn.write.Len() != 0 {
			t.Fatalf("legacy profile emitted packets target=%x actor=%x",
				fixture.targetConn.write.Bytes(), fixture.actorConn.write.Bytes())
		}
	})

	t.Run("active dungeon on target", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		fixture.target.dungeon.mu.Lock()
		fixture.target.dungeon.runtime = &runtimeDungeonState{Session: &worldmap.DungeonSession{}}
		fixture.target.dungeon.mu.Unlock()
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
			t.Fatalf("dungeon HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 {
			t.Fatalf("active dungeon emitted a town HP table: %x", fixture.targetConn.write.Bytes())
		}
	})

	t.Run("active dungeon on actor", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		fixture.actor.dungeon.mu.Lock()
		fixture.actor.dungeon.runtime = &runtimeDungeonState{Session: &worldmap.DungeonSession{}}
		fixture.actor.dungeon.mu.Unlock()
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
			t.Fatalf("dungeon actor HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 {
			t.Fatalf("dungeon actor emitted a town HP table: %x", fixture.targetConn.write.Bytes())
		}
	})

	t.Run("fiendwar target channel", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		fixture.target.channel.Type = 42
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
			t.Fatalf("fiendwar HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 {
			t.Fatalf("FiendWar emitted a town HP table: %x", fixture.targetConn.write.Bytes())
		}
	})

	t.Run("different party", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		outsiderConn := &bufferConn{}
		outsider := &gameSession{
			conn:                            outsiderConn,
			connID:                          "town-party-actor-hp-outsider",
			channel:                         fixture.target.channel,
			residentChannel:                 fixture.target.residentChannel,
			selectedCharacterID:             18,
			townSceneReadyCharacterID:       18,
			townActorOwnerChannel:           byte(fixture.target.channel.ID),
			connectionTownActorOwnerChannel: byte(fixture.target.channel.ID),
		}
		fixture.service.bindGameSessionCharacter(outsider, 18)
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, outsider); err != nil {
			t.Fatalf("outsider HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 || outsiderConn.write.Len() != 0 {
			t.Fatalf("different party emitted packets target=%x outsider=%x",
				fixture.targetConn.write.Bytes(), outsiderConn.write.Bytes())
		}
	})

	t.Run("different channel", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		fixture.actor.channel.ID = fixture.target.channel.ID + 1
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
			t.Fatalf("different-channel HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 {
			t.Fatalf("different channel emitted a town HP table: %x", fixture.targetConn.write.Bytes())
		}
	})

	t.Run("stale target binding", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, true)
		replacement := &gameSession{
			conn:                            &bufferConn{},
			connID:                          "town-party-actor-hp-replacement",
			channel:                         fixture.target.channel,
			residentChannel:                 fixture.target.residentChannel,
			selectedCharacterID:             29,
			townSceneReadyCharacterID:       29,
			townActorOwnerChannel:           byte(fixture.target.channel.ID),
			connectionTownActorOwnerChannel: byte(fixture.target.channel.ID),
		}
		fixture.service.bindGameSessionCharacter(replacement, 29)
		fixture.service.promoteResidentGameSession(replacement, 29)
		if _, current := fixture.service.boundGameSessionCharacterSnapshot(fixture.target); current {
			t.Fatal("replacement fixture did not retire the previous bound session")
		}
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
			t.Fatalf("stale-binding HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 {
			t.Fatalf("stale binding emitted a town HP table: %x", fixture.targetConn.write.Bytes())
		}
	})

	t.Run("cache only without manager party", func(t *testing.T) {
		fixture := newTownPartyActorHPFixture(t, false)
		fixture.reset()
		if err := fixture.service.sendCurrentTownPartyHPAfterActor(fixture.target, fixture.actor); err != nil {
			t.Fatalf("cache-only HP refresh: %v", err)
		}
		if fixture.targetConn.write.Len() != 0 || fixture.actorConn.write.Len() != 0 {
			t.Fatalf("cache-only roster fell back to a packet target=%x actor=%x",
				fixture.targetConn.write.Bytes(), fixture.actorConn.write.Bytes())
		}
	})
}

// TestTownPartyReturnRebindHPFollowsActorProjection reuses the two-player
// return fixture and the real dungeon-return rebind path. Both receivers get
// the actor projection, then the mid HP table, then op23, and finally the
// unchanged local roster pair.
func TestTownPartyReturnRebindHPFollowsActorProjection(t *testing.T) {
	_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	service.options.currentClientVersion = "official-24.9"
	t.Setenv(townCopresenceSkipSamePartyFrameEnv, "off")
	leaderInfo, ok := service.onlinePlayers.PlayerForCharacter(leader.selectedCharacterID)
	if !ok {
		t.Fatal("leader presence missing before return")
	}
	startTownPeerTest(t, service, leader, &leaderInfo)
	memberInfo, ok := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	if !ok {
		t.Fatal("member presence missing before return")
	}
	startTownPeerTest(t, service, member, &memberInfo)

	if ok, partyID, reason, members := service.forceManagedRuntimeParty(1001, 1002); !ok {
		t.Fatalf("force managed return party: party=%d members=%d reason=%s", partyID, members, reason)
	}
	service.onlinePlayers.LeaveArea(1001)
	leaderConn.write.Reset()
	memberConn.write.Reset()

	service.rebindTownCopresenceAfterDungeonReturn(leader, 38, 2, "test_town_party_actor_hp_return")
	drainTownPeerTest(t, service, leader)
	drainTownPeerTest(t, service, member)

	readFrames := func(wire []byte) []upperFrame {
		var frames []upperFrame
		for len(wire) > 0 {
			packet, rest := splitGameServerUpperPacketWithHeader(t, wire, service.gameUpperHeaderSize())
			if packet.Header.Classification != 0 {
				t.Fatalf("unexpected return frame classification: %+v", packet.Header)
			}
			frames = append(frames, upperFrame{msgID: packet.Header.MsgID, body: packet.Body})
			wire = rest
		}
		return frames
	}
	leaderFrames := readFrames(leaderConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, leaderFrames,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		0x0099,
		uint16(dnfenum.CmdPacketRecoverStamina),
		currentTownUserAreaNotificationMsgID,
		0x0099,
		uint16(dnfenum.CmdPacketRecoverStamina),
	)
	requireTownPartyActorModes(t, leaderFrames, 1002)
	requireTownPartyActorHPFrame(t, leaderFrames[2], townPartyActorHPRows1001And1002()...)
	requireTownMode0InitialHP(t, leaderFrames[0].body, 100)
	requireTownPartyActorOp9(t, leaderFrames[3])
	requireTownPartyActorHPFrame(t, leaderFrames[5], townPartyActorHPRows1001And1002()...)
	requireTownPartyActorOp9(t, leaderFrames[6])
	requireTownPartyActorHPNoLinkPackets(t, leaderFrames)

	memberFrames := readFrames(memberConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, memberFrames,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		0x0099,
		uint16(dnfenum.CmdPacketRecoverStamina),
		currentTownUserAreaNotificationMsgID,
		0x0099,
		uint16(dnfenum.CmdPacketRecoverStamina),
	)
	requireTownPartyActorModes(t, memberFrames, 1001)
	requireTownPartyActorHPFrame(t, memberFrames[2], townPartyActorHPRows1001And1002()...)
	requireTownMode0InitialHP(t, memberFrames[0].body, 100)
	requireTownPartyActorOp9(t, memberFrames[3])
	requireTownPartyActorHPFrame(t, memberFrames[5], townPartyActorHPRows1001And1002()...)
	requireTownPartyActorOp9(t, memberFrames[6])
	requireTownPartyActorHPNoLinkPackets(t, memberFrames)
}
