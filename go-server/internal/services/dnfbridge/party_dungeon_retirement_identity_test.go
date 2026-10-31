package dnfbridge

import (
	"reflect"
	"testing"
)

func TestPartyDungeonRetirementIdentityRejectsNewerRun(t *testing.T) {
	tests := []struct {
		name         string
		caller       string
		supersededBy string
	}{
		{name: "leader newer runtime", caller: "leader", supersededBy: "newer-runtime"},
		{name: "leader newer run key", caller: "leader", supersededBy: "newer-run-key"},
		{name: "member newer runtime", caller: "member", supersededBy: "newer-runtime"},
		{name: "member newer run key", caller: "member", supersededBy: "newer-run-key"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newLeaderExitFixture(t)
			manager := f.service.runtimePartyManagerForService()
			before, found := manager.SnapshotByID(f.instance.key.PartyID)
			if !found {
				t.Fatal("fixture party is missing from the manager")
			}

			session := f.leader
			if tc.caller == "member" {
				f.service.options.currentClientVersion = "official-24.9"
				session = f.member
			}

			switch tc.supersededBy {
			case "newer-runtime":
				session.dungeon.mu.Lock()
				session.dungeon.runtime = &runtimeDungeonState{
					partyInstance: &dungeonPartyInstance{key: f.instance.key},
				}
				session.dungeon.mu.Unlock()
			case "newer-run-key":
				session.dungeon.mu.Lock()
				session.dungeon.runtime = nil
				session.dungeon.mu.Unlock()

				newerRun := f.instance.key
				newerRun.Seed++
				session.dungeonPartyRunMu.Lock()
				session.dungeonPartyRun = newerRun
				session.dungeonPartyRunValid = true
				session.dungeonPartyRunMu.Unlock()
			default:
				t.Fatalf("unknown superseding run kind %q", tc.supersededBy)
			}

			switch tc.caller {
			case "leader":
				if sent := f.service.disbandRuntimePartyOnDungeonLeaderExit(
					f.leader,
					f.instance,
					f.leader.selectedCharacterID,
					"retirement_identity_test",
				); sent != 0 {
					t.Fatalf("stale leader retirement sent %d packets", sent)
				}
			case "member":
				if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
					t.Fatalf("stale member giveup returned an error: %v", err)
				}
			default:
				t.Fatalf("unknown caller %q", tc.caller)
			}

			after, found := manager.SnapshotByID(before.ID)
			if !found {
				t.Fatalf("manager party %d was retired by a stale run", before.ID)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("manager snapshot changed by stale run: before=%+v after=%+v", before, after)
			}
			if got := f.leaderConn.write.Len(); got != 0 {
				t.Fatalf("stale run sent %d bytes to the leader: %x", got, f.leaderConn.write.Bytes())
			}
			if got := f.memberConn.write.Len(); got != 0 {
				t.Fatalf("stale run sent %d bytes to the member: %x", got, f.memberConn.write.Bytes())
			}
		})
	}
}

func TestCurrentDungeonPartyRetirementAllowsRoomCoordinateAdvanceWithinInstance(t *testing.T) {
	f := newLeaderExitFixture(t)
	identity, ok := f.service.boundGameSessionCharacterSnapshot(f.member)
	if !ok {
		t.Fatal("member identity is missing")
	}

	newerRoom := f.instance.key
	newerRoom.RoomX++
	newerRoom.RoomY++
	f.member.dungeonPartyRunMu.Lock()
	f.member.dungeonPartyRun = newerRoom
	f.member.dungeonPartyRunValid = true
	f.member.dungeonPartyRunMu.Unlock()

	applied := 0
	if !f.service.withCurrentDungeonPartyRetirement(identity, f.instance, func() bool {
		applied++
		return true
	}) {
		t.Fatal("same instance with advanced room coordinates was rejected")
	}
	if applied != 1 {
		t.Fatalf("same instance apply count=%d want 1", applied)
	}
}
