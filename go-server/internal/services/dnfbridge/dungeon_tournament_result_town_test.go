package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

func tournamentResultTownFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState) {
	t.Helper()
	s, session, rt := newBackToVillageRuntime(t)
	s.options.currentClientVersion = "official-24.9"
	clearTournamentRoomForTest(t, rt)
	if err := rt.Session.Complete(); err != nil {
		t.Fatal(err)
	}
	rt.Tournament = championTournamentRuntimeForTest(t, rt.Session.Snapshot().Scene.Map.Map.ID)
	rt.IgnoreDefaultDungeonClear = true
	rt.tournamentReward = tournamentRewardStateForTest(t, true)
	session.conn.(*bufferConn).write.Reset()
	return s, session, rt
}

// The live 2026-10-17 test99 request after tournament121 selection was 01 02.
// This fixture owns a synthetic map and persisted town origin; it must exercise
// the real handler and complete town route without ordinary card flags.
func TestTournamentResultTownClickAfterSelection(t *testing.T) {
	s, session, rt := tournamentResultTownFixture(t)
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	before, _, _ := repos.Inventory.Load(ctx, "99")
	charBefore, _, _ := repos.Character.Load(ctx, "99")
	rewardBefore := *rt.tournamentReward
	if rt.settlementClearRewardSent || rt.settlementCardLayoutSent {
		t.Fatal("fixture faked ordinary cards")
	}
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	raw := session.conn.(*bufferConn).write.Bytes()
	if len(raw) == 0 {
		t.Fatal("tournament result town click emitted no response")
	}
	ack, rest := splitGameServerUpperPacket(t, raw)
	if ack.Header.MsgID != 72 || ack.Header.Classification != currentDungeonCardResponseClass || !bytes.Equal(ack.Body, buildCurrentDungeonOp72SuccessBody(1, 2)) {
		t.Fatalf("bad action ACK: %+v %x", ack.Header, ack.Body)
	}
	op24 := 0
	for len(rest) > 0 {
		p, r := splitGameServerUpperPacket(t, rest)
		rest = r
		if p.Header.MsgID == 24 {
			op24++
			if !bytes.Equal(p.Body, wantDungeonTownTransitionBody()) {
				t.Fatalf("bad frozen town origin %x", p.Body)
			}
		}
	}
	if op24 != 1 || session.dungeon.runtime != nil || !rt.townReturnOp24Sent {
		t.Fatal("completed tournament did not finish town route")
	}
	if rt.settlementClearRewardSent || rt.settlementCardLayoutSent || rt.settlementCardRewardCommitted || rewardBefore != *rt.tournamentReward {
		t.Fatal("exit changed award/card state")
	}
	after, _, _ := repos.Inventory.Load(ctx, "99")
	charAfter, _, _ := repos.Character.Load(ctx, "99")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(charBefore, charAfter) {
		t.Fatal("result button changed persisted assets")
	}
	n := len(raw)
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if session.conn.(*bufferConn).write.Len() != n {
		t.Fatal("late duplicate replayed a detached run")
	}
}

func TestTournamentResultTownWriteFailuresRetryWithoutAwards(t *testing.T) {
	for _, point := range []string{"ack", "town24"} {
		t.Run(point, func(t *testing.T) {
			s, session, rt := tournamentResultTownFixture(t)
			want := errors.New("tournament town injected write")
			reward := *rt.tournamentReward
			if point == "ack" {
				session.conn = &failNthDungeonWriteConn{failAt: 1, err: want}
			} else {
				session.conn = &failCompletedTownOp24WriteConn{t: t, err: want}
			}
			if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); !errors.Is(err, want) {
				t.Fatalf("write error %v", err)
			}
			if session.dungeon.runtime != rt || rt.townReturnOp24Sent || rt.settlementCardExitAckSent != (point == "town24") {
				t.Fatal("failed write consumed owner/action")
			}
			if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime != nil || !rt.townReturnOp24Sent || reward != *rt.tournamentReward {
				t.Fatal("retry failed or changed reward")
			}
			if point == "town24" {
				c := session.conn.(*failCompletedTownOp24WriteConn)
				if c.successfulOp24Writes != 1 || c.positionWrites != 1 || c.areaWrites != 1 {
					t.Fatal("retry duplicated town route")
				}
			}
		})
	}
}

func TestTournamentResultTownRequiresOwnedCompletedSelection(t *testing.T) {
	for _, name := range []string{"unselected", "foreign", "no-result", "ordinary", "active-tournament", "legacy", "unknown-action", "retry", "selector"} {
		t.Run(name, func(t *testing.T) {
			s, session, rt := tournamentResultTownFixture(t)
			body := []byte{1, 2}
			switch name {
			case "unselected":
				rt.tournamentReward.Selected[1] = tournamentUnselectedCard
			case "foreign":
				session.selectedCharacterID = 98
			case "no-result":
				rt.tournamentReward = nil
			case "ordinary":
				rt.Tournament = nil
			case "active-tournament":
				v := tournamentFlowFixtureDefinition(rt.Session.Snapshot().Scene.Map.Map.ID)
				var ok bool
				rt.Tournament, _, ok = newTournamentDungeonRuntime(&v, 1, deterministicTournamentNext(9))
				if !ok {
					t.Fatal("active fixture")
				}
			case "legacy":
				s.options.currentClientVersion = "legacy"
			case "unknown-action":
				body = []byte{1, 3}
			case "retry":
				body = []byte{1, 0}
			case "selector":
				body = []byte{1, 1}
			}
			if err := s.handleDungeonEplpCommand(session, body); err != nil {
				t.Fatal(err)
			}
			if session.conn.(*bufferConn).write.Len() != 0 || session.dungeon.runtime != rt || rt.settlementCardExitAckSent || rt.townReturnPending {
				t.Fatal("unready/unsupported action escaped tournament result")
			}
		})
	}
}

func TestTournamentResultTownFocusDoesNotConsumeClick(t *testing.T) {
	s, session, rt := tournamentResultTownFixture(t)
	if err := s.handleDungeonEplpCommand(session, []byte{2, 2}); err != nil {
		t.Fatal(err)
	}
	if session.conn.(*bufferConn).write.Len() == 0 || rt.settlementCardExitAckSent || rt.townReturnPending {
		t.Fatal("focus did not ACK independently")
	}
	session.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil {
		t.Fatal("focus consumed actual town click")
	}
}

func TestTournamentResultTownLegacyDispatchPreservesActuallyGrantedRewards(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	s, session, rt := tournamentResultTownFixture(t)
	rt.tournamentReward = tournamentRewardStateForTest(t, false)
	repos, _ := s.repositoryGroup()
	ctx := context.Background()
	before, _, _ := repos.Character.Load(ctx, "99")
	for _, kind := range []byte{0, 1} {
		if handled, err := s.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{kind, 0}); err != nil || !handled {
			t.Fatalf("selection %d handled=%v err=%v", kind, handled, err)
		}
	}
	granted, _, _ := repos.Character.Load(ctx, "99")
	if !rt.tournamentReward.isSelectionComplete() || granted.Stats["gold"]-before.Stats["gold"] != 2468 {
		t.Fatal("fixture did not actually commit both gold rows")
	}
	bag, _, _ := repos.Inventory.Load(ctx, "99")
	session.conn.(*bufferConn).write.Reset()
	if handled, err := currentGameplayModules.DispatchLegacy(s, session, 72, []byte{1, 2}); err != nil || !handled {
		t.Fatalf("legacy op72 handled=%v err=%v", handled, err)
	}
	if session.dungeon.runtime != nil || !rt.townReturnOp24Sent {
		t.Fatal("legacy dispatcher did not complete town route")
	}
	after, _, _ := repos.Character.Load(ctx, "99")
	bagAfter, _, _ := repos.Inventory.Load(ctx, "99")
	if !reflect.DeepEqual(granted, after) || !reflect.DeepEqual(bag, bagAfter) {
		t.Fatal("exit re-awarded or changed committed assets")
	}
}
