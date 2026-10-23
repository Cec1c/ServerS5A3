package dnfbridge

import (
	"errors"
	"reflect"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestR195RealDisplayedFinalCardsDoNotWaitUnattendedDeadline(t *testing.T) {
	for _, mode := range []string{"fiendwar"} {
		t.Run(mode, func(t *testing.T) {
			f, sessions := r195PublicFiendRewardFixture(t, mode)
			flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
			flow.AutoSelectAt = time.Now().Add(2 * time.Minute)
			var last time.Time
			for _, session := range sessions {
				f.conn = session.conn.(*bufferConn)
				f.conn.write.Reset()
				dispatchAntonCardRequest(t, f, session, 659, []byte{1})
				deadline := flow.PartySelectAfter[session.selectedCharacterID]
				if deadline.IsZero() || !deadline.Before(flow.AutoSelectAt) {
					t.Fatal("delivered card screen did not start its owner's short choice window")
				}
				if deadline.After(last) {
					last = deadline
				}
				// Duplicate movie completion and periodic projection cannot extend it.
				dispatchAntonCardRequest(t, f, session, 659, []byte{1})
				if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				if flow.PartySelectAfter[session.selectedCharacterID] != deadline {
					t.Fatal("duplicate delivery extended the choice window")
				}
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(last, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if flow.SquadShownAt.IsZero() {
				t.Fatal("both displayed owners still wait for unattended fallback")
			}
			frozen := cloneCurrentAntonRewardFlow(flow)
			for _, session := range sessions {
				if !flow.Party[session.selectedCharacterID].Committed || !flow.Squad[session.selectedCharacterID].Committed {
					t.Fatal("both actual reward pools must be committed")
				}
				conn := session.conn.(*bufferConn)
				conn.write.Reset()
				if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				wire := r195ReadFiendRewardWire(t, conn.write.Bytes())
				r195WantFiendRewardWire(t, wire, flow, map[byte][]uint16{1: {session.selectedCharacterID}, 3: {19, 20}}, "kind1", "kind3")
				conn.write.Reset()
				if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil || conn.write.Len() != 0 {
					t.Fatal("duplicate display was not silent", err)
				}
			}
			if !reflect.DeepEqual(frozen.Party, flow.Party) || !reflect.DeepEqual(frozen.Squad, flow.Squad) {
				t.Fatal("projection changed committed plans")
			}
		})
	}
}

func TestR195RealChoiceWindowIsPerRecipientAndPreservesManualCards(t *testing.T) {
	for _, mode := range []string{"fiendwar"} {
		t.Run(mode, func(t *testing.T) {
			f, sessions := r195PublicFiendRewardFixture(t, mode)
			flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
			flow.AutoSelectAt = time.Now().Add(2 * time.Minute)
			f.conn = sessions[0].conn.(*bufferConn)
			dispatchAntonCardRequest(t, f, sessions[0], 659, []byte{1})
			dispatchAntonCardRequest(t, f, sessions[0], 660, []byte{1, 0})
			manual := *flow.Party[sessions[0].selectedCharacterID]
			deadline := flow.PartySelectAfter[sessions[0].selectedCharacterID]
			if _, errs := f.service.advanceCurrentAntonRewardFlows(deadline, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if !flow.SquadShownAt.IsZero() || flow.Party[sessions[1].selectedCharacterID] != nil {
				t.Fatal("displaying one owner prematurely selected the absent owner's card")
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if flow.SquadShownAt.IsZero() || !reflect.DeepEqual(manual, *flow.Party[sessions[0].selectedCharacterID]) {
				t.Fatal("unattended fallback missing or manual choice changed")
			}
		})
	}
}

func TestR195ChoiceDeadlineScopeAndClone(t *testing.T) {
	for _, mode := range []byte{1, 2, 3} {
		for _, phase := range []byte{0, 1} {
			flow := &currentAntonRewardFlow{InstanceID: "choice", Mode: mode, Phase: phase, Recipients: []currentAntonRewardRecipient{{CharacterID: 19}}}
			s := &Service{antonRewardFlows: map[string]*currentAntonRewardFlow{currentAntonFlowKey("choice", phase): flow}}
			at := time.Now()
			s.markCurrentRaidPartySelectionShownLocked("choice", phase, 99, at)
			if len(flow.PartySelectAfter) != 0 {
				t.Fatal("foreign owner acquired a choice deadline")
			}
			s.markCurrentRaidPartySelectionShownLocked("choice", phase, 19, at)
			want := mode == 3
			if (len(flow.PartySelectAfter) != 0) != want {
				t.Fatalf("mode%d phase%d deadline scope changed", mode, phase)
			}
			if want {
				copy := cloneCurrentAntonRewardFlow(flow)
				copy.PartySelectAfter[19] = at
				if flow.PartySelectAfter[19] != at.Add(currentRaidDisplayedPartyChoiceWindow) {
					t.Fatal("snapshot aliases live owner deadlines")
				}
			}
		}
	}
}

func r195AdvanceRewardFlow(t *testing.T, service *Service, at time.Time) {
	t.Helper()
	if _, errs := service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
		t.Fatalf("advance at %s: %v", at.Format(time.RFC3339Nano), errs)
	}
}

// A card screen that really reached its socket keeps its full frozen window.
// The unattended deadline is a fallback for owners that never displayed, so an
// earlier fallback must not shorten a late display.
func TestR195DisplayedPartyWindowSurvivesEarlierUnattendedDeadline(t *testing.T) {
	for _, mode := range []string{"fiendwar"} {
		for _, tc := range []struct {
			name   string
			offset time.Duration
		}{
			{"ten_seconds_left", -10 * time.Second},
			{"unattended_deadline_already_passed", -2 * time.Minute},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				f, sessions := r195PublicFiendRewardFixture(t, mode)
				flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
				session := sessions[0]
				f.conn = session.conn.(*bufferConn)
				dispatchAntonCardRequest(t, f, session, 659, []byte{1})
				deadline := flow.PartySelectAfter[session.selectedCharacterID]
				if deadline.IsZero() {
					t.Fatal("displayed card screen did not freeze a choice window")
				}
				flow.AutoSelectAt = deadline.Add(tc.offset)
				if !flow.AutoSelectAt.Before(deadline) {
					t.Fatalf("fixture did not move the fallback before the window: auto=%s shown=%s", flow.AutoSelectAt, deadline)
				}

				r195AdvanceRewardFlow(t, f.service, deadline.Add(-time.Second))
				if card := flow.Party[session.selectedCharacterID]; card != nil && card.Committed {
					t.Fatal("unattended deadline shortened the displayed owner's window")
				}
				r195AdvanceRewardFlow(t, f.service, deadline)
				if card := flow.Party[session.selectedCharacterID]; card == nil || !card.Committed {
					t.Fatal("displayed window did not commit at its own deadline")
				}
			})
		}
	}
}

// A failed card-screen write must not anchor a window: only the owner whose
// kind1 actually reached its socket gets the short deadline, the peer keeps the
// unattended fallback.
func TestR195ChoiceDeadlineSendFailureDoesNotAnchorPeer(t *testing.T) {
	f, sessions := r195PublicFiendRewardFixture(t, "fiendwar")
	flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
	flow.AutoSelectAt = time.Now().Add(time.Hour)
	failed, healthy := sessions[0], sessions[1]

	saved := failed.conn.(*bufferConn)
	failed.conn = &r195DeadlineFailConn{bufferConn: saved}
	frame, err := dnfproto.BuildChannelPacket(659, []byte{1}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.handleGameUpper(failed, frame); err == nil {
		t.Fatal("expected the failed card screen write to surface an error")
	}

	f.conn = healthy.conn.(*bufferConn)
	dispatchAntonCardRequest(t, f, healthy, 659, []byte{1})

	if _, anchored := flow.PartySelectAfter[failed.selectedCharacterID]; anchored {
		t.Fatal("failed delivery anchored its own choice window")
	}
	deadline := flow.PartySelectAfter[healthy.selectedCharacterID]
	if deadline.IsZero() {
		t.Fatal("healthy display did not anchor its window")
	}

	r195AdvanceRewardFlow(t, f.service, deadline)
	if card := flow.Party[healthy.selectedCharacterID]; card == nil || !card.Committed {
		t.Fatal("healthy owner did not commit at its own window")
	}
	if card := flow.Party[failed.selectedCharacterID]; card != nil && card.Committed {
		t.Fatal("failed owner was committed by the peer's window")
	}
}

func TestR195DuplicateCardAcksDoNotExtendChoiceWindow(t *testing.T) {
	f, sessions := r195PublicFiendRewardFixture(t, "fiendwar")
	flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
	flow.AutoSelectAt = time.Now().Add(time.Hour)
	session := sessions[0]
	f.conn = session.conn.(*bufferConn)

	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	deadline := flow.PartySelectAfter[session.selectedCharacterID]
	if deadline.IsZero() {
		t.Fatal("displayed card screen did not freeze a choice window")
	}

	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	if flow.PartySelectAfter[session.selectedCharacterID] != deadline {
		t.Fatal("duplicate movie ACK extended the choice window")
	}
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	if flow.PartySelectAfter[session.selectedCharacterID] != deadline {
		t.Fatal("manual card ACK extended the choice window")
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if flow.PartySelectAfter[session.selectedCharacterID] != deadline {
		t.Fatal("repeat projection extended the choice window")
	}
}

type r195DeadlineFailConn struct{ *bufferConn }

func (c *r195DeadlineFailConn) Write([]byte) (int, error) {
	return 0, errors.New("r195 probe transport failure")
}
