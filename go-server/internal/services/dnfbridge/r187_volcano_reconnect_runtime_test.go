package dnfbridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Phase-two progression is an explicit resource-VM fixture. Dungeon selection,
// loading, socket replacement and resubscription below use bridge handlers.
// It does not emulate native monster damage or claim GUI acceptance.
func TestRealPVFR187VolcanoRuntimeReconnectRetainsBarrierState(t *testing.T) {
	f, owners, conns := r160AntonLegalFixture(t)
	s := f.service
	at := time.Now()
	program, err := raid.LoadAntonProgram(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := program.BeginPhase(1, at, 19703)
	if err != nil {
		t.Fatal(err)
	}
	s.raidMu.Lock()
	b := f.state.Battle
	b.Phase, b.Anton, b.Dungeons = 1, progress, progress.Dungeons()
	b.EnterAfter, b.Deadline = at, at.Add(time.Hour)
	b.ProgressVersion++
	phaseState := cloneRuntimeRaidState(*f.state)
	s.raidMu.Unlock()
	for _, p := range owners {
		deadlineAuditV10Event(t, s, p, func() error { return s.sendCurrentRaidProgressProjection(p, phaseState, false, true) })
	}
	r160EnterRaidGroup(t, f, owners, 0, 219)
	r160WaitRaidProjection(t, f, owners)
	r187WaitVolcanoLoading(t, s, owners[:2])
	old := owners[1]
	oldRun := old.dungeon.runtime
	if oldRun == nil || oldRun.Dungeon.ID != 219 || oldRun.Room == nil || oldRun.raidEntry == nil || oldRun.raidEntry.RunID == "" {
		t.Fatal("actual volcano runtime missing")
	}
	s.shutdownGameSessionEvents(old, false)
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.handleDungeonGiveupGame(owners[0], nil) })
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.sendEnterSelectDungeonState(owners[0], "upper_15", false, true) })

	// The barrier breaks while this client is offline. Accepted domain events
	// are explicit fixtures; they do not pretend to be public energy op39 input.
	s.raidMu.Lock()
	for n := 0; n < 4; n++ {
		next, replay, e := b.Anton.ApplyBarrierDamage(fmt.Sprintf("r187-energy-%d", n), raid.AntonLargeTransmission, 218, b.Anton.Epoch(), at)
		if e != nil || replay {
			s.raidMu.Unlock()
			t.Fatalf("damage fixture: %v/%t", e, replay)
		}
		b.Anton = next
	}
	b.Dungeons = b.Anton.Dungeons()
	b.ProgressVersion++
	broken := cloneRuntimeRaidState(*f.state)
	s.raidMu.Unlock()
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.sendCurrentRaidProgressProjection(owners[0], broken, false, false) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fresh := f.session()
	fresh.conn = &deadlineAuditV10Conn{}
	fresh.ctx = ctx
	fresh.connID = "r187-volcano-replacement"
	fresh.accountID, fresh.channel = old.accountID, old.channel
	fresh.loginPacketSeen, fresh.accountFromToken = true, true
	s.bindGameSessionCharacter(fresh, old.selectedCharacterID)
	if err := s.startGameSessionEvents(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.shutdownGameSessionEvents(fresh, false) })
	owners[1], conns[1] = fresh, fresh.conn.(*deadlineAuditV10Conn)
	deadlineAuditV10NormalTown(t, f, fresh)
	conns[1].reset()
	deadlineAuditV10Event(t, s, fresh, func() error { return s.handleGameCommand(fresh, 1, 668, rejoinContextRequest(f.state.RaidKey)) })
	acks := 0
	for _, p := range r160FindFrames(t, conns[1].bytes()) {
		if p.Header.Classification == 1 && p.Header.MsgID == 668 {
			if len(p.Body) != 1 || p.Body[0] != 1 {
				t.Fatalf("rejoin rejected: %x", p.Body)
			}
			acks++
		}
	}
	if acks != 1 {
		t.Fatalf("rejoin success ACK count=%d", acks)
	}
	// Decode an ordinary client state store: reset clears previous values;
	// subsequent symbols replace by ID. No native rendering/damage is simulated.
	stores := []map[uint32]uint32{{}, {}}
	consume := func(i int) {
		for _, p := range r160FindFrames(t, conns[i].bytes()) {
			if p.Header.Classification != 0 || p.Header.MsgID != 583 {
				continue
			}
			for _, v := range antonProjectionWrites(t, p.Body) {
				if v.ID == 0 {
					stores[i] = map[uint32]uint32{}
				} else {
					stores[i][v.ID] = v.Value
				}
			}
		}
		conns[i].reset()
	}
	consume(0)
	consume(1)
	check := func(wantHP, wantOpen uint32) {
		t.Helper()
		for i := 0; i < 2; i++ {
			hp, hasHP := stores[i][110]
			open, hasOpen := stores[i][111]
			if !hasHP || !hasOpen || hp != wantHP || open != wantOpen {
				t.Fatalf("receiver%d barrier=%d open=%d want=%d/%d", i, stores[i][110], stores[i][111], wantHP, wantOpen)
			}
		}
	}
	check(0, 1)
	state, result, ok := s.createOrJoinManagedRuntimeParty(fresh, owners[0])
	if !ok && result.Reason != "already_member" || state.PartyID <= 0 {
		t.Fatalf("regroup: %+v", result)
	}
	r160EnterRaidGroup(t, f, owners, 0, 219)
	r160WaitRaidProjection(t, f, owners)
	r187WaitVolcanoLoading(t, s, owners[:2])
	for i, p := range owners[:2] {
		r := p.dungeon.runtime
		if r == nil || r.Dungeon.ID != 219 || r.raidEntry == nil || r.raidEntry.Phase != 1 || r.Room == nil {
			t.Fatal("replacement did not enter actual volcano")
		}
		var frames []upperFrame
		for _, frame := range r160FindFrames(t, conns[i].bytes()) {
			frames = append(frames, upperFrame{msgID: frame.Header.MsgID, body: frame.Body})
		}
		a, b, c := r160UpperPeerActorCounts(frames, owners[1-i].selectedCharacterID)
		if a == 0 || b == 0 || c == 0 {
			t.Fatalf("volcano receiver%d missing actors=%d/%d/%d", i, a, b, c)
		}
		consume(i)
	}
	check(0, 1)
	left, right := owners[0].dungeon.runtime, fresh.dungeon.runtime
	if left.partyInstance == nil || left.partyInstance != right.partyInstance || left.raidEntry.RunID != right.raidEntry.RunID || right == oldRun {
		t.Fatal("volcano shared run mismatch")
	}
	s.releaseCurrentRaidParticipationSession(old)
	s.releasePartyDungeonInstance(oldRun, old.selectedCharacterID, "r187_late_volcano_socket_cleanup")
	s.raidMu.Lock()
	entry := f.state.Battle.Participation[fresh.selectedCharacterID]
	s.raidMu.Unlock()
	if entry.Session != fresh || entry.Runtime != right || !dungeonPartyInstanceHasMember(left.partyInstance, fresh.selectedCharacterID) {
		t.Fatal("retired volcano socket removed replacement participation or shared instance")
	}
	for _, step := range []struct {
		seconds  int
		hp, open uint32
	}{{1, 17, 1}, {700, 10000, 0}} {
		s.raidMu.Lock()
		next, e := b.Anton.Tick(at.Add(time.Duration(step.seconds) * time.Second))
		if e != nil {
			s.raidMu.Unlock()
			t.Fatal(e)
		}
		b.Anton, b.Dungeons = next, next.Dungeons()
		b.ProgressVersion++
		snapshot := cloneRuntimeRaidState(*f.state)
		s.raidMu.Unlock()
		for i, p := range owners[:2] {
			deadlineAuditV10Event(t, s, p, func() error { return s.sendCurrentRaidProgressProjection(p, snapshot, false, false) })
			consume(i)
		}
		check(step.hp, step.open)
	}
	for i, p := range owners[:2] {
		p.dungeon.mu.Lock()
		v := s.resolveCurrentDungeonPartyHitOwner(p, p.dungeon.runtime, owners[1-i].selectedCharacterID, dungeonPartyHitOwnerParty)
		p.dungeon.mu.Unlock()
		if !v.accepted || !v.sameInstance {
			t.Fatalf("volcano peer ownership failed: %+v", v)
		}
	}
}

// Participation is recorded before the loading tail completes. Wait for the
// actual scene state gates on their owning event loops before inspecting wire.
func r187WaitVolcanoLoading(t *testing.T, s *Service, owners []*gameSession) {
	t.Helper()
	for _, p := range owners {
		ready := false
		until := time.Now().Add(createWriteTimeout)
		for time.Now().Before(until) {
			deadlineAuditV10Event(t, s, p, func() error {
				ready = p.currentFinishLoadingStateSent && p.currentFinishLoadingCompletionSent && p.postFinishLoadingPlayerStateSent
				return nil
			})
			if ready {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if !ready {
			t.Fatal("volcano loading tail did not complete")
		}
	}
}
