package dnfbridge

import (
	"context"
	"fmt"
	"testing"
)

// Completed ESC give-up (42) is a personal departure just like leave-party
// (13). A completed result-page return (72) remains the team-return control.
func TestCompletedPartyPersonalExitPreservesSurvivor(t *testing.T) {
	for _, opcode := range []uint16{13, 42, 72} {
		for _, leaderLeaves := range []bool{true, false} {
			t.Run(fmt.Sprintf("opcode%d/leader%t", opcode, leaderLeaves), func(t *testing.T) {
				f := newCompletionReturnPartyFixture(t)
				f.service.options.currentClientVersion = "official-24.9"
				for _, runtime := range []*runtimeDungeonState{f.leaderRuntime, f.memberRuntime} {
					if err := runtime.Session.CompleteCurrentRoom(); err != nil {
						t.Fatal(err)
					}
					// Both players have already finished their own settlement. This
					// makes the survivor eligible for the unwanted team fanout.
					runtime.settlementEntrySent = true
					runtime.settlementPlayResultReceived = true
					runtime.settlementStatisticReceived = true
					runtime.settlementClearRewardSent = true
					runtime.settlementCardLayoutSent = true
					runtime.settlementCardSelectionSent = true
					runtime.settlementCardRewardCommitted = true
					runtime.settlementCardSideSelectionSent[dungeonCardSideFree] = true
					runtime.advanceSettlementPhase(currentDungeonSettlementPhaseRewardCommitted)
				}
				leaving, staying := f.leader, f.member
				if !leaderLeaves {
					leaving, staying = staying, leaving
				}
				leavingRuntime, stayingRuntime := leaving.dungeon.runtime, staying.dungeon.runtime
				before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(staying.selectedCharacterID, 0)
				f.leaderConn.write.Reset()
				f.memberConn.write.Reset()
				var err error
				if opcode == 72 {
					transition, prepErr := f.service.prepareCurrentDungeonTownTransitionForSession(context.Background(), leaving, leaving.selectedCharacterID)
					if prepErr != nil {
						t.Fatal(prepErr)
					}
					err = f.service.sendCurrentCompletedDungeonReturnToTown(leaving, leavingRuntime, transition, opcode, "test_completed_result_page_return")
				} else if opcode == 13 {
					err = f.service.handleOnlineLeaveParty(leaving, opcode)
				} else {
					err = f.service.handleDungeonGiveupGame(leaving, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if leaving.dungeon.runtime != nil || !leavingRuntime.townReturnOp24Sent {
					t.Fatal("departing player did not finish the completed town route")
				}
				if opcode == 72 && leaderLeaves {
					if staying.dungeon.runtime != nil || !stayingRuntime.townReturnOp24Sent {
						t.Fatal("explicit result-page team return no longer returns the settled member")
					}
					return
				}
				if staying.dungeon.runtime != stayingRuntime || stayingRuntime.partyInstance != f.instance || stayingRuntime.townReturnPending {
					t.Fatal("personal completed exit moved the survivor out of its dungeon")
				}
				for _, frame := range splitUpperFrames(t, staying.conn.(*bufferConn).write.Bytes()) {
					if frame.msgID == currentSceneTransitionMsgID {
						t.Fatal("personal completed exit sent a town transition to the survivor")
					}
				}
				if opcode != 72 {
					manager := f.service.runtimePartyManagerForService()
					if _, found := manager.SnapshotByUser(leaving.selectedCharacterID, 0); found {
						t.Fatal("personal exit retained departing party membership")
					}
					after, found := manager.SnapshotByUser(staying.selectedCharacterID, 0)
					if !found || after.ID != before.ID || after.Leader != staying.selectedCharacterID || len(after.Members) != 1 {
						t.Fatal("personal exit replaced or disbanded the survivor's party")
					}
					if err := f.service.handleDungeonGiveupGame(leaving, nil); err != nil {
						t.Fatal(err)
					}
					if staying.dungeon.runtime != stayingRuntime {
						t.Fatal("replayed personal exit moved the survivor")
					}
				}
			})
		}
	}
}

func TestCompletedPartyPersonalExitSceneConfirmationPreservesSurvivor(t *testing.T) {
	for _, opcode := range []uint16{13, 42} {
		t.Run(fmt.Sprint(opcode), func(t *testing.T) {
			f := newCompletionReturnPartyFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			for _, runtime := range []*runtimeDungeonState{f.leaderRuntime, f.memberRuntime} {
				if err := runtime.Session.CompleteCurrentRoom(); err != nil {
					t.Fatal(err)
				}
				runtime.settlementClearRewardSent = true
			}
			transition, err := f.service.prepareCurrentDungeonTownTransitionForSession(context.Background(), f.leader, f.leader.selectedCharacterID)
			if err != nil {
				t.Fatal(err)
			}
			f.leader.dungeon.mu.Lock()
			err = f.service.sendCurrentDungeonReturnToTownLocked(f.leader, f.leaderRuntime, transition, opcode, "test_completed_personal_pending_route")
			f.leader.dungeon.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err := f.service.commitPendingDungeonReturnForSceneRequest(f.leader, "test_completed_personal_town_confirmation"); err != nil {
				t.Fatal(err)
			}
			if f.leader.dungeon.runtime != nil || f.member.dungeon.runtime != f.memberRuntime || f.memberRuntime.partyInstance != f.instance {
				t.Fatal("completed personal scene confirmation moved another player")
			}
			if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(f.leader.selectedCharacterID, 0); found {
				t.Fatal("completed personal scene confirmation retained departing membership")
			}
			for _, frame := range splitUpperFrames(t, f.memberConn.write.Bytes()) {
				if frame.msgID == currentSceneTransitionMsgID {
					t.Fatal("completed personal scene confirmation sent survivor a town transition")
				}
			}
		})
	}
}
