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

func TestRaidDungeonPartyRetirementAllowsRoomSeedChangeOnlyWithinFrozenRun(t *testing.T) {
	for _, scenario := range []string{"same-run", "new-run", "new-instance", "new-raid", "ordinary-seed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.instance.key.RaidKey = 41
			f.instance.key.RaidInstanceID = "raid-instance"
			f.instance.key.RaidRunID = "admitted-run"
			if scenario == "ordinary-seed" {
				f.instance.key.RaidKey = 0
				f.instance.key.RaidInstanceID, f.instance.key.RaidRunID = "", ""
			}
			current := f.instance.key
			current.Seed++
			current.RoomX++
			current.MapID++
			switch scenario {
			case "new-run":
				current.RaidRunID = "replacement-run"
			case "new-instance":
				current.RaidInstanceID = "replacement-instance"
			case "new-raid":
				current.RaidKey++
			}
			f.member.dungeon.runtime = nil
			f.member.dungeonPartyRun, f.member.dungeonPartyRunValid = current, true
			identity, ok := f.service.boundGameSessionCharacterSnapshot(f.member)
			if !ok {
				t.Fatal("missing bound member")
			}
			applied := 0
			got := f.service.withCurrentDungeonPartyRetirement(identity, f.instance, func() bool { applied++; return true })
			want := scenario == "same-run"
			if got != want || applied != map[bool]int{false: 0, true: 1}[want] {
				t.Fatalf("retirement accepted=%t apply=%d want accepted=%t", got, applied, want)
			}
		})
	}
}

func TestRaidDungeonPartyRetirementAfterRoomChangeRemovesOnlyReturningMember(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	addRetirementPeer(t, f, 101)
	addRetirementPeer(t, f, 102)
	before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	round69AlignInstancePartyID(t, f, before.ID)
	f.instance.key.RaidKey = 41
	f.instance.key.RaidInstanceID, f.instance.key.RaidRunID = "raid-instance", "admitted-run"
	current := f.instance.key
	current.Seed++
	current.RoomX++
	current.MapID++
	f.member.dungeonPartyRun, f.member.dungeonPartyRunValid = current, true
	f.member.dungeon.runtime = nil
	f.service.releasePartyDungeonInstance(f.memberRuntime, 100, "back_to_village_committed")
	if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
		t.Fatal(err)
	}
	if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(100, 0); found {
		t.Fatal("returning member stayed in the ordinary party after instance removal")
	}
	after, found := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	if !found || after.ID != before.ID || len(after.Members) != 3 || f.leader.dungeon.runtime != f.leaderRuntime {
		t.Fatalf("survivor party/instance changed: %+v", after)
	}
	removed := false
	for _, frame := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
		if frame.msgID == currentTownUserLeaveMsgID {
			removed = true
		}
	}
	if !removed {
		t.Fatal("survivor never received actor removal")
	}
}
