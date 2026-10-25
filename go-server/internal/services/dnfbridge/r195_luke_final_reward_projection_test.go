package dnfbridge

import (
	"reflect"
	"testing"
	"time"
)

// Both actual 660 commits must unlock final team grants before the unattended
// deadline. This controls the diagnosis: GUI completion and grant completion
// are different observations, and a fixed two-minute wait is not unconditional.
func TestR195RealLukeFinalBothManualCardsUnlockTeamImmediately(t *testing.T) {
	f, sessions := r195MovieStageFixture(t, "luke_phase1")
	flow := f.state.Battle.RewardFlow[1]
	if flow.Recipients[0].Group == flow.Recipients[1].Group {
		t.Fatal("fixture requires independently grouped owners")
	}
	for _, session := range sessions {
		f.conn = session.conn.(*bufferConn)
		dispatchAntonCardRequest(t, f, session, 659, []byte{1})
		f.conn.write.Reset()
		dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
		r195WantFiendRewardWire(t, r195ReadFiendRewardWire(t, f.conn.write.Bytes()), flow,
			map[byte][]uint16{1: {session.selectedCharacterID}}, "kind1")
	}
	at := flow.CompletedAt.Add(time.Second)
	if !at.Before(flow.AutoSelectAt) {
		t.Fatal("fixture did not stay before unattended deadline")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !flow.SquadShownAt.Equal(at) {
		t.Fatal("all committed manual cards still wait for the automatic deadline")
	}
	for _, owner := range flow.Recipients {
		if card := flow.Squad[owner.CharacterID]; card == nil || !card.Committed {
			t.Fatalf("owner%d team grant missing", owner.CharacterID)
		}
	}
}

// Luke's final phase has both pools. If one member does not send 660, its
// automatically committed party item must still reach category0 before the
// team list. Kind3 cannot carry or display that party item on its behalf.
func TestR195RealLukeFinalAutomaticProjectionPreservesBothPools(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "all_automatic"
		if manual {
			name = "one_manual_one_automatic"
		}
		t.Run(name, func(t *testing.T) {
			f, sessions := r195MovieStageFixture(t, "luke_phase1")
			flow := f.state.Battle.RewardFlow[1]
			for i, session := range sessions {
				f.conn = session.conn.(*bufferConn)
				dispatchAntonCardRequest(t, f, session, 659, []byte{1})
				if manual && i == 0 {
					f.conn.write.Reset()
					dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
					r195WantFiendRewardWire(t, r195ReadFiendRewardWire(t, f.conn.write.Bytes()), flow,
						map[byte][]uint16{1: {session.selectedCharacterID}}, "kind1")
				}
			}
			// The fixture completes the raid in the past. A newly displayed
			// owner still receives its full window even after that old fallback.
			at := flow.AutoSelectAt
			for _, deadline := range flow.PartySelectAfter {
				if deadline.After(at) {
					at = deadline
				}
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if flow.SquadShownAt.IsZero() {
				t.Fatal("all automatic choice windows elapsed without squad commit")
			}
			frozen := cloneCurrentAntonRewardFlow(flow)
			for i, session := range sessions {
				conn := session.conn.(*bufferConn)
				if err := f.service.sendSelectedCurrentContainerListsRefresh(session, "r195_luke_final_fixture"); err != nil {
					t.Fatal(err)
				}
				assets := lukeDisplayAssetSnapshot(t, f.service, session)
				conn.write.Reset()
				if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				owners := map[byte][]uint16{1: {session.selectedCharacterID}, 3: {19, 20}}
				sequence := []string{"kind1", "kind3"}
				if manual && i == 0 {
					delete(owners, 1)
					sequence = []string{"kind3"}
				}
				r195WantFiendRewardWire(t, r195ReadFiendRewardWire(t, conn.write.Bytes()), flow, owners, sequence...)
				conn.write.Reset()
				if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil || conn.write.Len() != 0 {
					t.Fatal("repeat final projection was not silent", err, conn.write.Len())
				}
				if !reflect.DeepEqual(assets, lukeDisplayAssetSnapshot(t, f.service, session)) {
					t.Fatal("display changed persisted reward assets or receipts")
				}
			}
			if !reflect.DeepEqual(frozen.Party, flow.Party) || !reflect.DeepEqual(frozen.Squad, flow.Squad) {
				t.Fatal("display changed frozen rewards")
			}
		})
	}
}
