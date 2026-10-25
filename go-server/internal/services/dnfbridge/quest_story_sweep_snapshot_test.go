package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestStorySweepCommittedProjectionEmptyActiveAndResumedGate(t *testing.T) {
	for _, source := range []string{"ticket", "manual", "question_finish"} {
		t.Run(source, func(t *testing.T) {
			s, p, g, r := questGateRefreshFixture(t)
			r.PostCommitQuest.States[3868] = repo.QuestState{Status: "completed"}
			r.QuestID = 3868
			r.ResumedStoryQuestIDs = []int64{3156}
			before, _, _ := g.Character.Load(context.Background(), "29")
			var err error
			if source == "question_finish" {
				err = s.sendCurrentFinishQuestPostCommitSnapshots(p, r)
			} else {
				err = s.sendCommittedStorySweepSnapshots(p, r.PostCommitCharacter, r.PostCommitQuest, []int64{3868, 3156}, source)
			}
			if err != nil {
				t.Fatal(err)
			}
			wire := p.conn.(*bufferConn).write.Bytes()
			view := emptyActiveNativeQuestView{completed: make(map[int64]bool), gate: make(map[int64]bool)}
			view.consume(t, wire)
			if !view.gate[3156] || !view.completed[3868] {
				t.Fatal("zero-active erased completed state")
			}
			first, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
			if first.Header.MsgID != currentActiveQuestSnapshotMsgID {
				t.Fatal("active not first")
			}
			_, wire = splitTownAreaTransitionSequence(t, wire, 29, 38, 3, 562, 321, 5, 3)
			clear, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
			assertFinishBridgeClearQuestProjection(t, clear, 3156)
			available, wire := splitCurrentGameServerUpperPacketAuto(t, wire)
			if available.Header.MsgID != currentAcceptableQuestListMsgID || len(wire) != 0 {
				t.Fatal("available must follow restored completed table")
			}
			after, _, _ := g.Character.Load(context.Background(), "29")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("projection mutated character")
			}
		})
	}
}

func TestStorySweepProjectionRejectsChangedCharacter(t *testing.T) {
	s, p, _, r := questGateRefreshFixture(t)
	p.selectedCharacterID++
	if err := s.sendCommittedStorySweepSnapshots(p, r.PostCommitCharacter, r.PostCommitQuest, []int64{3156}, "test"); err == nil {
		t.Fatal("wrong character projection allowed")
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("stale snapshot emitted")
	}
}
