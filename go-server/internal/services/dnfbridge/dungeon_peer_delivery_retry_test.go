package dnfbridge

import (
	"strconv"
	"testing"
)

func TestAF10SkippedPeerProjectionKeepsInitialAndFinalRetries(t *testing.T) {
	for _, path := range []string{"initial", "finish", "landing"} {
		t.Run(path, func(t *testing.T) {
			s, receiver, actor, _, _ := partyDungeonDeathFixture(t)
			t.Setenv(dungeonPartyPeerActorLandingResendEnv, "on")
			for _, p := range []*gameSession{receiver, actor} {
				p.setFinishLoadingGateState(true)
				p.setFinishLoadingGateCompletion(true)
				p.setFinishLoadingGatePostFinish(true)
			}
			key, ready := s.currentDungeonPartyRunKey(actor)
			if !ready {
				t.Fatal("fixture actor has no shared run")
			}
			changed := false
			wrapCurrentCrossSceneProfileBatchCharacterLoads(t, s, func(id string) {
				if changed || id != strconv.Itoa(int(actor.selectedCharacterID)) {
					return
				}
				changed = true
				// Only the peer leaves this room while its profile is prepared.
				// The receiver remains in the same room, so its claimed markers
				// must be explicitly released after the guarded send skips.
				actor.dungeonPartyRunMu.Lock()
				actor.dungeonPartyRun.Seed++
				actor.dungeonPartyRunMu.Unlock()
			})
			invoke := func() {
				t.Helper()
				switch path {
				case "initial":
					if err := s.projectDungeonPartyPeerActors(receiver, "af10_initial"); err != nil {
						t.Fatal(err)
					}
				case "finish":
					s.resendDungeonPartyPeerActorsAfterFinishLoading(receiver, "af10_finish")
				case "landing":
					s.resendDungeonPartyPeerActorToAlreadyLandedMembers(actor, "af10_landing")
				}
			}
			receiver.conn.(*bufferConn).write.Reset()
			actor.conn.(*bufferConn).write.Reset()
			invoke()
			if !changed {
				t.Fatal("profile preparation did not reach the scene-change boundary")
			}
			receiver.dungeonPartyRunMu.Lock()
			initialClaimed := len(receiver.dungeonPeerProjected)
			finalClaimed := len(receiver.dungeonPeerActorResent)
			receiver.dungeonPartyRunMu.Unlock()
			if initialClaimed != 0 || finalClaimed != 0 {
				t.Fatalf("guarded skip consumed retry: initial=%d final=%d", initialClaimed, finalClaimed)
			}
			if receiver.conn.(*bufferConn).write.Len() != 0 || actor.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("projection crossed the peer's changed room")
			}
			actor.dungeonPartyRunMu.Lock()
			actor.dungeonPartyRun = key
			actor.dungeonPartyRunMu.Unlock()
			invoke()
			created, bound, roster := r160UpperPeerActorCounts(splitUpperFrames(t, receiver.conn.(*bufferConn).write.Bytes()), actor.selectedCharacterID)
			if created == 0 || bound == 0 || roster == 0 {
				t.Fatalf("valid retry lost peer projection: mode0=%d mode1=%d op9=%d", created, bound, roster)
			}
			receiver.conn.(*bufferConn).write.Reset()
			actor.conn.(*bufferConn).write.Reset()
			invoke()
			if receiver.conn.(*bufferConn).write.Len() != 0 || actor.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("successful projection was not idempotent")
			}
		})
	}
}
