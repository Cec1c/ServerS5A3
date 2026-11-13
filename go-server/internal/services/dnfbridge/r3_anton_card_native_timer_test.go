package dnfbridge

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

func TestR3AntonCardChoiceDeadlineMatchesNativePanelTimer(t *testing.T) {
	if got, want := currentRaidNativeCardChoiceWindow, 15*time.Second; got != want {
		t.Fatalf("native card-choice timer=%s want=%s", got, want)
	}

	completed := time.Unix(1_800_000_000, 0)
	movie := 20 * time.Second
	phase0 := &currentAntonRewardFlow{
		InstanceID:   "r3-card-timer",
		Mode:         1,
		Phase:        0,
		CompletedAt:  completed,
		PartyShownAt: completed.Add(30 * time.Second),
		AutoSelectAt: completed.Add(40 * time.Second),
	}
	if got, want := currentAntonPartySelectAt(phase0, movie), phase0.PartyShownAt.Add(15*time.Second); !got.Equal(want) {
		t.Fatalf("phase0 displayed deadline=%s want native panel=%s", got, want)
	}

	final := &currentAntonRewardFlow{
		InstanceID:   "r3-card-timer",
		Mode:         1,
		Phase:        1,
		CompletedAt:  completed,
		AutoSelectAt: completed.Add(40 * time.Second),
		Recipients:   []currentAntonRewardRecipient{{CharacterID: 19}},
	}
	service := &Service{antonRewardFlows: map[string]*currentAntonRewardFlow{
		currentAntonFlowKey(final.InstanceID, final.Phase): final,
	}}
	shown := completed.Add(31 * time.Second)
	service.markCurrentRaidPartySelectionShownLocked(final.InstanceID, final.Phase, 19, shown)
	if got, want := final.PartySelectAfter[19], shown.Add(15*time.Second); !got.Equal(want) {
		t.Fatalf("final displayed deadline=%s want native panel=%s", got, want)
	}
	service.markCurrentRaidPartySelectionShownLocked(final.InstanceID, final.Phase, 19, shown.Add(5*time.Second))
	if got, want := final.PartySelectAfter[19], shown.Add(15*time.Second); !got.Equal(want) {
		t.Fatalf("duplicate display extended deadline to %s want stable=%s", got, want)
	}
}

// Exercise the accepted PVF phase0 reducer and actual 601/659/600 transport.
// Two independent groups must keep their own party rows and share the team
// list only after the post-commit stage4 card timer, without a second state4.
func r3AntonPhase0DisplayedFixture(t *testing.T) (*raidStartGuardFixture, []*gameSession, *currentAntonRewardFlow, time.Time) {
	t.Helper()
	f, sessions, _ := phase0RewardFlowWithGroups(t, []byte{1, 2})
	ensureRaidRewardAccount(t, f, "account-20")
	flow := f.state.Battle.RewardFlow[0]
	flow.PartyShownAt = time.Time{}
	flow.PartySelectAfter = nil
	for _, session := range sessions {
		session.conn = &bufferConn{}
		r195RewardParty(t, f.service, session.selectedCharacterID)
		if err := f.service.sendSelectedCurrentContainerListsRefresh(session, "r3_card_fixture"); err != nil {
			t.Fatal(err)
		}
		if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		session.conn.(*bufferConn).write.Reset()
		f.conn = session.conn.(*bufferConn)
		dispatchAntonCardRequest(t, f, session, 659, []byte{1})
		phase0AllCardsFaceDown(t, antonNativeCardBodies(t, f)[1])
		session.conn.(*bufferConn).write.Reset()
	}
	if flow.PartyShownAt.IsZero() || len(flow.Party) != 0 || flow.PartyManual {
		t.Fatal("natural movie completion did not start an unselected party screen")
	}
	var deadline time.Time
	for _, personal := range flow.PartySelectAfter {
		if personal.After(deadline) {
			deadline = personal
		}
	}
	if !deadline.After(flow.AutoSelectAt) {
		t.Fatal("fixture must model a natural movie completing after the fallback")
	}
	return f, sessions, flow, deadline
}

func r3WantCommittedCardWire(t *testing.T, got r195FiendRewardWire, flow *currentAntonRewardFlow, kind byte, ids []uint16, event string) {
	t.Helper()
	if !reflect.DeepEqual(got.sequence, []string{event}) || !reflect.DeepEqual(got.owners, map[byte][]uint16{kind: ids}) {
		t.Fatalf("card deadline emitted incorrect stages/owners: sequence=%v owners=%v", got.sequence, got.owners)
	}
	cards := flow.Party
	if kind == 3 {
		cards = flow.Squad
	}
	want := map[r195FiendRewardDescriptor]int{}
	for _, id := range ids {
		card := cards[id]
		if card == nil || !card.Committed || len(card.Plan.Rewards) == 0 {
			t.Fatal("projection has no committed nonempty plan", id)
		}
		for _, reward := range card.Plan.Rewards {
			count := reward.Count
			if reward.Definition.ItemID == 10304529 && card.Plan.FateGranted != nil {
				count = *card.Plan.FateGranted
			}
			if count != 0 {
				want[r195FiendRewardDescriptor{kind: kind, owner: id, id: reward.Definition.ItemID, quantity: uint16(count)}]++
			}
		}
	}
	// Pool choice Flags are not the NOTI600 hide-item byte. Actual granted
	// descriptors must carry zero there, including special reward pools.
	if !reflect.DeepEqual(got.items, want) {
		t.Fatalf("committed descriptor mismatch: got=%+v want=%+v", got.items, want)
	}
}

func TestRealPVFR3AntonPhase0TeamProjectionAtNativeCardDeadline(t *testing.T) {
	f, sessions, flow, selectedAt := r3AntonPhase0DisplayedFixture(t)
	firstAt := selectedAt
	for _, personal := range flow.PartySelectAfter {
		if personal.Before(firstAt) {
			firstAt = personal
		}
	}
	r195AdvanceRewardFlow(t, f.service, firstAt.Add(-time.Nanosecond))
	if len(flow.Party) != 0 || len(flow.Squad) != 0 {
		t.Fatal("card rewards committed before the native choice deadline")
	}
	r195AdvanceRewardFlow(t, f.service, selectedAt)
	if !flow.PartyCommittedAt.Equal(selectedAt) || len(flow.Squad) != 0 || flow.PartyManual {
		t.Fatal("automatic party commit skipped its stage4 display")
	}
	for _, session := range sessions {
		if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		conn := session.conn.(*bufferConn)
		r3WantCommittedCardWire(t, r195ReadFiendRewardWire(t, conn.write.Bytes()), flow,
			1, []uint16{session.selectedCharacterID}, "kind1")
		conn.write.Reset()
	}
	teamAt := selectedAt.Add(15 * time.Second)
	r195AdvanceRewardFlow(t, f.service, teamAt.Add(-time.Nanosecond))
	if len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() {
		t.Fatal("team projection crossed the native party stage4 deadline")
	}
	r195AdvanceRewardFlow(t, f.service, teamAt)
	if !flow.SquadShownAt.Equal(teamAt) || !flow.FinishedAt.IsZero() {
		t.Fatal("team list still waits for the old 20-second movie threshold")
	}
	for _, session := range sessions {
		if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		conn := session.conn.(*bufferConn)
		r3WantCommittedCardWire(t, r195ReadFiendRewardWire(t, conn.write.Bytes()), flow,
			3, []uint16{19, 20}, "kind3")
		conn.write.Reset()
		id := strconv.Itoa(int(session.selectedCharacterID))
		if antonGrantedItemCount(t, f, id, 10304529) != 30 || antonGrantedItemCount(t, f, id, 10093974) != 19 {
			t.Fatal("deadline changed the actual PVF rewards", id)
		}
	}
	// Team rendering has a separate native state machine. Its completion is
	// not proved to be a 15-second timer by party subpanel 0190C970.
	finishedAt := teamAt.Add(currentAntonMovieWindow(flow))
	r195AdvanceRewardFlow(t, f.service, teamAt.Add(15*time.Second))
	if !flow.FinishedAt.IsZero() {
		t.Fatal("party timer prematurely completed the independent team display")
	}
	r195AdvanceRewardFlow(t, f.service, finishedAt.Add(-time.Nanosecond))
	if !flow.FinishedAt.IsZero() {
		t.Fatal("team completion policy changed")
	}
	r195AdvanceRewardFlow(t, f.service, finishedAt)
	if !flow.FinishedAt.Equal(finishedAt) || !flow.BreakUntil.Equal(finishedAt.Add(600*time.Second)) {
		t.Fatal("team completion did not retain its configured break")
	}
	frozen := cloneCurrentAntonRewardFlow(flow)
	for _, session := range sessions {
		if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		conn := session.conn.(*bufferConn)
		wire := r195ReadFiendRewardWire(t, conn.write.Bytes())
		if !reflect.DeepEqual(wire.sequence, []string{"state5"}) {
			t.Fatalf("completion replayed card lists: %v", wire.sequence)
		}
		conn.write.Reset()
		before := r195PublicRewardAssetSnapshot(t, f.service, session)
		r195AdvanceRewardFlow(t, f.service, finishedAt.Add(time.Hour))
		if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil || conn.write.Len() != 0 {
			t.Fatal("finished retry replayed the display", err)
		}
		if after := r195PublicRewardAssetSnapshot(t, f.service, session); !reflect.DeepEqual(before, after) {
			t.Fatal("finished retry changed assets or receipts")
		}
	}
	if !reflect.DeepEqual(frozen.Party, flow.Party) || !reflect.DeepEqual(frozen.Squad, flow.Squad) {
		t.Fatal("repeated card deadlines changed committed plans")
	}
}

func TestRealPVFR3AntonPhase0TeamDeadlineRollbackRetainsFrozenPlan(t *testing.T) {
	f, _, flow, selectedAt := r3AntonPhase0DisplayedFixture(t)
	r195AdvanceRewardFlow(t, f.service, selectedAt)
	party := cloneCurrentAntonRewardFlow(flow).Party
	teamAt := selectedAt.Add(15 * time.Second)
	original := f.repos.AccountAssets
	f.repos.AccountAssets = newAntonCardFailLateAssets(original)
	_, errs := f.service.advanceCurrentAntonRewardFlows(teamAt, zeroAntonRoll)
	f.repos.AccountAssets = original
	if len(errs) != 2 || len(flow.Squad) != 2 || flow.Squad[19].Committed || !flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
		t.Fatal("failed native-deadline grant advanced the team display", errs)
	}
	frozen := cloneCurrentAntonRewardPlan(flow.Squad[19].Plan)
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304529) != 30 || antonGrantedItemCount(t, f, id, 10093974) != 0 {
			t.Fatal("rolled-back team grant changed party rewards or leaked team assets")
		}
	}
	r195AdvanceRewardFlow(t, f.service, flow.OwnerNextAttempt[19].Add(-time.Nanosecond))
	if flow.Squad[19].Committed || !flow.SquadShownAt.IsZero() {
		t.Fatal("native card deadline bypassed grant retry backoff")
	}
	retryAt := flow.OwnerNextAttempt[19]
	r195AdvanceRewardFlow(t, f.service, retryAt)
	if !flow.SquadShownAt.Equal(retryAt) || !reflect.DeepEqual(frozen, flow.Squad[19].Plan) || !reflect.DeepEqual(party, flow.Party) {
		t.Fatal("team retry rerolled frozen rewards or lost the manual/automatic party state")
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10093974) != 19 {
			t.Fatal("team retry did not grant exactly one actual PVF reward")
		}
	}
}

func TestRealPVFR3AntonFinalTeamDataDoesNotWaitForCompletionWindow(t *testing.T) {
	f, sessions, flow := finalTeamReadyFixture(t, true)
	var selectedAt time.Time
	for _, session := range sessions {
		dispatchAntonCardRequest(t, f, session, 659, []byte{1})
		session.conn.(*publicP14ConcurrentConn).take()
		deadline := flow.PartySelectAfter[session.selectedCharacterID]
		if deadline.After(selectedAt) {
			selectedAt = deadline
		}
	}
	if selectedAt.IsZero() {
		t.Fatal("final natural movie did not freeze a card deadline")
	}
	finalTeamReadyAdvance(t, f, selectedAt)
	if !flow.SquadShownAt.Equal(selectedAt) || !flow.FinishedAt.IsZero() {
		t.Fatal("final team data waits for a separate movie completion deadline")
	}
	for _, session := range sessions {
		if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		finalTeamReadyWire(t, session.conn.(*publicP14ConcurrentConn).take(), []uint16{session.selectedCharacterID}, 0)
	}
	finalTeamReadyAdvance(t, f, selectedAt.Add(15*time.Second))
	if !flow.FinishedAt.IsZero() {
		t.Fatal("party countdown was incorrectly applied to final completion")
	}
	finalTeamReadyAdvance(t, f, selectedAt.Add(currentAntonMovieWindow(flow)))
	if flow.FinishedAt.IsZero() {
		t.Fatal("final completion lost its existing display policy")
	}
}

func TestR3AntonCardChoiceKeepsMovieFallbackWithoutDisplay(t *testing.T) {
	configuration := raid.AntonFlowConfiguration{MovieEndSeconds: [4]uint32{10, 20, 5, 20}}
	completed := time.Unix(1_800_000_000, 0)
	flow := &currentAntonRewardFlow{
		InstanceID:    "r3-card-movie-fallback",
		Mode:          1,
		Phase:         0,
		CompletedAt:   completed,
		Configuration: configuration,
		AutoSelectAt:  completed.Add(40 * time.Second),
	}
	if got, want := currentAntonMovieWindow(flow), 20*time.Second; got != want {
		t.Fatalf("movie window=%s want configured maximum=%s", got, want)
	}
	if got, want := currentAntonPartySelectAt(flow, currentAntonMovieWindow(flow)), flow.AutoSelectAt.Add(20*time.Second); !got.Equal(want) {
		t.Fatalf("undisplayed fallback=%s want movie-bound fallback=%s", got, want)
	}
}
