package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Drive the real completed-return route and the stream dispatcher, rather
// than fabricating townPostTransition.generation (completed returns do not
// advance that counter). A cold-login rebuild must not replace this actor.
func TestCompletedReturnDoesNotRebuildLoginActorOnNextClientPacket(t *testing.T) {
	s, session, runtime := newBackToVillageRuntime(t)
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("repositories missing")
	}
	enableTownMoveSkillProjection(t, s, repos, "99")
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	transition, err := s.prepareCurrentDungeonTownTransition(context.Background(), session.selectedCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentCompletedDungeonReturnToTown(session, runtime, transition, uint16(dnfenum.CmdPacketEplpCommand), "r132_completed_return"); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil {
		t.Fatal("completed return did not detach runtime")
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := s.handleGameStreamPacket(session, loginActorGenerationLegacyStream(buildLegacyTownSceneReadyAckPacket())); err != nil {
		t.Fatal(err)
	}
	if !session.initialTownLegacySceneReadyAccepted {
		t.Fatal("scene callback not accepted")
	}
	conn.write.Reset()
	if err := s.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
		t.Fatal(err)
	}
	for _, frame := range splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes()) {
		if frame.Header.Classification == 0 && frame.Header.MsgID == 2 && len(frame.Body) > 0 && (frame.Body[0] == 0 || frame.Body[0] == 1) {
			t.Errorf("completed return was misclassified as cold login: extra mode%d actor frame", frame.Body[0])
		}
	}
}
