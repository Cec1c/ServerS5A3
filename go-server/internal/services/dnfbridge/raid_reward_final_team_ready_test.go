package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Both owners earned their final clear through the actual PVF volcano/heart
// reducer. Keep weekly subscribers out of setup, then use real authenticated
// result, movie, card and flow projection entry points. The different-group
// case changes the completed roster before reward ownership is frozen; it is
// a reward/display boundary fixture, not a separate-group dungeon simulation.
func finalTeamReadyFixture(t *testing.T, separateGroups bool) (*raidStartGuardFixture, []*gameSession, *currentAntonRewardFlow) {
	t.Helper()
	f, leader, _, at := onlineAntonTerminalFixture(t)
	warmAntonRewardQuestFixture(t, f)
	ensureRaidRewardAccount(t, f, "account-20")
	if separateGroups {
		for i := range f.state.Members {
			if f.state.Members[i].CharID == 20 {
				f.state.Members[i].GroupIndex = 2
			}
		}
		r195RewardParty(t, f.service, 19)
		r195RewardParty(t, f.service, 20)
	} else {
		r195RewardParty(t, f.service, 19, 20)
	}
	cfg, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Battle.AntonFlowConfig = cfg
	if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}
	f.service.mu.Lock()
	online := f.service.gameSessions
	f.service.gameSessions = nil
	f.service.mu.Unlock()
	done, errs := f.service.commitCurrentRaidWeeklyIntents(at)
	f.service.mu.Lock()
	f.service.gameSessions = online
	f.service.mu.Unlock()
	if len(done) != 1 || len(errs) != 0 {
		t.Fatal("real final weekly owner did not commit", done, errs)
	}
	peer := r195MoviePeer(f, leader)
	sessions := []*gameSession{leader, peer}
	for _, p := range sessions {
		p.conn = &publicP14ConcurrentConn{bufferConn: p.conn.(*bufferConn)}
		// This fixture starts from a real completed raid rather than town
		// login. Run the ordinary initial container normalization first: its
		// seeded legacy daily item otherwise acquires its valid 06:00 expiry
		// at the first reward display, unrelated to any reward grant. Keep
		// expiry and metadata exact in every subsequent asset comparison.
		if err := f.service.sendSelectedCurrentContainerListsRefresh(p, "final_team_fixture_initial_containers"); err != nil {
			t.Fatal(err)
		}
		if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		p.conn.(*publicP14ConcurrentConn).take()
	}
	return f, sessions, f.state.Battle.RewardFlow[1]
}

// The 927/748 readiness gate requires a nonempty kind3 after the current 601.
// Inspect actual nested wire bytes, including each owner's visible contents;
// an empty list, a kind0 reply or another owner's item cannot satisfy this.
func finalTeamReadyWire(t *testing.T, stream []byte, partyOwners []uint16, screens int) {
	t.Helper()
	wire := r195ReadFiendRewardWire(t, stream)
	wantOrder := []string{"kind1", "kind3"}
	if screens == 1 {
		wantOrder = append([]string{"state4"}, wantOrder...)
	}
	if !reflect.DeepEqual(wire.sequence, wantOrder) || !reflect.DeepEqual(wire.owners[1], partyOwners) || !reflect.DeepEqual(wire.owners[3], []uint16{19, 20}) {
		t.Fatalf("native party/team order or recipients missing: %+v", wire)
	}
	shown := map[uint16]bool{}
	for item, n := range wire.items {
		if n != 1 || item.flags != 0 || item.quantity == 0 || item.id == 0 {
			t.Fatalf("duplicate, hidden or empty reward descriptor: %+v count=%d", item, n)
		}
		if item.kind == 3 {
			shown[item.owner] = true
		}
	}
	if !shown[19] || !shown[20] {
		t.Fatal("nonempty team rows must contain actual granted contents for both owners")
	}
}

func finalTeamReadyAdvance(t *testing.T, f *raidStartGuardFixture, at time.Time) {
	t.Helper()
	if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
}

// Real-PVF archives remain referenced by service catalog caches for the life of
// a test process. Keep the two cases independently selectable in bounded Linux
// validation rather than retaining two full archives in one subtest process.
func TestRealPVFAntonFinalNoInputEmitsNonemptyTeamAtBoundedDeadline(t *testing.T) {
	finalTeamReadyNoInput(t, false)
}

func TestRealPVFAntonFinalNoInputDifferentGroupsEmitNonemptyTeamAtBoundedDeadline(t *testing.T) {
	finalTeamReadyNoInput(t, true)
}

func finalTeamReadyNoInput(t *testing.T, separate bool) {
	t.Helper()
	f, sessions, flow := finalTeamReadyFixture(t, separate)
	// Neither owner sends 659 or 660. This is the unattended fallback,
	// rather than a test that sets Ready or Committed flags directly.
	finalTeamReadyAdvance(t, f, flow.AutoSelectAt.Add(-time.Nanosecond))
	if len(flow.Party) != 0 || len(flow.Squad) != 0 {
		t.Fatal("fallback committed before its deadline")
	}
	finalTeamReadyAdvance(t, f, flow.AutoSelectAt)
	if flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
		t.Fatal("unattended flow omitted the team stage or skipped its display window")
	}
	for _, p := range sessions {
		if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		owners := []uint16{19, 20}
		if separate {
			owners = []uint16{p.selectedCharacterID}
		}
		finalTeamReadyWire(t, p.conn.(*publicP14ConcurrentConn).take(), owners, 1)
	}
	finish := flow.SquadShownAt.Add(currentAntonMovieWindow(flow))
	finalTeamReadyAdvance(t, f, finish.Add(-time.Nanosecond))
	if !flow.FinishedAt.IsZero() {
		t.Fatal("final team movie window was shortened")
	}
	finalTeamReadyAdvance(t, f, finish)
	if flow.FinishedAt.IsZero() {
		t.Fatal("unattended final display never finished")
	}
	for _, p := range sessions {
		before := r195PublicRewardAssetSnapshot(t, f.service, p)
		finalTeamReadyAdvance(t, f, finish.Add(time.Hour))
		if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		if stream := p.conn.(*publicP14ConcurrentConn).take(); len(stream) != 0 {
			t.Fatalf("finished final projection reopened the screen: %x", stream)
		}
		if after := r195PublicRewardAssetSnapshot(t, f.service, p); !reflect.DeepEqual(before, after) {
			t.Fatal("finished retry changed rewards or durable receipts")
		}
	}
}

func TestRealPVFAntonFinalLateMovieRestoresTeamAfterDisplayDeadline(t *testing.T) {
	f, sessions, flow := finalTeamReadyFixture(t, true)
	finalTeamReadyAdvance(t, f, flow.AutoSelectAt)
	finalTeamReadyAdvance(t, f, flow.SquadShownAt.Add(currentAntonMovieWindow(flow)))
	if flow.FinishedAt.IsZero() {
		t.Fatal("late projection fixture has not finished")
	}
	// Delay both socket projections until after the global display deadline.
	// Each late connection still needs its own result -> party -> team order.
	for _, p := range sessions {
		before := r195PublicRewardAssetSnapshot(t, f.service, p)
		dispatchAntonCardRequest(t, f, p, 659, []byte{1})
		stream := p.conn.(*publicP14ConcurrentConn).take()
		ack, rest := splitGameServerUpperPacket(t, stream)
		if ack.Header.MsgID != 659 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) {
			t.Fatal("late completion did not acknowledge the owned movie")
		}
		finalTeamReadyWire(t, rest, []uint16{p.selectedCharacterID}, 1)
		if p.raidProjection.rewardSquadDisplayShownAt.IsZero() {
			t.Fatal("late final team delivery did not establish its actual socket receipt")
		}
		dispatchAntonCardRequest(t, f, p, 659, []byte{1})
		reply, tail := splitGameServerUpperPacket(t, p.conn.(*publicP14ConcurrentConn).take())
		if reply.Header.MsgID != 659 || !bytes.Equal(reply.Body, []byte{1}) || len(tail) != 0 {
			t.Fatal("duplicate late movie replayed an old mask or the team window")
		}
		if after := r195PublicRewardAssetSnapshot(t, f.service, p); !reflect.DeepEqual(before, after) {
			for slot := range before {
				if !reflect.DeepEqual(before[slot], after[slot]) {
					t.Errorf("owner %d late snapshot slot %d changed: before=%+v after=%+v", p.selectedCharacterID, slot, before[slot], after[slot])
				}
			}
			t.Fatal("late display duplicated assets or durable receipts")
		}
	}
}

func TestRealPVFAntonFinalTeamGrantRetryKeepsPartyAndNativeReadyOrder(t *testing.T) {
	f, sessions, flow := finalTeamReadyFixture(t, false)
	for _, p := range sessions {
		dispatchAntonCardRequest(t, f, p, 659, []byte{1})
		p.conn.(*publicP14ConcurrentConn).take()
	}
	for i, p := range sessions {
		dispatchAntonCardRequest(t, f, p, 660, []byte{1, byte(i)})
		card := flow.Party[p.selectedCharacterID]
		body, err := raid.BuildRewardListBody(1, []raid.RewardDisplayCard{{CharacterID: p.selectedCharacterID, CardType: card.Card, Items: currentAntonPartyDisplayItems(card.Plan)}})
		if err != nil {
			t.Fatal(err)
		}
		publicP14WaitDisplay(t, sessions[1-i], body)
	}
	// The real clock persists completion activity before entering each reward
	// transaction. It is an independent, idempotent accepted-clear event, not a
	// card grant. Prime that legitimate event before the rollback snapshot so
	// every reward item and durable receipt can still be compared exactly.
	for _, owner := range flow.Recipients {
		if err := f.service.persistPrivateActivity(context.Background(), owner.AccountID, strconv.Itoa(int(owner.CharacterID)), 4, flow.AutoSelectAt, flow.CompletedAt); err != nil {
			t.Fatal(err)
		}
	}
	before := make([][]any, len(sessions))
	for i, p := range sessions {
		p.conn.(*publicP14ConcurrentConn).take()
		before[i] = r195PublicRewardAssetSnapshot(t, f.service, p)
	}
	party := cloneCurrentAntonRewardFlow(flow).Party
	original := f.repos.AccountAssets
	f.repos.AccountAssets = newAntonCardFailLateAssets(original)
	at := flow.AutoSelectAt
	_, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll)
	f.repos.AccountAssets = original
	if len(errs) != 1 || !flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() || !flow.NextAttempt.After(at) {
		t.Fatal("failed team transaction advanced the display instead of deferring", errs)
	}
	for i, p := range sessions {
		if after := r195PublicRewardAssetSnapshot(t, f.service, p); !reflect.DeepEqual(before[i], after) {
			for slot := range before[i] {
				if !reflect.DeepEqual(before[i][slot], after[slot]) {
					t.Errorf("owner %d changed snapshot slot %d: before=%+v after=%+v", p.selectedCharacterID, slot, before[i][slot], after[slot])
				}
			}
			t.Fatal("rolled-back team transaction changed assets or receipts")
		}
	}
	finalTeamReadyAdvance(t, f, flow.NextAttempt.Add(-time.Nanosecond))
	if !flow.SquadShownAt.IsZero() {
		t.Fatal("team grant ignored its retry backoff")
	}
	finalTeamReadyAdvance(t, f, flow.NextAttempt)
	if flow.SquadShownAt.IsZero() || !reflect.DeepEqual(party, flow.Party) {
		t.Fatal("team retry never became ready or changed committed manual party cards")
	}
	for _, p := range sessions {
		if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		finalTeamReadyWire(t, p.conn.(*publicP14ConcurrentConn).take(), []uint16{19, 20}, 0)
	}
}
