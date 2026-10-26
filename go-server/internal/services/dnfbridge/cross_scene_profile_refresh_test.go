package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCrossScenePartyRefreshAppendsProfilesAfterExistingTransport(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	snapshot, found := f.service.runtimePartyManagerForService().SnapshotByUser(29, 0)
	if !found {
		t.Fatal("missing central party")
	}
	// An unauthenticated profile identity suppresses only the new background
	// projection. Capture the established transport/roster stream for comparison.
	f.target.accountID, f.actor.accountID, f.viewer2.accountID = "", "", ""
	f.reset()
	if err := f.service.sendManagedRuntimePartySnapshots(snapshot.StateFor(29)); err != nil {
		t.Fatal(err)
	}
	baseline := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(baseline) == 0 {
		t.Fatal("fixture emitted no existing transport/roster")
	}
	f.target.accountID, f.actor.accountID, f.viewer2.accountID = "dnf:1", "dnf:2", "dnf:3"
	f.reset()
	if err := f.service.sendManagedRuntimePartySnapshots(snapshot.StateFor(29)); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(frames) != len(baseline)+2 {
		t.Fatalf("frames=%v baseline=%v, want two appended profiles", townPartyActorHPFrameIDs(frames), townPartyActorHPFrameIDs(baseline))
	}
	for i, old := range baseline {
		if frames[i].msgID != old.msgID || !bytes.Equal(frames[i].body, old.body) {
			t.Fatalf("existing transport/roster frame %d changed", i)
		}
	}
	seen := make(map[uint16]bool)
	for _, frame := range frames[len(baseline):] {
		if frame.msgID != 2 || len(frame.body) < 78 || frame.body[0] != 8 {
			t.Fatal("roster tail is not a complete auxiliary profile")
		}
		id := binary.LittleEndian.Uint16(frame.body[76:78])
		if (id != 17 && id != 1001) || seen[id] {
			t.Fatalf("unexpected or duplicate peer profile %d", id)
		}
		seen[id] = true
	}
}

func TestCrossSceneRaidRefreshPrimesAppearanceBetweenProfilesAndRoster(t *testing.T) {
	s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
	leader.connectionTownActorOwnerChannel = byte(leader.channel.ID)
	member.connectionTownActorOwnerChannel = byte(member.channel.ID)
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		conn *bufferConn
		peer uint16
	}{{lc, member.selectedCharacterID}, {mc, leader.selectedCharacterID}} {
		frames := splitUpperFrames(t, tc.conn.write.Bytes())
		if len(frames) < 3 || frames[0].msgID != 752 || frames[2].msgID != 591 {
			t.Fatalf("raid refresh must retain 752 -> auxiliary profile -> 591, got %v", townPartyActorHPFrameIDs(frames))
		}
		requireCurrentCrossSceneProfileBatchMode8(t, frames[1:2], tc.peer, byte(state.RaidKey>>16))
	}
}

func TestCrossSceneRaidRefreshDoesNotPrimeDirectoryViewer(t *testing.T) {
	s, leader, member, lc, _, state := prepareApprovedCurrentRaid(t)
	leader.connectionTownActorOwnerChannel = byte(leader.channel.ID)
	// Keep the directory snapshot but remove the current authoritative member.
	s.raidMu.Lock()
	live := s.runtimeRaidByMemberLocked(leader.selectedCharacterID)
	for i, m := range live.Members {
		if m.CharID == leader.selectedCharacterID {
			live.Members = append(live.Members[:i], live.Members[i+1:]...)
			break
		}
	}
	s.raidMu.Unlock()
	s.sendCurrentRaidCrossSceneProfiles(newCurrentCrossSceneProfileBatch(s, context.Background()), leader, state)
	if lc.write.Len() != 0 || member.selectedCharacterID == 0 {
		t.Fatal("directory viewer received a member-only full profile")
	}
}

func TestCrossSceneWearRefreshReloadsAppearanceAndClearsRemovedFashion(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "off")
	f := newTownPartyActorHPFixture(t, true)
	f.target.accountID, f.actor.accountID = "dnf:1", "dnf:2"
	f.target.connectionTownActorOwnerChannel = byte(f.target.channel.ID)
	info := f.actorInfo
	info.AreaID++
	f.service.onlinePlayers.EnterArea(&info)
	repos, _ := f.service.repositoryGroup()
	for _, item := range []uint32{414500098, currentActorMode0AppearanceEmptyItem, 414500099} {
		equipment := dnfrepo.EquipmentRecord{CharacterID: "17"}
		if item != currentActorMode0AppearanceEmptyItem {
			equipment.Entries = map[string]dnfrepo.EquipmentEntry{"coat": {SlotIndex: 3, ItemID: int64(item)}}
		}
		if err := repos.Equipment.Save(context.Background(), equipment); err != nil {
			t.Fatal(err)
		}
		f.reset()
		sent, err := f.service.sendSelectedActorMode0AppearanceWithBuffPolicy(f.actor, "test_wear", "committed", nil, true)
		if err != nil || !sent {
			t.Fatalf("own appearance sent=%v err=%v", sent, err)
		}
		own := splitUpperFrames(t, f.actorConn.write.Bytes())
		if len(own) != 1 || own[0].msgID != 2 || own[0].body[0] != 0 {
			t.Fatalf("wear changed the actor's existing mode0 stream: %v", townPartyActorHPFrameIDs(own))
		}
		body := requireCurrentCrossSceneProfileBatchMode8(t, splitUpperFrames(t, f.targetConn.write.Bytes()), 17, currentConnectionTownActorOwnerContext(f.target))
		start := currentSceneObjectTailStartForTest("peer") + 6
		if len(body) <= start || body[start] != currentActorMode0AppearanceSlotCount {
			t.Fatal("full appearance rows missing from auxiliary profile")
		}
		if got := currentActorMode0AppearanceGoldenItemID(t, body[start:], 3); got != item {
			t.Fatalf("peer coat=%d want current repository item %d", got, item)
		}
	}
}

func TestCrossSceneWearRefreshDoesNotFailCommittedWearOnPeerWrite(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "off")
	f := newTownPartyActorHPFixture(t, true)
	f.target.accountID, f.actor.accountID = "dnf:1", "dnf:2"
	f.target.connectionTownActorOwnerChannel = byte(f.target.channel.ID)
	bad := &currentCrossSceneProfileBatchFailConn{}
	f.target.conn = bad
	f.reset()
	sent, err := f.service.sendSelectedActorMode0AppearanceWithBuffPolicy(f.actor, "test_wear", "committed", nil, true)
	if err != nil || !sent || bad.writes != 1 || f.actorConn.write.Len() == 0 {
		t.Fatalf("peer failure undid own committed appearance: sent=%v err=%v peerWrites=%d", sent, err, bad.writes)
	}
}
