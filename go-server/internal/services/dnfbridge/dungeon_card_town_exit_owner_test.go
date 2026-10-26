package dnfbridge

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Preserve the first deferred request through route ownership transfer.
// This pinned baseline has no completed-op42 ACK sender: assert owner and
// scene/reward effects here, not an ACK supplied by a separate change.
func TestCardTownExitFirstRequestOwner(t *testing.T) {
	for _, first := range []uint16{42, 132} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			s, r, session, conn := cardTownExitFixture(t, 0)
			second := uint16(132)
			if first == 132 {
				second = 42
			}
			for _, opcode := range []uint16{first, second, first, second} {
				if err := cardTownExitRequest(t, s, session, opcode); err != nil {
					t.Fatal(err)
				}
			}
			if conn.write.Len() != 0 || session.dungeon.runtime != r {
				t.Fatal("early requests crossed layout barrier")
			}
			for _, opcode := range []uint16{69, 70} {
				if err := cardTownExitRequest(t, s, session, opcode); err != nil {
					t.Fatal(err)
				}
			}
			if r.townReturnRequestMsgID != first {
				t.Errorf("deferred return lost first owner: first=%d stored=%d", first, r.townReturnRequestMsgID)
			}
			assertCardTownExitCommitted(t, s, r, session, 1033)
			if got := cardTownExitOwnerFrameCount(t, conn.write.Bytes(), 0, 24); got != 1 {
				t.Fatalf("town transitions=%d", got)
			}
			paid := captureRound100RetryAssets(t, s, r.Dungeon.ID)
			conn.write.Reset()
			for _, opcode := range []uint16{42, 132, 70, 71} {
				if err := cardTownExitRequest(t, s, session, opcode); err != nil {
					t.Fatal(err)
				}
			}
			replay := captureRound100RetryAssets(t, s, r.Dungeon.ID)
			if conn.write.Len() != 0 || !reflect.DeepEqual(paid.character.Stats, replay.character.Stats) || !reflect.DeepEqual(paid.inventory, replay.inventory) {
				t.Fatal("retired mixed request replay mutated state")
			}
		})
	}
}

func TestCardTownExitRewardRetryOwner(t *testing.T) {
	for _, action := range []string{"town", "retry"} {
		t.Run(action, func(t *testing.T) {
			s, r, session, conn := cardTownExitFixture(t, 0)
			authenticateRound100RetrySession(s, session)
			queue := newFakeCurrentDungeonDeathTimerQueue()
			s.gameplayTimers = queue
			dispatch := func(opcode uint16, body []byte) {
				t.Helper()
				if err := s.handleGameUpper(session, buildCurrentDungeonClientUpperForPhaseTest(t, opcode, body)); err != nil {
					t.Fatal(err)
				}
			}
			dispatch(42, nil)
			provider := s.repositoryProvider
			repos, _ := provider()
			failure := cardTownExitFailedAssets{errors.New("test reward transaction failure")}
			repos.CharacterAssets = failure
			repos.MailboxAssets = failure
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			dispatch(69, nil)
			dispatch(70, nil)
			if session.dungeon.runtime != r || r.settlementCardRewardCommitted || r.townReturnPending {
				t.Fatal("failed reward crossed town barrier")
			}
			s.repositoryProvider = provider
			conn.write.Reset()
			if action == "town" {
				dispatch(72, []byte{1, 2})
				assertCardTownExitCommitted(t, s, r, session, 1033)
				if r.townReturnRequestMsgID != 42 {
					t.Fatal("op72 reward retry lost pending op42 return owner")
				}
			} else {
				dispatch(72, []byte{1, 0})
				next := session.dungeon.runtime
				if next == nil || next == r || next.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || next.settlementTownReturnRequestMsgID != 0 {
					t.Fatal("valid retry failed or inherited previous town intent")
				}
				before := captureRound100RetryAssets(t, s, r.Dungeon.ID)
				conn.write.Reset()
				if err := s.resumeCurrentDungeonCardTownReturn(session, r); err != nil {
					t.Fatal(err)
				}
				after := captureRound100RetryAssets(t, s, r.Dungeon.ID)
				if session.dungeon.runtime != next || conn.write.Len() != 0 || !reflect.DeepEqual(before.character.Stats, after.character.Stats) || !reflect.DeepEqual(before.inventory, after.inventory) {
					t.Fatal("old continuation affected new run")
				}
			}
		})
	}
}

func cardTownExitOwnerFrameCount(t *testing.T, raw []byte, classification byte, opcode uint16) int {
	t.Helper()
	count := 0
	for len(raw) > 0 {
		packet, tail := splitGameServerUpperPacket(t, raw)
		if packet.Header.Classification == classification && packet.Header.MsgID == opcode {
			count++
		}
		raw = tail
	}
	return count
}
