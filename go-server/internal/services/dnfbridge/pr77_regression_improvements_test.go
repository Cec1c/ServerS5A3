package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestPR77RelayLegPortsStayStableAcrossFreshRooms(t *testing.T) {
	portStart := testCurrentPartyUDPRelayPortStart(t)
	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	wantAB := uint16(portStart)
	wantBA := uint16(portStart + 1)
	for cycle := 0; cycle < 16; cycle++ {
		cycle := cycle
		t.Run("cycle_"+string(rune('A'+cycle)), func(t *testing.T) {
			relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
				enabled:                true,
				advertiseIP:            "127.0.0.1",
				portStart:              portStart,
				portCount:              64,
				probeListen:            false,
				seedForward:            false,
				observedEndpointFanout: false,
			})
			if err != nil {
				t.Fatalf("create relay: %v", err)
			}
			defer func() { _ = relay.Close() }()
			ordered := append([]currentPartyUDPRelayBinding(nil), bindings...)
			if cycle%2 == 1 {
				ordered[0], ordered[1] = ordered[1], ordered[0]
			}
			if !relay.Sync(77, ordered) {
				t.Fatal("relay sync failed")
			}
			relay.mu.Lock()
			ab := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 101, peerID: 202}]
			ba := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 202, peerID: 101}]
			ports := [2]uint16{0, 0}
			if ab != nil {
				ports[0] = ab.port
			}
			if ba != nil {
				ports[1] = ba.port
			}
			relay.mu.Unlock()
			if ab == nil || ba == nil {
				t.Fatalf("missing directed legs AB=%p BA=%p", ab, ba)
			}
			if ports[0] != wantAB || ports[1] != wantBA {
				t.Fatalf("cycle=%d owner101=%d owner202=%d want=%d/%d", cycle, ports[0], ports[1], wantAB, wantBA)
			}
		})
	}
}

func TestPR77FiendWarRetiresOnlyAfterPeerLeavesSameRun(t *testing.T) {
	manager := party.NewRuntimePartyManager()
	created := manager.Create(party.RuntimePartyMember{UserID: 19, SessionGeneration: 1, Slot: 0}, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatalf("create party: %+v", created)
	}
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{UserID: 20, SessionGeneration: 1, Slot: 1})
	if !joined.OK {
		t.Fatalf("join party: %+v", joined)
	}
	frozen, found := manager.SnapshotByID(created.Party.ID)
	if !found || len(frozen.Members) != 2 {
		t.Fatalf("party snapshot=%+v found=%t", frozen, found)
	}

	self := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, characterGeneration: 1, accountID: "account-1"}
	peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 20, characterGeneration: 1, accountID: "account-20"}
	service := &Service{
		gameSessions:        map[uint16]*gameSession{19: self, 20: peer},
		runtimePartyManager: manager,
	}
	peer.dungeon.runtime = &runtimeDungeonState{raidEntry: &currentRaidDungeonEntry{RunID: "run-77"}}
	runtime := &runtimeDungeonState{
		Character: dnfrepo.CharacterRecord{CharacterID: "19"},
		raidEntry: &currentRaidDungeonEntry{
			ChannelType:   42,
			RunID:         "run-77",
			fiendWarParty: &frozen,
		},
	}

	if !service.retireCurrentFiendWarEncounterParty(self, runtime) {
		t.Fatal("retirement hook rejected active encounter")
	}
	if current, found := manager.SnapshotByID(frozen.ID); !found || len(current.Members) != 2 {
		t.Fatalf("party retired while peer remained in same run: %+v found=%t", current, found)
	}

	peer.dungeon.runtime = nil
	if !service.retireCurrentFiendWarEncounterParty(self, runtime) {
		t.Fatal("final actor did not reach retirement path")
	}
	if _, found := manager.SnapshotByID(frozen.ID); found {
		t.Fatal("party survived after the last member left the run")
	}
}

func TestPR77FiendWarEntryChecksInitiatorAreaNotFollowerSource(t *testing.T) {
	for _, tc := range []struct {
		name          string
		initiatorArea byte
		followerArea  byte
		wantOK        bool
	}{
		{name: "initiator_target_follower_source", initiatorArea: 7, followerArea: 6, wantOK: true},
		{name: "initiator_still_source", initiatorArea: 6, followerArea: 7, wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := raidSafetyFixture(t, nil, 2)
			s := f.service
			s.options.currentClientVersion = "official-24.9"
			leader := f.session()
			leader.channel.ID, leader.channel.Type = 23, 42
			leader.loginPacketSeen, leader.accountFromToken = true, true
			leader.conn = &bufferConn{}
			peer := f.session()
			peer.selectedCharacterID, peer.accountID = 20, "account-20"
			peer.channel.ID, peer.channel.Type = 23, 42
			peer.loginPacketSeen, peer.accountFromToken = true, true
			peer.conn = &bufferConn{}
			s.bindGameSessionCharacter(leader, 19)
			s.bindGameSessionCharacter(peer, 20)

			config := &fiendwar.FiendWarBattleConfig{
				WaitingTown: 98,
				WaitingArea: 0,
				Areas: map[int]fiendwar.FiendWarArea{
					6: {Index: 6, Dungeon: 0},
					7: {Index: 7, Dungeon: 4957, MemberMax: 2},
				},
				Symbols: map[string]uint32{},
			}
			battle := &fiendwar.FiendWarBattle{
				Config:        config,
				Symbols:       map[uint32]int{},
				Areas:         map[int]fiendwar.FiendWarAreaState{7: {Enemy: fiendwar.FiendWarUnit{ID: 1}}},
				Timers:        map[fiendwar.FiendWarTimerKey]fiendwar.FiendWarTimer{},
				DungeonStates: map[int]string{},
				GroupAreas:    map[byte]int{1: 7, 2: 7},
				MemberAreas:   map[uint16]int{19: 7, 20: 7},
			}
			f.state.Started = true
			f.state.Battle = &runtimeRaidBattle{
				InstanceID:     "00112233445566778899aabbccddeeff",
				Mode:           1,
				ChannelID:      leader.channel.ID,
				ChannelType:    leader.channel.Type,
				Phase:          0,
				EnterAfter:     time.Now().Add(-time.Minute),
				Deadline:       time.Now().Add(time.Hour),
				Dungeons:       []raid.DungeonState{{ID: 4957, State: 0}},
				MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"},
				MemberGroups:   map[uint16]byte{19: 1, 20: 2},
				FiendWar:       battle,
				RunDepartures:  map[string]map[uint16]bool{},
				MemberClears:   map[uint16]uint32{},
				MemberLives:    map[currentRaidMemberRun]currentRaidMemberLife{},
				ClearedRuns:    map[string]currentRaidClearedRun{},
			}
			leader.townSelectorOriginSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 98, AreaID: tc.initiatorArea, PositionValid: true}
			leader.townSelectorOriginBound = true
			leader.townPositionSnapshot = leader.townSelectorOriginSnapshot
			peer.townSelectorOriginSnapshot = currentTownPositionSnapshot{CharacterID: 20, TownID: 98, AreaID: tc.followerArea, PositionValid: true}
			peer.townSelectorOriginBound = true
			peer.townPositionSnapshot = peer.townSelectorOriginSnapshot

			leaderRuntime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 4957}, AccountID: "account-1", Character: dnfrepo.CharacterRecord{CharacterID: "19"}}
			peerRuntime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 4957}, AccountID: "account-20", Character: dnfrepo.CharacterRecord{CharacterID: "20"}}
			err := s.freezeCurrentRaidDungeonGroupEntry(
				leader,
				leaderRuntime,
				[]*runtimePartyPreparedDungeonEntry{{session: peer, runtime: peerRuntime}},
			)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("initiator in target and follower in source rejected: %v", err)
				}
				if leaderRuntime.raidEntry == nil || peerRuntime.raidEntry == nil {
					t.Fatal("group entry was not frozen for both members")
				}
				if got := leaderRuntime.raidEntry.fiendWarOrigin.AreaID; got != tc.initiatorArea {
					t.Fatalf("frozen origin area=%d want initiator area=%d", got, tc.initiatorArea)
				}
				if got := peer.townPositionSnapshot.AreaID; got != tc.followerArea {
					t.Fatalf("follower source area changed=%d want=%d", got, tc.followerArea)
				}
			} else if err == nil {
				t.Fatal("initiator outside the target area was accepted")
			} else if leaderRuntime.raidEntry != nil || peerRuntime.raidEntry != nil {
				t.Fatal("failed initiator preflight partially froze the group")
			}
		})
	}
}
