package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func townDisplayFrames(t *testing.T, raw []byte) []upperFrame {
	t.Helper()
	var frames []upperFrame
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, raw, dnfproto.GameServerUpperHeaderSize16)
		frames = append(frames, upperFrame{msgID: packet.Header.MsgID, body: packet.Body})
		raw = rest
	}
	return frames
}

func TestTownDisplayOnlyDefaultsAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, setting, profile, source string
		mutate                         func(*Service, *gameSession, *onlinePlayerInfo)
		want                           bool
	}{
		{name: "default off", profile: "official-24.9"},
		{name: "explicit off", setting: "off", profile: "official-24.9"},
		{name: "legacy unchanged", setting: "on", profile: "90cn-decode-bypass-v1"},
		{name: "ordinary", setting: "on", profile: "official-24.9", want: true},
		{name: "finish loading replay", setting: "on", profile: "official-24.9", source: "town_copresence_actor_state_after_finish_loading", want: true},
		{name: "party remote path", setting: "on", profile: "official-24.9", source: "town_remote_party_actor_after_set_user_area"},
		{name: "unknown source", setting: "on", profile: "official-24.9", source: "unknown"},
		{name: "actor has party", setting: "on", profile: "official-24.9", mutate: func(_ *Service, _ *gameSession, a *onlinePlayerInfo) {
			storeRuntimePartyState(a.Session, alignedcmd.PartyState{PartyID: 9})
		}},
		{name: "observer has party", setting: "on", profile: "official-24.9", mutate: func(_ *Service, p *gameSession, _ *onlinePlayerInfo) {
			storeRuntimePartyState(p, alignedcmd.PartyState{PartyID: 9})
		}},
		{name: "private instance", setting: "on", profile: "official-24.9", mutate: func(s *Service, _ *gameSession, a *onlinePlayerInfo) {
			a.Instance = 1
			s.onlinePlayers.EnterArea(a)
		}},
		{name: "raid instance", setting: "on", profile: "official-24.9", mutate: func(s *Service, _ *gameSession, a *onlinePlayerInfo) {
			a.RaidBattleInstance = "other-run"
			s.onlinePlayers.EnterArea(a)
		}},
		{name: "different channel", setting: "on", profile: "official-24.9", mutate: func(s *Service, _ *gameSession, a *onlinePlayerInfo) {
			a.Session.channel.ID++
			s.onlinePlayers.EnterArea(a)
		}},
		{name: "different area", setting: "on", profile: "official-24.9", mutate: func(s *Service, _ *gameSession, a *onlinePlayerInfo) {
			a.AreaID++
			s.onlinePlayers.EnterArea(a)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(townDisplayOnlyEnv, tc.setting)
			_, s, observer, _, _, _ := newTownCoPresenceTestService(t)
			s.options.currentClientVersion = tc.profile
			actor, _ := s.onlinePlayers.PlayerForCharacter(1002)
			if tc.mutate != nil {
				tc.mutate(s, observer, &actor)
			}
			source := tc.source
			if source == "" {
				source = "town_copresence_actor_state_newcomer_to_existing"
			}
			if got := s.currentTownDisplayOnlyProjection(observer, actor, source); got != tc.want {
				t.Fatalf("display-only=%t want=%t", got, tc.want)
			}
		})
	}
}

func TestTownDisplayOnlyWireAndPartyPromotion(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	_, s, observer, newcomer, observerConn, newcomerConn := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	startTownPeerTest(t, s, observer, nil)
	startTownPeerTest(t, s, newcomer, nil)
	actor, _ := s.onlinePlayers.PlayerForCharacter(1002)
	peer, _ := s.onlinePlayers.PlayerForCharacter(1001)
	s.broadcastTownPlayerEnter(&actor, []onlinePlayerInfo{peer})
	drainTownPeerTest(t, s, observer)
	for _, row := range []struct {
		target *gameSession
		conn   *bufferConn
		actor  onlinePlayerInfo
	}{{observer, observerConn, actor}, {newcomer, newcomerConn, peer}} {
		frames := townDisplayFrames(t, row.conn.write.Bytes())
		requireTownPartyActorHPFrameIDs(t, frames, 2, 9, 23)
		if frames[0].body[0] != 0 || binary.LittleEndian.Uint16(frames[0].body[0x4c:]) != row.actor.CharacterID {
			t.Fatal("mode0 lost actor identity")
		}
		wantArea := buildCurrentTownUserAreaNotificationBody(row.actor.CharacterID, row.actor.TownID, row.actor.AreaID,
			row.actor.PositionX, row.actor.PositionY, row.actor.Direction, row.actor.AreaState)
		if !bytes.Equal(frames[2].body, wantArea) {
			t.Fatal("native area/position body changed")
		}
		row.target.townDisplayMu.Lock()
		_, marked := row.target.townDisplayPeers[row.actor.CharacterID]
		row.target.townDisplayMu.Unlock()
		if !marked {
			t.Fatal("display-only generation not tracked")
		}
		row.conn.write.Reset()
	}
	if ok, _, reason, _ := s.forceManagedRuntimeParty(1001, 1002); !ok {
		t.Fatal(reason)
	}
	for _, row := range []struct {
		target *gameSession
		conn   *bufferConn
		actor  uint16
	}{{observer, observerConn, 1002}, {newcomer, newcomerConn, 1001}} {
		frames := townDisplayFrames(t, row.conn.write.Bytes())
		mode1, firstRoster := -1, -1
		for index, frame := range frames {
			if frame.msgID == 2 && len(frame.body) >= 23 && frame.body[0] == 1 && binary.LittleEndian.Uint16(frame.body[21:]) == row.actor {
				if mode1 >= 0 {
					t.Fatal("party promotion repeated full mode1")
				}
				mode1 = index
			}
			// A preceding singleton lobby owns only itself. The full peer must
			// precede the first HP/party table that actually references it.
			if firstRoster < 0 && frame.msgID == 153 {
				for _, member := range parseRealtimeInfoRows(t, frame.body) {
					if member.uid == row.actor {
						firstRoster = index
					}
				}
			}
			if firstRoster < 0 && frame.msgID == 9 && len(frame.body) > 9 && frame.body[9] == currentSceneOp9ActorDisplayKind {
				members, _ := parseOp9PartyFrameForProfile(t, frame.body, false, true)
				for _, member := range members {
					if member.uid == row.actor {
						firstRoster = index
					}
				}
			}
		}
		if mode1 < 0 || firstRoster < 0 || mode1 >= firstRoster {
			t.Fatalf("party HUD preceded full promotion: mode1=%d firstRoster=%d", mode1, firstRoster)
		}
		row.conn.write.Reset()
		if err := s.sendRuntimePartyRosterLocal(row.target, s.runtimePartyStateForSession(row.target), "repeat_roster"); err != nil {
			t.Fatal(err)
		}
		for _, frame := range townDisplayFrames(t, row.conn.write.Bytes()) {
			if frame.msgID == 2 {
				t.Fatal("already promoted peer rebuilt on repeated roster")
			}
		}
	}
}

func TestTownDisplayOnlyFinalLeaveAndSceneReset(t *testing.T) {
	_, s, observer, newcomer, observerConn, _ := newTownCoPresenceTestService(t)
	startTownPeerTest(t, s, observer, nil)
	identity, _ := s.boundGameSessionCharacterSnapshot(newcomer)
	observer.rememberTownDisplayPeer(identity)
	_, peers := s.onlinePlayers.LeaveArea(identity.character)
	s.broadcastTownPlayerLeave(identity.character, peers)
	drainTownPeerTest(t, s, observer)
	frames := townDisplayFrames(t, observerConn.write.Bytes())
	requireTownPartyActorHPFrameIDs(t, frames, 6)
	observer.townDisplayMu.Lock()
	left := len(observer.townDisplayPeers)
	observer.townDisplayMu.Unlock()
	if left != 0 {
		t.Fatal("leave retained display-only tracking")
	}
	observer.rememberTownDisplayPeer(identity)
	observer.townMu.Lock()
	setCurrentTownPositionSceneLocked(observer, observer.selectedCharacterID, 38, 1)
	observer.townMu.Unlock()
	if len(observer.townDisplayPeers) != 0 {
		t.Fatal("new scene retained old tracking")
	}
}
