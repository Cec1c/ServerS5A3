package dnfbridge

import (
	"bytes"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Unlike the existing manual-party/automatic-squad matrix, the first movie
// completion here arrives only after the server finished the display window.
// All rewards and weekly eligibility pass through the existing asset owners.
func TestR158LateFinalMovieHasOneRewardTransition(t *testing.T) {
	for _, finished := range []bool{false, true} {
		name := "during_display"
		if finished {
			name = "after_display"
		}
		t.Run(name, func(t *testing.T) {
			f, session, _, at := onlineAntonTerminalFixture(t)
			cfg, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
			if err != nil {
				t.Fatal(err)
			}
			f.state.Battle.AntonFlowConfig = cfg
			if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
				t.Fatal(err)
			}
			flow := f.state.Battle.RewardFlow[1]
			if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
				t.Fatal(done, errs)
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
				t.Fatal(errs)
			}
			if finished {
				if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 || flow.FinishedAt.IsZero() {
					t.Fatal(errs)
				}
			}
			if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, session, 659, []byte{0})
			stream := append([]byte(nil), f.conn.write.Bytes()...)
			var starts, squads, acks int
			var order []uint16
			for len(stream) > 0 {
				p, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				order = append(order, p.Header.MsgID)
				if p.Header.MsgID == 587 && bytes.Equal(p.Body, []byte{4, 0}) {
					starts++
				}
				if p.Header.MsgID == 600 && p.Body[0] == 3 {
					squads++
				}
				if p.Header.MsgID == 659 && bytes.Equal(p.Body, []byte{1}) {
					acks++
				}
			}
			t.Logf("finished=%t wire_order=%v reward_starts=%d squad_lists=%d", finished, order, starts, squads)
			if acks != 1 || starts != 1 || squads != 1 {
				t.Errorf("late movie must restore one committed reward screen: ack=%d starts=%d squads=%d", acks, starts, squads)
			}
			if antonGrantedItemCount(t, f, "19", 10304529) != 45 || antonGrantedItemCount(t, f, "19", 10093974) != 52 {
				t.Fatal("real final rewards changed")
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, session, 659, []byte{0})
			p, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
			if p.Header.MsgID != 659 || len(rest) != 0 {
				t.Fatal("same movie replay reopened reward screen")
			}
		})
	}
}

func TestR158LateMovieCannotBypassWeeklyRollback(t *testing.T) {
	f, session, _, at := onlineAntonTerminalFixture(t)
	cfg, err := raid.LoadAntonFlowConfiguration(f.service.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Battle.AntonFlowConfig = cfg
	if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}
	flow := f.state.Battle.RewardFlow[1]
	original := f.repos.CharacterTrade
	f.repos.CharacterTrade = raidFailGroupTransaction{CharacterTradeUnitOfWork: original, group: original.(repo.CharacterInventoryGroupUnitOfWork), commitFailure: true}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 0 || len(errs) != 1 {
		t.Fatal("weekly rollback fixture", done, errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(time.Hour), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	p, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if p.Header.MsgID != 659 || len(p.Body) == 0 || p.Body[0] != 0 || len(rest) != 0 {
		t.Fatal("failed weekly owner published reward success")
	}
	if len(flow.Party) != 0 || len(flow.Squad) != 0 || !flow.FinishedAt.IsZero() || antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("late movie bypassed real atomic weekly receipt")
	}
}
