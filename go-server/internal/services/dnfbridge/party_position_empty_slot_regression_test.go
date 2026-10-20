package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

// The position request carries the destination slot directly. When that slot is
// empty it must remain the destination; selecting an earlier free slot would
// silently move the member somewhere the leader did not request.
func TestChangePartyMemberPositionExplicitEmptySlot(t *testing.T) {
	t.Run("destination_slot_three_is_not_remapped_to_the_hole_at_one", func(t *testing.T) {
		service, manager, sessions, generations := newPositionRegressionParty(t, 1001, 1002, 1003)
		transferred := manager.TransferLeader(1001, generations[1001], 1003, generations[1003])
		if !transferred.OK {
			t.Fatalf("transfer leader: %+v", transferred)
		}
		left := manager.Leave(1002, generations[1002])
		if !left.OK || left.LeaderMoved {
			t.Fatalf("middle member leave: %+v", left)
		}
		service.publishRuntimePartySnapshot(left.Party)
		if got := runtimePartySlotList(left.Party); got != "0:1001,2:1003" {
			t.Fatalf("fixture slots=%s want 0:1001,2:1003", got)
		}

		if err := service.handleOnlineChangePartyMemberPosition(
			sessions[1003], uint16(dnfenum.CmdPacketChangePartyMemberPosition), []byte{0, 3},
		); err != nil {
			t.Fatalf("change position: %v", err)
		}
		snapshot, found := manager.SnapshotByUser(1003, generations[1003])
		if !found || snapshot.Leader != 1003 {
			t.Fatalf("leader snapshot found=%t snapshot=%+v", found, snapshot)
		}
		if got := runtimePartySlotList(snapshot); got != "2:1003,3:1001" {
			t.Fatalf("explicit empty destination was not honored: slots=%s want 2:1003,3:1001", got)
		}
	})

	t.Run("swap_across_a_hole_keeps_the_requested_target", func(t *testing.T) {
		service, manager, sessions, generations := newPositionRegressionParty(t, 1001, 1002, 1003, 1004)
		left := manager.Leave(1003, generations[1003])
		if !left.OK || left.LeaderMoved {
			t.Fatalf("member at slot two leave: %+v", left)
		}
		service.publishRuntimePartySnapshot(left.Party)
		if got := runtimePartySlotList(left.Party); got != "0:1001,1:1002,3:1004" {
			t.Fatalf("fixture slots=%s want 0:1001,1:1002,3:1004", got)
		}

		if err := service.handleOnlineChangePartyMemberPosition(
			sessions[1001], uint16(dnfenum.CmdPacketChangePartyMemberPosition), []byte{3, 1},
		); err != nil {
			t.Fatalf("change position across hole: %v", err)
		}
		snapshot, found := manager.SnapshotByUser(1001, generations[1001])
		if !found || snapshot.Leader != 1001 {
			t.Fatalf("leader snapshot found=%t snapshot=%+v", found, snapshot)
		}
		if got := runtimePartySlotList(snapshot); got != "0:1001,1:1004,3:1002" {
			t.Fatalf("cross-hole swap slots=%s want 0:1001,1:1004,3:1002", got)
		}
	})
}

func newPositionRegressionParty(t *testing.T, userIDs ...uint16) (*Service, *party.RuntimePartyManager, map[uint16]*gameSession, map[uint16]uint64) {
	t.Helper()
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	channel := channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038}
	sessions := make(map[uint16]*gameSession, len(userIDs))
	generations := make(map[uint16]uint64, len(userIDs))
	for _, userID := range userIDs {
		session := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: userID}
		service.bindGameSessionCharacter(session, userID)
		identity, ok := service.boundGameSessionCharacterSnapshot(session)
		if !ok {
			t.Fatalf("session %d is not bound", userID)
		}
		sessions[userID] = session
		generations[userID] = identity.generation
	}

	manager := service.runtimePartyManagerForService()
	if manager == nil {
		t.Fatal("runtime party manager is unavailable")
	}
	leader := userIDs[0]
	created := manager.Create(party.RuntimePartyMember{
		UserID:            leader,
		SessionGeneration: generations[leader],
		State:             alignedcmd.PartyMemberState{UserID: leader, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create runtime party: %+v", created)
	}
	for _, userID := range userIDs[1:] {
		joined := manager.Join(created.Party.ID, party.RuntimePartyMember{
			UserID:            userID,
			SessionGeneration: generations[userID],
			State:             alignedcmd.PartyMemberState{UserID: userID, UserState: 1, HPPercent: 100, MPPercent: 100},
		})
		if !joined.OK {
			t.Fatalf("join runtime party %d: %+v", userID, joined)
		}
	}
	return service, manager, sessions, generations
}
