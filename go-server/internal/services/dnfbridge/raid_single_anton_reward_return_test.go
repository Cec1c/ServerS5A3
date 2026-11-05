package dnfbridge

import (
	"context"
	"reflect"
	"testing"
	"time"

	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func singleAntonRewardReturnFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, *currentAntonRewardFlow) {
	t.Helper()
	f, session, at := singleAntonCompletedRewardFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	r := session.dungeon.runtime
	// The reward fixture reduces completed maps directly. Supply the frozen
	// participation row normally captured by the successful entry tail.
	e := r.raidEntry
	f.state.Battle.Participation = map[uint16]currentRaidParticipation{19: {
		RunID: e.RunID, DungeonID: uint32(r.Dungeon.ID), Phase: e.Phase,
		Attempt: e.Attempt, Generation: session.characterGeneration, Runtime: r, Session: session,
	}}
	ctx := context.Background()
	ch, found, err := f.repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatal(err, found)
	}
	ch.Stats = map[string]int64{"town_id": 19, "area_id": 1, "pos_x": 819, "pos_y": 172, "direction": 0, "area_state": 0}
	if err = f.repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	r.Character = ch
	r.townReturnOrigin = currentTownPositionSnapshot{CharacterID: 19, TownID: 19, AreaID: 1, PositionX: 819, PositionY: 172, PositionValid: true}
	f.service.townCatalog, err = dnftown.Load(ctx, f.service.initialEquipmentArchive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if err = f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err = f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	return f, session, flow
}

func elapseSingleAntonDisplayForTest(p *gameSession) {
	p.raidProjection.mu.Lock()
	p.raidProjection.rewardDisplayShownAt = time.Now().Add(-time.Minute)
	p.raidProjection.mu.Unlock()
}

func TestRealPVFSingleAntonFinishedCardsReturnToTownOnce(t *testing.T) {
	f, session, flow := singleAntonRewardReturnFixture(t)
	r := session.dungeon.runtime
	var err error
	before := r202SingleAssetCounts(t, f, []uint32{10304529, 10093974})
	f.conn.write.Reset()
	if err = f.service.sendCurrentSingleAntonRewardReturn(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("returned before display finished", err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if flow.FinishedAt.IsZero() {
		t.Fatal("display not finished")
	}
	if err = f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	elapseSingleAntonDisplayForTest(session)
	if err = f.service.sendCurrentSingleAntonRewardReturn(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	counts := map[uint16]int{}
	for _, p := range splitAllGameServerUpperPackets(t, f.conn.write.Bytes()) {
		counts[p.Header.MsgID]++
	}
	if counts[24] != 1 || !r.townReturnOp24Sent || session.dungeon.runtime != nil {
		t.Fatalf("finished raid stranded: packets=%v return=%v runtime=%p", counts, r.townReturnOp24Sent, session.dungeon.runtime)
	}
	if counts[600] != 0 || counts[587] != 0 || counts[34] != 0 || counts[35] != 0 || counts[115] != 0 {
		t.Fatal("return replayed result/cards", counts)
	}
	if !reflect.DeepEqual(before, r202SingleAssetCounts(t, f, []uint32{10304529, 10093974})) {
		t.Fatal("return changed rewards")
	}
	f.conn.write.Reset()
	if err = f.service.sendCurrentSingleAntonRewardReturn(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("return replay", err, f.conn.write.Len())
	}
}

func TestRealPVFSingleAntonCompletionQueueReturnsAfterFinalDisplay(t *testing.T) {
	f, session, flow := singleAntonRewardReturnFixture(t)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	elapseSingleAntonDisplayForTest(session)
	// This completed-result fixture does not model first-entry HP/stat data.
	// Mark the unchanged progress page as already delivered, as it is before
	// the production reward-completion callback runs on a resident session.
	session.raidProjection.initialized = true
	session.raidProjection.identity = session.raidProjection.resultIdentity
	session.raidProjection.version = f.state.Battle.ProgressVersion
	f.conn.write.Reset()
	// Instrumented runs also execute the real actor reconstruction here.
	// Allow its cold resource parsing time without changing production budgets.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.service.startGameSessionEvents(ctx, session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.service.stopGameSessionEvents(context.Background(), session); err != nil {
			t.Error(err)
		}
	})
	// Exercise the production completion queue rather than calling the new
	// return helper directly. Read observations on the same session owner.
	f.service.queueCurrentRaidDungeonProgress(cloneRuntimeRaidState(*f.state))
	for ctx.Err() == nil {
		returned := false
		var wire []byte
		err := f.service.callGameSession(ctx, session, "single-anton-return-observation", func() error {
			returned = session.dungeon.runtime == nil
			if returned {
				wire = append([]byte(nil), f.conn.write.Bytes()...)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if returned {
			var transitions int
			for _, packet := range splitAllGameServerUpperPackets(t, wire) {
				if packet.Header.MsgID == 24 {
					transitions++
				}
			}
			if transitions != 1 {
				t.Fatalf("completion queue returned with %d town transitions", transitions)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("completion queue left the player in the final dungeon after the card display")
}

func TestRealPVFSingleAntonRewardReturnRejectsStaleAndUncommittedOwners(t *testing.T) {
	for _, fault := range []string{"unfinished", "weekly", "party", "squad", "display", "result", "transport", "instance", "phase", "account", "participant", "run", "generation", "runtime", "intermediate", "team", "luke"} {
		t.Run(fault, func(t *testing.T) {
			f, p, flow := singleAntonRewardReturnFixture(t)
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			elapseSingleAntonDisplayForTest(p)
			r, b := p.dungeon.runtime, f.state.Battle
			captured := cloneRuntimeRaidState(*f.state)
			assets := r202SingleAssetCounts(t, f, []uint32{10304529, 10093974})
			switch fault {
			case "unfinished":
				flow.FinishedAt = time.Time{}
				captured = cloneRuntimeRaidState(*f.state)
			case "weekly":
				f.service.raidWeeklyIntents[b.InstanceID].Committed = false
			case "party":
				flow.Party[19].Committed = false
				captured = cloneRuntimeRaidState(*f.state)
			case "squad":
				flow.Squad[19].Committed = false
				captured = cloneRuntimeRaidState(*f.state)
			case "display":
				p.raidProjection.rewardDisplaySignature = [32]byte{}
			case "result":
				p.raidProjection.resultSent = false
			case "transport":
				p.raidProjection.failed = true
			case "instance":
				b.InstanceID = "replaced"
			case "phase":
				b.Phase = 1
			case "account":
				b.MemberAccounts[19] = "other"
			case "participant":
				r.raidEntry.Participants = []uint16{20}
			case "run":
				r.raidEntry.RunID = "other"
			case "generation":
				p.characterGeneration++
			case "runtime":
				delete(b.Participation, 19)
			case "intermediate":
				r.Dungeon.ID = int64(b.SingleSequence[0])
			case "team":
				b.Single = false
				captured = cloneRuntimeRaidState(*f.state)
			case "luke":
				b.Mode = 2
				captured = cloneRuntimeRaidState(*f.state)
			}
			f.conn.write.Reset()
			if err := f.service.sendCurrentSingleAntonRewardReturn(p, captured); err != nil {
				t.Fatal(err)
			}
			if f.conn.write.Len() != 0 || r.townReturnOp24Sent || p.dungeon.runtime != r {
				t.Fatal("foreign or unfinished owner returned", fault)
			}
			if !reflect.DeepEqual(assets, r202SingleAssetCounts(t, f, []uint32{10304529, 10093974})) {
				t.Fatal("return guard changed assets")
			}
		})
	}
}

func TestRealPVFSingleAntonReturnPreparationFailureRetainsOwnedRuntime(t *testing.T) {
	f, p, flow := singleAntonRewardReturnFixture(t)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	elapseSingleAntonDisplayForTest(p)
	r := p.dungeon.runtime
	origin := r.townReturnOrigin
	r.townReturnOrigin = currentTownPositionSnapshot{}
	f.conn.write.Reset()
	if err := f.service.sendCurrentSingleAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err == nil {
		t.Fatal("missing frozen origin accepted")
	}
	if r.townReturnPending || r.townReturnOp24Sent || p.dungeon.runtime != r || f.conn.write.Len() != 0 {
		t.Fatal("failed preparation detached or changed runtime")
	}
	r.townReturnOrigin = origin
	if err := f.service.sendCurrentSingleAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil || !r.townReturnOp24Sent {
		t.Fatal("owned retry failed", err)
	}
}

func TestRealPVFSingleAntonLateFinalCardsKeepTheirDisplayWindow(t *testing.T) {
	f, p, flow := singleAntonRewardReturnFixture(t)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(time.Minute), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	// Model the same owned result whose first squad display was delayed until
	// after the authoritative reward flow finished. No asset is granted here.
	p.raidProjection.rewardFlowSignature = [32]byte{}
	p.raidProjection.rewardDisplaySignature = [32]byte{}
	if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentSingleAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	timer := p.raidProjection.singleAntonReturnTimer
	if timer == nil || f.conn.write.Len() != 0 || p.dungeon.runtime == nil {
		t.Fatal("late cards were skipped by immediate return")
	}
	t.Cleanup(func() { timer.Stop() })
	if err := f.service.sendCurrentSingleAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil || p.raidProjection.singleAntonReturnTimer != timer {
		t.Fatal("duplicate return timer", err)
	}
	elapseSingleAntonDisplayForTest(p)
	if err := f.service.sendCurrentSingleAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil || p.dungeon.runtime != nil {
		t.Fatal("late display never returned", err)
	}
}
