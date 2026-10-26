package dnfbridge

import (
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestRealPVFLukeSelectorReturnLeaderFanoutRestoresWaitingMembers(t *testing.T) {
	f, leader, peer := lukeSelectorReturnWaitingFixture(t, true)
	s := f.service
	t.Setenv(dungeonPartySelectReturnFanoutEnv, "on")
	state := alignedcmd.PartyState{PartyID: 1, UserID: leader.selectedCharacterID, MaxMembers: 4, UserState: 1,
		Members: []alignedcmd.PartyMemberState{{UserID: leader.selectedCharacterID, UserState: 1}, {UserID: peer.selectedCharacterID, UserState: 1}}}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(peer, state)
	// Only the leader requests op132. The follower returns through the real
	// party fanout; requiring it to send a second op132 would mask this bug.
	if err := s.handleDungeonBackToVillage(leader, nil); err != nil {
		t.Fatal(err)
	}
	for _, member := range []*gameSession{leader, peer} {
		if !member.confirmedDungeonReturnStatePending || member.selectorReturnPresence == nil {
			t.Fatalf("party selector return lost pending ownership for %d", member.selectedCharacterID)
		}
		if _, found := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID); found {
			t.Fatal("fanout published an unconfirmed actor")
		}
	}
	if err := s.handleTownSetUserPosition(leader, buildTownPositionRequest(580, 243, 5, 100)); err != nil {
		t.Fatal(err)
	}
	if err := lukeSelectorWaitingPresenceError(t, f); err == nil {
		t.Fatal("waiting presence gate admitted an unconfirmed follower")
	}
	if _, ok, err := s.startRuntimeRaidWithEntryCost(leader); ok || !errors.Is(err, errCurrentRaidMembersOutsideWaitingArea) {
		t.Fatal("phase-two start admitted an unconfirmed follower", ok, err)
	}
	if err := s.sendFinishLoadingStatus(peer, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	if !s.onlinePlayers.PeerInSameArea(leader.selectedCharacterID, peer.selectedCharacterID) {
		t.Fatal("fanout follower remained absent after confirming return")
	}
	if err := lukeSelectorWaitingPresenceError(t, f); err != nil {
		t.Fatal("confirmed fanout is absent from the waiting-area scope", err)
	}
	if _, ok, err := s.startRuntimeRaidWithEntryCost(leader); !ok || err != nil {
		t.Fatal("confirmed fanout still blocked phase two", ok, err)
	}
}

func TestRealPVFLukeSelectorReturnWithoutAreaCrossingRestoresWaitingMembers(t *testing.T) {
	f, leader, peer := lukeSelectorReturnWaitingFixture(t)
	s := f.service
	before := lukeSelectorWaitingInventories(t, f)
	for _, member := range []*gameSession{leader, peer} {
		if err := lukeSelectorWaitingPresenceError(t, f); err == nil {
			t.Fatal("waiting presence gate admitted before every return confirmed")
		}
		if _, ok, err := s.startRuntimeRaidWithEntryCost(leader); ok || !errors.Is(err, errCurrentRaidMembersOutsideWaitingArea) {
			t.Fatal("phase-two start admitted before every return confirmed", ok, err)
		}
		if err := s.handleTownSetUserPosition(member, buildTownPositionRequest(580, 243, 5, 100)); err != nil {
			t.Fatal(err)
		}
		p, found := s.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
		if !found || p.Session != member || p.TownID != 32 || p.AreaID != 4 || p.Instance != f.state.RaidKey || p.RaidBattleInstance != f.state.Battle.InstanceID {
			t.Fatalf("confirmed selector return did not restore current waiting ownership: char=%d found=%t presence=%+v", member.selectedCharacterID, found, p)
		}
		generation := member.townPostTransition.generation
		bytesBefore := member.conn.(*bufferConn).write.Len()
		if err := s.ensureCurrentConfirmedDungeonReturnPlayerState(member, "duplicate_confirm"); err != nil || generation != member.townPostTransition.generation || bytesBefore != member.conn.(*bufferConn).write.Len() {
			t.Fatal("duplicate confirmation replayed actor generation", err)
		}
	}
	if !s.onlinePlayers.PeerInSameArea(leader.selectedCharacterID, peer.selectedCharacterID) {
		t.Fatal("confirmed return members are not visible to one another")
	}
	if err := lukeSelectorWaitingPresenceError(t, f); err != nil {
		t.Fatal("confirmed returns are absent from the waiting-area scope", err)
	}
	state, ok, err := s.startRuntimeRaidWithEntryCost(leader)
	if err != nil || !ok || state.Battle == nil || state.Battle.Phase != 1 {
		t.Fatalf("confirmed selector returns still block phase two: ok=%t err=%v", ok, err)
	}
	if !reflect.DeepEqual(before, lukeSelectorWaitingInventories(t, f)) {
		t.Fatal("return/second-phase start changed settled inventory")
	}
}

func TestRealPVFLukeSelectorReturnAlternateConfirmations(t *testing.T) {
	for _, kind := range []string{"op37", "op15"} {
		t.Run(kind, func(t *testing.T) {
			f, leader, peer := lukeSelectorReturnWaitingFixture(t)
			for _, member := range []*gameSession{leader, peer} {
				var err error
				if kind == "op37" {
					err = f.service.sendFinishLoadingStatus(member, make([]byte, 8))
				} else {
					err = f.service.commitPendingDungeonReturnBeforeTownEnterSelect(member, "op15_confirm")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !f.service.onlinePlayers.PeerInSameArea(leader.selectedCharacterID, peer.selectedCharacterID) {
				t.Fatal("alternate confirmation omitted peer presence")
			}
			if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); !ok || err != nil {
				t.Fatal("alternate confirmation still blocked", ok, err)
			}
		})
	}
}

func TestRealPVFLukeSelectorReturnRejectsStaleOwner(t *testing.T) {
	for _, fault := range []string{"channel", "battle", "raid", "replacement", "generation", "entry_reset"} {
		t.Run(fault, func(t *testing.T) {
			f, leader, _ := lukeSelectorReturnWaitingFixture(t)
			s := f.service
			switch fault {
			case "channel":
				leader.channel.ID++
			case "battle":
				f.state.Battle.InstanceID += "-new"
			case "raid":
				f.state.RaidKey++
			case "replacement":
				replacement := f.session()
				replacement.channel, replacement.loginPacketSeen, replacement.accountFromToken = leader.channel, true, true
				s.bindGameSessionCharacter(replacement, leader.selectedCharacterID)
			case "generation":
				s.mu.Lock()
				leader.characterGeneration++
				s.mu.Unlock()
			case "entry_reset":
				resetDungeonEntrySceneGates(leader)
			}
			if err := s.ensureCurrentConfirmedDungeonReturnPlayerState(leader, "stale_return"); err != nil {
				t.Fatal(err)
			}
			if _, found := s.onlinePlayers.PlayerForCharacter(19); found {
				t.Fatal("stale return published presence")
			}
			if leader.selectorReturnPresence != nil {
				t.Fatal("stale return retained owner")
			}
			if f.state.Battle.Phase != 0 {
				t.Fatal("stale return advanced phase")
			}
		})
	}
}

type lukeSelectorFailWriteConn struct{ net.Conn }

func (c lukeSelectorFailWriteConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRealPVFLukeSelectorReturnWaitsForActorWrite(t *testing.T) {
	f, leader, _ := lukeSelectorReturnWaitingFixture(t)
	s := f.service
	conn := leader.conn
	leader.conn = lukeSelectorFailWriteConn{conn}
	if err := s.ensureCurrentConfirmedDungeonReturnPlayerState(leader, "failed_actor_write"); err == nil {
		t.Fatal("failed write accepted")
	}
	if _, found := s.onlinePlayers.PlayerForCharacter(19); found {
		t.Fatal("failed actor write published presence")
	}
	if !leader.confirmedDungeonReturnStatePending || leader.selectorReturnPresence == nil {
		t.Fatal("failed write discarded pending return")
	}
	leader.conn = conn
	if err := s.ensureCurrentConfirmedDungeonReturnPlayerState(leader, "retry_actor_write"); err != nil {
		t.Fatal(err)
	}
	if p, found := s.onlinePlayers.PlayerForCharacter(19); !found || p.Session != leader {
		t.Fatal("successful retry omitted presence")
	}
}
