package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestPartyPeerFinalProjectionWaitsForBothFinishTails(t *testing.T) {
	f := newLeaderExitFixture(t)
	for _, p := range []*gameSession{f.leader, f.member} {
		p.setFinishLoadingGateState(false)
		p.setFinishLoadingGateCompletion(false)
		p.setFinishLoadingGatePostFinish(false)
	}
	// A receiver finishing first must leave the final peer resend available.
	f.leader.setFinishLoadingGateState(true)
	f.leader.setFinishLoadingGateCompletion(true)
	f.leader.setFinishLoadingGatePostFinish(true)
	f.service.resendDungeonPartyPeerActorsAfterFinishLoading(f.leader, "test_first_finishes")
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("final resend was consumed before the peer finished loading")
	}
	f.member.setFinishLoadingGateState(true)
	f.member.setFinishLoadingGateCompletion(true)
	f.member.setFinishLoadingGatePostFinish(true)
	if err := f.service.sendCurrentFinishLoadingCharacterState(f.member, "test_late_finishes"); err != nil {
		t.Fatal(err)
	}
	for _, receiver := range []*gameSession{f.leader, f.member} {
		actor := uint16(99)
		if receiver == f.leader {
			actor = 100
		}
		created, bound := false, false
		for _, frame := range writeFrames(t, receiver) {
			if frame.msgID != 2 {
				continue
			}
			if len(frame.body) > 78 && frame.body[0] == 0 && binary.LittleEndian.Uint16(frame.body[76:78]) == actor {
				created = true
			}
			if len(frame.body) > 23 && frame.body[0] == 1 && binary.LittleEndian.Uint16(frame.body[21:23]) == actor {
				bound = true
			}
		}
		if !created || !bound {
			t.Fatalf("receiver %d final peer actor %d: created=%t bound=%t", receiver.selectedCharacterID, actor, created, bound)
		}
	}
	if err := f.service.sendCurrentFinishLoadingCharacterState(f.member, "test_late_finish_replay"); err != nil {
		t.Fatal(err)
	}
	if f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
		t.Fatal("repeated finish tail replayed peer actors")
	}
}
