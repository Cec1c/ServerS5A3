package dnfbridge

import (
	"fmt"
	"testing"
)

// TestPartyCharacterSwitchAuditClearsOldMembershipAndKeepsSurvivor exercises
// the real A -> B bind after character selection. Both directions use the same
// two-character party fixture: the departing character must be removed, the
// remaining character must retain a truthful singleton projection, and the
// newly bound character must not inherit the old team or receive its frames.
func TestPartyCharacterSwitchAuditClearsOldMembershipAndKeepsSurvivor(t *testing.T) {
	tests := []struct {
		name                string
		oldCharacterID      uint16
		newCharacterID      uint16
		survivorCharacterID uint16
		survivorSlot        byte
		preservePartyID     bool
	}{
		{
			name:                "leader_switches_to_new_character",
			oldCharacterID:      1001,
			newCharacterID:      2001,
			survivorCharacterID: 1002,
			survivorSlot:        1,
			preservePartyID:     false,
		},
		{
			name:                "member_switches_to_new_character",
			oldCharacterID:      1002,
			newCharacterID:      2002,
			survivorCharacterID: 1001,
			survivorSlot:        0,
			preservePartyID:     true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			h := newPartyLeaveHarness(t)

			var switching *gameSession
			var switchingConn *bufferConn
			var survivor *gameSession
			var survivorConn *bufferConn
			switch tt.oldCharacterID {
			case 1001:
				switching, switchingConn = h.leader, h.leaderConn
				survivor, survivorConn = h.member, h.memberConn
			case 1002:
				switching, switchingConn = h.member, h.memberConn
				survivor, survivorConn = h.leader, h.leaderConn
			default:
				t.Fatalf("fixture has no session for character %d", tt.oldCharacterID)
			}

			manager := h.service.runtimePartyManagerForService()
			before, found := manager.SnapshotByUser(tt.oldCharacterID, 0)
			if !found || before.ID != h.partyID || before.Leader != 1001 || len(before.Members) != 2 {
				t.Fatalf("fixture party precondition mismatch: found=%t snapshot=%+v", found, before)
			}
			oldProjection := runtimePartyStateSnapshot(switching)
			if oldProjection.PartyID != int(h.partyID) || len(runtimePartyMembers(oldProjection)) != 2 {
				t.Fatalf("fixture session projection precondition mismatch: %+v", oldProjection)
			}

			// The fixture's initial snapshots are irrelevant to the switch. Keep
			// only bytes produced by the real bind path below.
			switchingConn.write.Reset()
			survivorConn.write.Reset()
			h.service.bindGameSessionCharacter(switching, tt.newCharacterID)

			if got := switching.selectedCharacterID; got != tt.newCharacterID {
				t.Fatalf("bind selected character=%d, want=%d", got, tt.newCharacterID)
			}
			if _, found := h.service.onlineGameSession(tt.oldCharacterID); found {
				t.Fatalf("old character %d still owns a game session after switch", tt.oldCharacterID)
			}
			if owner, found := h.service.onlineGameSession(tt.newCharacterID); !found || owner != switching {
				t.Fatalf("new character %d is not owned by the switched session: found=%t owner=%p", tt.newCharacterID, found, owner)
			}

			if snapshot, found := manager.SnapshotByUser(tt.oldCharacterID, 0); found {
				t.Fatalf("old character %d remains in the runtime party: %+v", tt.oldCharacterID, snapshot)
			}
			if snapshot, found := manager.SnapshotByUser(tt.newCharacterID, 0); found {
				t.Fatalf("new character %d inherited the old runtime party: %+v", tt.newCharacterID, snapshot)
			}

			survivorParty, found := manager.SnapshotByUser(tt.survivorCharacterID, 0)
			if !found {
				t.Fatalf("survivor %d lost the runtime party", tt.survivorCharacterID)
			}
			if survivorParty.Leader != tt.survivorCharacterID {
				t.Fatalf("survivor party leader=%d, want=%d", survivorParty.Leader, tt.survivorCharacterID)
			}
			if len(survivorParty.Members) != 1 {
				t.Fatalf("survivor party members=%+v, want one singleton", survivorParty.Members)
			}
			member := survivorParty.Members[0]
			if member.UserID != tt.survivorCharacterID || member.Slot != tt.survivorSlot {
				t.Fatalf("survivor member=%+v, want user=%d slot=%d", member, tt.survivorCharacterID, tt.survivorSlot)
			}
			if tt.preservePartyID {
				if survivorParty.ID != h.partyID {
					t.Fatalf("member switch changed surviving party id=%d, want=%d", survivorParty.ID, h.partyID)
				}
			} else if survivorParty.ID == h.partyID {
				t.Fatalf("leader switch reused the retired party id %d", h.partyID)
			}

			assertPartyProjectionSingleton(
				t,
				"survivor primary cache",
				runtimePartyStateSnapshot(survivor),
				int(survivorParty.ID),
				tt.survivorCharacterID,
				tt.survivorSlot,
				true,
			)
			assertPartyProjectionSingleton(
				t,
				"survivor accessor",
				h.service.runtimePartyStateForSession(survivor),
				int(survivorParty.ID),
				tt.survivorCharacterID,
				tt.survivorSlot,
				true,
			)

			assertPartyProjectionEmpty(t, "switched session / new character cache", switching)
			if state := h.service.runtimePartyStateForSession(switching); state.PartyID != 0 || len(runtimePartyMembers(state)) != 0 {
				t.Fatalf("new character accessor retained old party: %+v", state)
			}

			if got := survivorConn.write.Len(); got == 0 {
				t.Fatal("survivor received no updated singleton party frame")
			}
			if sent, err := h.service.sendCurrentPartyActorFrameProjection(switching, switching, oldProjection, "character_switch_old_projection_audit"); sent || err == nil || err.Error() != fmt.Sprintf("current party projection missing projected actor %d in party %d", tt.newCharacterID, h.partyID) {
				t.Fatalf("old party projection after character switch: sent=%t err=%v", sent, err)
			}
			if got := switchingConn.write.Len(); got != 0 {
				t.Fatalf("old party frame reached new character %d: %d bytes: % X",
					tt.newCharacterID, got, switchingConn.write.Bytes())
			}
		})
	}
}
