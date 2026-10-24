package dnfbridge

import (
	"testing"
	"time"
)

// A completed local party must not enter the empty squad page while another
// reward group is still choosing. Global phase progression keeps its barrier.
func TestR195LukeLocalPartyCompletionDoesNotWaitForOtherGroup(t *testing.T) {
	f, session, flow := r195LukeCompletedPartyFixture(t)
	if flow.Recipients[0].Group == flow.Recipients[1].Group {
		t.Fatal("fixture must retain distinct groups")
	}
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	if flow.Party[19] == nil || !flow.Party[19].Committed || flow.Party[20] != nil && flow.Party[20].Committed {
		t.Fatal("expected only local party committed")
	}
	// Both notices must be sent by the real 660 handler, before a timer tick.
	r195LukeWantCompletionWire(t, r195LukeCompletionWire(t, f.conn.write.Bytes()), "kind1", "empty3")
	if !flow.FinishedAt.IsZero() || currentLukePhase0PartyOnlyRewardSettled(flow) || len(flow.Squad) != 0 {
		t.Fatal("local UI completion bypassed global reward barrier")
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("local completion repeated", err)
	}
}

func TestR195LukeAllPartyCommittedHasNoPhantomSquadMovieDelay(t *testing.T) {
	f, _, flow := r195LukeCompletedPartyFixture(t)
	settledAt := flow.AutoSelectAt
	if _, errs := f.service.advanceCurrentAntonRewardFlows(settledAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	r195LukeAssertNoSquadAssets(t, f, flow)
	if !flow.FinishedAt.Equal(settledAt) || !flow.BreakUntil.Equal(settledAt.Add(time.Duration(flow.Configuration.PhaseBreakSeconds)*time.Second)) {
		t.Fatalf("party-only phase waits for nonexistent squad movie: finished=%v break=%v committed=%v", flow.FinishedAt, flow.BreakUntil, settledAt)
	}
}

func TestR195LukeLocalCompletionStillWaitsForVisiblePartyMember(t *testing.T) {
	f, session, flow := r195LukeCompletedPartyFixture(t)
	r195RewardParty(t, f.service, 19, 20)
	f.state.Members[1].GroupIndex = f.state.Members[0].GroupIndex
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	// The full visible roster can refresh, but the unchosen member must
	// retain their card-selection window and the group cannot finish yet.
	r195LukeWantCompletionWire(t, r195LukeCompletionWire(t, f.conn.write.Bytes()), "kind1")
	if flow.Party[20] != nil && flow.Party[20].Committed || !flow.FinishedAt.IsZero() {
		t.Fatal("local completion invented another member's selection")
	}
}

func TestR195LukeLocalCompletionRequiresQualifiedOwner(t *testing.T) {
	f := &currentAntonRewardFlow{Mode: 2, Recipients: []currentAntonRewardRecipient{{CharacterID: 19}, {CharacterID: 20}}, Party: map[uint16]*currentAntonRewardCard{19: {Committed: true}}}
	for _, tc := range []struct {
		name    string
		owner   uint16
		visible map[uint16]bool
		want    bool
	}{
		{"one completed local group", 19, map[uint16]bool{19: true}, true},
		{"uncommitted visible peer", 19, map[uint16]bool{19: true, 20: true}, false},
		{"spectator with another owner's reward", 21, map[uint16]bool{19: true, 21: true}, false},
		{"owner absent from visible group", 19, map[uint16]bool{20: true}, false},
		{"empty group", 19, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := currentLukeLocalPartyDisplayCommitted(f, tc.owner, tc.visible); got != tc.want {
				t.Fatalf("local completion=%t want=%t", got, tc.want)
			}
		})
	}
}
