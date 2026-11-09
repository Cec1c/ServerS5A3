package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Exercise the missing Anton case of the existing real clock regression. The
// fixture accepts the PVF volcano/heart clears through the production reducer;
// only the clock is controlled, and all assets use the ordinary memory owners.
func TestRealPVFAntonFinalManualCardsAdvanceThroughRaidClock(t *testing.T) {
	f, sessions := r195MovieStageFixture(t, "anton_phase1")
	flow := f.state.Battle.RewardFlow[1]
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.mu.Lock()
	q.now = flow.CompletedAt.Add(time.Second)
	q.mu.Unlock()
	f.service.gameplayTimers = q
	flow.AutoSelectAt = time.Now().Add(2 * time.Minute)
	for i, session := range sessions {
		dispatchAntonCardRequest(t, f, session, 659, []byte{1})
		dispatchAntonCardRequest(t, f, session, 660, []byte{1, byte(i)})
	}
	before := r195ManualRewardProjectionSignatures(sessions)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.service.startCurrentRaidClock(ctx)
	q.mu.Lock()
	task := q.active[currentRaidClockName]
	q.mu.Unlock()
	if task == nil {
		t.Fatal("real raid clock was not scheduled")
	}
	q.fire(task, false)
	r195WaitManualRewardWorker(t, f.service)
	wires, at := r195WaitManualRewardKind3(t, sessions)
	r195WaitManualRewardProjections(t, sessions, before)
	if !at.Before(flow.AutoSelectAt) {
		t.Fatal("manual completion waited for unattended fallback")
	}
	for _, session := range sessions {
		id := session.selectedCharacterID
		wire := r195ReadFiendRewardWire(t, wires[id])
		wire = r195ManualRewardOnlyKind3(t, wire)
		if !reflect.DeepEqual(wire.owners[3], []uint16{19, 20}) || len(wire.items) == 0 {
			t.Fatalf("owner%d team list omitted the two frozen recipients: %+v", id, wire)
		}
		for descriptor, count := range wire.items {
			// Pool choice Flags and the native reward-limit byte are distinct.
			// In particular, a rare pool must still display its granted items.
			if descriptor.flags != 0 || descriptor.quantity == 0 || count != 1 {
				t.Fatalf("hidden, empty or duplicate team descriptor: %+v count=%d", descriptor, count)
			}
		}
		if !flow.Party[id].Committed || !flow.Squad[id].Committed {
			t.Fatalf("owner%d did not settle both owned reward pools", id)
		}
	}
	t.Logf("real Anton clock delivered kind3 to both owners before %s", flow.AutoSelectAt.Format(time.RFC3339Nano))
}

func lateGoldAntonFinalFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, *currentAntonRewardFlow) {
	t.Helper()
	f, session, _, now := onlineAntonTerminalFixture(t)
	warmAntonRewardQuestFixture(t, f)
	ensureRaidRewardAccount(t, f, "account-20")
	config, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Battle.AntonFlowConfig = config
	if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}
	// Drive projection explicitly here. The separate clock test above covers
	// actual asynchronous subscribers; this avoids racing wire assertions with
	// an unrelated callback from the weekly transaction.
	f.service.mu.Lock()
	online := f.service.gameSessions
	f.service.gameSessions = nil
	f.service.mu.Unlock()
	done, errs := f.service.commitCurrentRaidWeeklyIntents(now)
	f.service.mu.Lock()
	f.service.gameSessions = online
	f.service.mu.Unlock()
	if len(done) != 1 || len(errs) != 0 {
		t.Fatal("actual weekly owner did not commit", done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	return f, session, f.state.Battle.RewardFlow[1]
}

// A genuine kind0 request is the second half of the native party card. It is
// distinct from a late 659 mask or kind1 reveal. The server has no gold pool,
// so its authoritative answer is the existing empty kind0, not another grant.
func TestRealPVFAntonFinalGoldReplySurvivesSquadCommit(t *testing.T) {
	for _, afterSquad := range []bool{false, true} {
		name := "before_squad"
		if afterSquad {
			name = "after_squad"
		}
		t.Run(name, func(t *testing.T) {
			f, session, flow := lateGoldAntonFinalFixture(t)
			dispatchAntonCardRequest(t, f, session, 660, []byte{1, 2})
			if flow.Party[19] == nil || !flow.Party[19].Committed {
				t.Fatal("real manual party grant missing")
			}
			if afterSquad {
				if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || flow.SquadShownAt.IsZero() {
					t.Fatal("real squad grant did not commit", errs)
				}
				if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
					t.Fatal(err)
				}
				if len(antonNativeCardBodies(t, f)[3]) == 0 {
					t.Fatal("fixture did not actually write the committed team list")
				}
			}
			before := r195PublicRewardAssetSnapshot(t, f.service, session)
			want, err := raid.BuildRewardListBody(0, []raid.RewardDisplayCard{{CharacterID: 19, CardType: flow.Party[19].Card}})
			if err != nil {
				t.Fatal(err)
			}
			for repeat := 0; repeat < 2; repeat++ {
				f.conn.write.Reset()
				dispatchAntonCardRequest(t, f, session, 660, []byte{0, 2})
				var ack, reveal int
				for wire := f.conn.write.Bytes(); len(wire) > 0; {
					packet, rest := splitGameServerUpperPacket(t, wire)
					wire = rest
					switch {
					case packet.Header.Classification == 1 && packet.Header.MsgID == 660 && bytes.Equal(packet.Body, []byte{1}):
						ack++
					case packet.Header.Classification == 0 && packet.Header.MsgID == 600 && bytes.Equal(packet.Body, want):
						reveal++
					default:
						t.Fatalf("kind0 reopened/overwrote reward screen: opcode=%d class=%d body=%x", packet.Header.MsgID, packet.Header.Classification, packet.Body)
					}
				}
				t.Logf("afterSquad=%t repeat=%d kind0=%d ack=%d expected=%x", afterSquad, repeat, reveal, ack, want)
				if ack != 1 || reveal != 1 {
					t.Errorf("owned native kind0 response lost: kind0=%d ack=%d", reveal, ack)
				}
			}
			after := r195PublicRewardAssetSnapshot(t, f.service, session)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("empty kind0 reply changed assets or durable receipts")
			}
		})
	}
}

func TestRealPVFAntonFinalLateGoldStillRejectsUnownedCardAndMasks(t *testing.T) {
	f, session, flow := lateGoldAntonFinalFixture(t)
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{0, 2})
	packet, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if packet.Header.MsgID != 660 || packet.Header.Classification != 1 || len(packet.Body) == 0 || packet.Body[0] != 0 || len(rest) != 0 {
		t.Fatal("kind0 without a committed party card was not rejected")
	}
	if len(flow.Party) != 0 {
		t.Fatal("gold request created a party entitlement")
	}
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 2})
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || flow.SquadShownAt.IsZero() {
		t.Fatal("squad setup", errs)
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	before := r195PublicRewardAssetSnapshot(t, f.service, session)
	// A late movie or repeated kind1 must still leave the current team screen
	// intact. The narrow kind0 exception cannot admit either original mask.
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 3})
	publicP14NoCardRestart(t, f.conn.write.Bytes(), "late movie/kind1 after team commit")
	if after := r195PublicRewardAssetSnapshot(t, f.service, session); !reflect.DeepEqual(before, after) {
		t.Fatal("late mask/replay changed assets or receipts")
	}
	// Even an internal attempted kind0 for another owner or a forged card
	// cannot pass the exact committed-body comparison.
	for _, card := range []raid.RewardDisplayCard{{CharacterID: 20, CardType: 2}, {CharacterID: 19, CardType: 3}} {
		body, err := raid.BuildRewardListBody(0, []raid.RewardDisplayCard{card})
		if err != nil {
			t.Fatal(err)
		}
		f.conn.write.Reset()
		if err := f.service.sendCurrentAntonRewardPackets(session, cloneRuntimeRaidState(*f.state), []currentAntonRewardPacket{{opcode: 600, body: body}}, false); err != nil || f.conn.write.Len() != 0 {
			t.Fatal("forged late gold descriptor escaped filtering", card, err)
		}
	}
}
