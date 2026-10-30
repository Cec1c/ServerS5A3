package dnfbridge

import (
	"bytes"
	"context"
	"testing"
)

// Mode8 embeds a normal mode0 at the same offsets after its three-byte
// prefix. It must not clear the town percentage just sent by op153.
func requireTownProfileHP(t *testing.T, body []byte, want byte) {
	t.Helper()
	if len(body) < 3 || body[0] != currentCrossSceneMode8Subtype {
		t.Fatal("expected the actual mode8 profile frame")
	}
	if got := body[townMode0FixtureHPOffset(t, body)]; got != want {
		t.Fatalf("profile HP=%d overwrites preceding town projection %d", got, want)
	}
}

func TestTownPartyProfileHPAfterRealtimeRefresh(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	f.reset()
	if err := f.service.sendTownRemoteActorState(f.target, f.actorInfo, "hp_before_profile"); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(frames) < 3 {
		t.Fatal("normal town actor projection missing")
	}
	want := frames[0].body[townMode0FixtureHPOffset(t, frames[0].body)]
	if want == 0 {
		t.Fatal("fixture needs a nonzero existing town projection")
	}
	f.reset()
	batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	for _, receiver := range []*gameSession{f.target, f.viewer2} {
		if err := batch.send(receiver, f.actor, currentConnectionTownActorOwnerContext(receiver)); err != nil {
			t.Fatal(err)
		}
	}
	for _, conn := range []*bufferConn{f.targetConn, f.viewer2Conn} {
		frames := splitUpperFrames(t, conn.write.Bytes())
		body := requireCurrentCrossSceneProfileBatchMode8(t, frames, 17, currentConnectionTownActorOwnerContext(f.target))
		requireTownProfileHP(t, body, want)
	}
}

func TestTownPartyProfileHPOnExplicitInspection(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	f.reset()
	if err := f.service.sendCurrentCrossScenePeerProfile(context.Background(), f.target, f.actor, 29, 17); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(frames) != 2 || frames[1].body[0] != currentCrossSceneMode3Subtype {
		t.Fatal("explicit inspection must keep the mode8/mode3 pair")
	}
	requireTownProfileHP(t, frames[0].body, 100)
}

func TestTownPartyProfileHPDoesNotMutateSharedTemplate(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
	owner := currentConnectionTownActorOwnerContext(f.target)
	if err := batch.send(f.target, f.actor, owner); err != nil {
		t.Fatal(err)
	}
	template := batch.bodies[currentCrossSceneProfileKey{actor: 17, owner: owner}].data
	original := append([]byte(nil), template...)
	projected, err := f.service.currentTownPartyProfileWithHP(f.target, f.actor, template)
	if err != nil {
		t.Fatal(err)
	}
	requireTownProfileHP(t, projected, 100)
	if template[townMode0FixtureHPOffset(t, template)] != 0 || !bytes.Equal(template, original) {
		t.Fatal("receiver-specific HP leaked into the reusable template")
	}
	want := append([]byte(nil), original...)
	want[townMode0FixtureHPOffset(t, want)] = 100
	if !bytes.Equal(projected, want) {
		t.Fatal("projection changed other mode0 or mode3 fields")
	}
	// The shared profile can also be delivered for a raid relationship where
	// the receiver no longer shares this ordinary party.
	f.service.runtimePartyManagerForService().Leave(29, f.target.characterGeneration)
	unrelated, err := f.service.currentTownPartyProfileWithHP(f.target, f.actor, template)
	if err != nil || !bytes.Equal(unrelated, original) {
		t.Fatalf("former member received the town party projection: %v", err)
	}
}
