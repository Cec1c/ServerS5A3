package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPartyRemovalDoesNotLoadCharactersOrInitializeMembers(t *testing.T) {
	f := newPartyFrameBatchRosterFixture(t, 4)
	manager := f.service.runtimePartyManagerForService()
	snapshot, ok := manager.SnapshotByUser(f.sessions[0].selectedCharacterID, 0)
	if !ok || !manager.RetireSnapshot(snapshot) {
		t.Fatal("failed to retire fixture party")
	}
	for _, p := range f.sessions {
		storeRuntimePartyState(p, alignedcmd.PartyState{})
	}
	for _, actor := range f.sessions {
		f.resetConns()
		f.counter.reset()
		sent, err := f.service.sendCurrentPartyActorFrameProjection(f.sessions[0], actor, alignedcmd.PartyState{}, "removal_read_budget")
		if err != nil || !sent {
			t.Fatalf("removal sent=%t err=%v", sent, err)
		}
		if len(f.counter.loadIDs) != 0 {
			t.Fatalf("kind3 removal loaded full characters: %v", f.counter.loadIDs)
		}
		frames := splitUpperFrames(t, f.conns[0].write.Bytes())
		if len(frames) != 1 || frames[0].msgID != 9 {
			t.Fatalf("removal frames=%v", frameIDs(frames))
		}
		owner := f.sessions[0].townActorOwnerChannel
		if currentOwnerContextUsesClientChannel() {
			owner = currentClientSelectedChannel(f.sessions[0])
		}
		want := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(actor.selectedCharacterID), owner, f.service.currentSceneActorRoute())
		if !bytes.Equal(frames[0].body, want) {
			t.Fatalf("removal bytes changed: got=%x want=%x", frames[0].body, want)
		}
	}
}

func TestPartyFrameRosterDoesNotReadUnserializedEquipment(t *testing.T) {
	for _, count := range []int{2, 3, 4} {
		f := newPartyFrameBatchRosterFixture(t, count)
		// The wire oracle is the established enter-record path. Equipment and
		// initialization remain available here so it exercises their old reads.
		original := make(map[uint16]dnfrepo.CharacterRecord)
		for _, p := range f.sessions {
			_, _, row, found := f.service.characterForEnter(context.Background(), p, p.selectedCharacterID)
			if !found {
				t.Fatal("oracle character missing")
			}
			original[p.selectedCharacterID] = row
		}
		repos, _ := f.service.repositoryGroup()
		counter := newRepoCallCounter()
		repos.Equipment = countingEquipmentRepository{EquipmentRepository: repos.Equipment, c: counter, scope: "Equipment"}
		f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
		f.counter.reset()
		batch := newPartyFrameCharacterBatch(f.service)
		var members []currentSceneOp9PartyMemberProjection
		for _, m := range runtimePartyMembers(f.state) {
			r := original[m.UserID]
			members = append(members, currentSceneOp9PartyMemberProjection{State: m, Name: r.Name, Job: byte(numericCharacterStat(r.Job)), Level: byte(r.Level), Grow: characterGrowTypeByte(r)})
		}
		for _, receiver := range f.sessions {
			for _, actor := range f.sessions {
				f.resetConns()
				sent, err := f.service.sendCurrentPartyActorFrameProjectionForDungeonPeer(receiver, actor, f.state, "roster_read_budget", batch)
				if err != nil || !sent {
					t.Fatalf("members=%d sent=%t err=%v", count, sent, err)
				}
				frames := splitUpperFrames(t, receiver.conn.(*bufferConn).write.Bytes())
				if len(frames) != 1 || frames[0].msgID != 9 {
					t.Fatalf("unexpected roster frames: %v", frameIDs(frames))
				}
				row := original[actor.selectedCharacterID]
				want := buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(currentSceneActorObjectKey(actor.selectedCharacterID), row, true, row.Name, currentSceneObjectContext, currentClientSelectedChannelID(receiver), members, currentSceneOp9PartyLeaderSlot(members, f.state.UserID), false, f.service.currentSceneActorRoute())
				if !bytes.Equal(frames[0].body, want) {
					t.Fatalf("member table bytes changed for %d players", count)
				}
			}
		}
		requirePartyFrameBatchUniqueReads(t, f, count)
		if counter.total("Equipment") != 0 {
			t.Fatalf("op9 read unserialized equipment: %s", counter.summary())
		}
	}
}
