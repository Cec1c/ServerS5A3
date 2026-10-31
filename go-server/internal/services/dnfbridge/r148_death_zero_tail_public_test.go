package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func r148DeathFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	s, g, r := newBackToVillageRuntime(t)
	authenticateRound100RetrySession(s, g)
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	repos, _ := s.repositoryGroup()
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{fmt.Sprintf("0:%d", csharpReviveCoinWalletSlot): {ItemID: int64(csharpReviveCoinItemID), Count: 3}}}); err != nil {
		t.Fatal(err)
	}
	return s, g, r, q
}
func r148Dispatch(t *testing.T, s *Service, g *gameSession, op uint16, body []byte) {
	t.Helper()
	if err := s.handleGameUpper(g, buildCurrentDungeonClientUpperForPhaseTest(t, op, body)); err != nil {
		t.Fatalf("op%d: %v", op, err)
	}
}
func r148Bag(t *testing.T, s *Service) dnfrepo.InventoryRecord {
	t.Helper()
	repos, _ := s.repositoryGroup()
	bag, found, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("bag found=%t err=%v", found, err)
	}
	return cloneRound100Inventory(bag)
}
func r148Coins(t *testing.T, s *Service) int64 {
	t.Helper()
	return r148Bag(t, s).Slots[fmt.Sprintf("0:%d", csharpReviveCoinWalletSlot)].Count
}
func r148Complete(t *testing.T, s *Service, g *gameSession, r *runtimeDungeonState) {
	t.Helper()
	if _, err := r.Room.AnnounceAllActors(r.Session); err != nil {
		t.Fatal(err)
	}
	boss := r.Room.Snapshot().Monsters[0].ObjectKey
	body := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(body, boss)
	binary.LittleEndian.PutUint16(body[4:], 99)
	r148Dispatch(t, s, g, uint16(dnfenum.CmdPacketDieMonster), body)
	r148Dispatch(t, s, g, uint16(dnfenum.CmdPacketBossDieCheck), bossDieCheckRequestBody(99, uint16(boss)))
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.settlementEntrySent {
		t.Fatal("completion fixture rejected")
	}
}
func r148AssertRevive(t *testing.T, s *Service, g *gameSession, r *runtimeDungeonState) {
	t.Helper()
	conn := g.conn.(*bufferConn)
	conn.write.Reset()
	r148Dispatch(t, s, g, 41, []byte{99, 0})
	if r148Coins(t, s) != 2 || r.deathStateSent || r.deathReturnWaiting {
		t.Fatal("revive did not consume exactly one coin and clear death")
	}
	noti, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	ack, _ := splitGameServerUpperPacket(t, rest)
	if noti.Header.Classification != 0 || noti.Header.MsgID != 32 || !bytes.Equal(noti.Body, []byte{99, 0, 1, 0}) || ack.Header.Classification != 1 || ack.Header.MsgID != 41 || !bytes.Equal(ack.Body, []byte{1, 0}) {
		t.Fatal("revive notification must precede successful ACK")
	}
}

func TestR148DeathShapeAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{{"four", []byte{0xec, 0xff, 234, 0}}, {"six_zero", []byte{0xec, 0xff, 234, 0, 0, 0}}} {
		t.Run(tc.name, func(t *testing.T) {
			s, g, r, q := r148DeathFixture(t)
			before := append([]byte(nil), tc.body...)
			r148Dispatch(t, s, g, 40, tc.body)
			conn := g.conn.(*bufferConn)
			if !r.deathStateSent || !r.deathReturnWaiting || conn.write.Len() == 0 {
				t.Fatalf("active legal death rejected: body=%x dead=%t waiting=%t output_bytes=%d", tc.body, r.deathStateSent, r.deathReturnWaiting, conn.write.Len())
			}
			p, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if p.Header.Classification != 0 || p.Header.MsgID != 32 || !bytes.Equal(p.Body, []byte{99, 0, 0, 0}) || len(rest) != 0 {
				t.Fatal("wrong death projection")
			}
			if !bytes.Equal(before, tc.body) || r148Coins(t, s) != 3 {
				t.Fatal("death changed input or assets")
			}
			if scheduled, _, active := q.counts(); scheduled != 1 || active != 1 {
				t.Fatal("death must have one active timer")
			}
			r148AssertRevive(t, s, g, r)
		})
	}
}

func TestR148DeathRejectsOtherShapesAndClasses(t *testing.T) {
	cases := []struct {
		name  string
		body  []byte
		class byte
	}{}
	for _, size := range []int{0, 1, 2, 3, 5, 7, 8, 20} {
		cases = append(cases, struct {
			name  string
			body  []byte
			class byte
		}{fmt.Sprintf("length%d", size), make([]byte, size), 1})
	}
	for _, tail := range []uint16{1, 256, 65535} {
		b := make([]byte, 6)
		binary.LittleEndian.PutUint16(b[4:], tail)
		cases = append(cases, struct {
			name  string
			body  []byte
			class byte
		}{fmt.Sprintf("tail%d", tail), b, 1})
	}
	for _, class := range []byte{0, 2} {
		cases = append(cases, struct {
			name  string
			body  []byte
			class byte
		}{fmt.Sprintf("class%d", class), make([]byte, 6), class})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, g, r, q := r148DeathFixture(t)
			before := r148Bag(t, s)
			packet, err := dnfproto.BuildChannelPacket(40, tc.body, 0, tc.class)
			if err != nil {
				t.Fatal(err)
			}
			// The builder defaults class zero to command class one; preserve the
			// requested raw class for this negative dispatcher test.
			packet[0] = tc.class
			if err = s.handleGameUpper(g, packet); err != nil {
				t.Fatal(err)
			}
			if g.conn.(*bufferConn).write.Len() != 0 || r.deathStateSent || r.deathReturnWaiting || !reflect.DeepEqual(before, r148Bag(t, s)) {
				t.Fatal("rejected shape changed death/assets or sent packet")
			}
			if scheduled, _, active := q.counts(); scheduled != 0 || active != 0 {
				t.Fatal("rejected shape scheduled a timer")
			}
		})
	}
}

func TestR148DeathZeroTailRetainsOwnerGuards(t *testing.T) {
	for _, boundary := range []string{"no_runtime", "unselected", "wrong_character", "zero_lifecycle", "town_pending", "completed_first_death", "completed_after_revive"} {
		t.Run(boundary, func(t *testing.T) {
			s, g, r, q := r148DeathFixture(t)
			switch boundary {
			case "no_runtime":
				g.dungeon.runtime = nil
			case "unselected":
				g.selectedCharacterID = 0
			case "wrong_character":
				g.selectedCharacterID = 100
			case "zero_lifecycle":
				r.lifecycleToken = 0
			case "town_pending":
				r.townReturnPending = true
			case "completed_first_death":
				r148Complete(t, s, g, r)
			case "completed_after_revive":
				r148Dispatch(t, s, g, 40, make([]byte, 4))
				r148AssertRevive(t, s, g, r)
				r148Complete(t, s, g, r)
			}
			before := r148Bag(t, s)
			conn := g.conn.(*bufferConn)
			conn.write.Reset()
			r148Dispatch(t, s, g, 40, []byte{218, 1, 234, 0, 0, 0})
			r148Dispatch(t, s, g, 41, []byte{99, 0})
			if conn.write.Len() != 0 || r.deathStateSent || r.deathReturnWaiting || !reflect.DeepEqual(before, r148Bag(t, s)) {
				t.Fatal("zero tail widened death owner or revive admission")
			}
			if _, _, active := q.counts(); active != 0 {
				t.Fatal("invalid owner left active death timer")
			}
		})
	}
}

func TestR148DeathMixedRepeatKeepsOneTimerAndOneReviveDebit(t *testing.T) {
	s, g, r, q := r148DeathFixture(t)
	before := r148Bag(t, s)
	for _, body := range [][]byte{{218, 1, 234, 0, 0, 0}, {218, 1, 234, 0}, {218, 1, 234, 0, 0, 0}} {
		r148Dispatch(t, s, g, 40, body)
	}
	if scheduled, _, active := q.counts(); scheduled != 3 || active != 1 {
		t.Fatalf("existing rearm contract: scheduled=%d active=%d", scheduled, active)
	}
	if !reflect.DeepEqual(before, r148Bag(t, s)) {
		t.Fatal("death repeat debited assets")
	}
	conn := g.conn.(*bufferConn)
	conn.write.Reset()
	q.fire(q.task(t, 0), true)
	q.fire(q.task(t, 1), true)
	if conn.write.Len() != 0 || !r.deathStateSent || !r.deathReturnWaiting || r.townReturnPending {
		t.Fatal("old death callback escaped generation barrier")
	}
	r148AssertRevive(t, s, g, r)
	paid := r148Bag(t, s)
	conn.write.Reset()
	r148Dispatch(t, s, g, 41, []byte{99, 0})
	q.fire(q.task(t, 2), true)
	if conn.write.Len() != 0 || !reflect.DeepEqual(paid, r148Bag(t, s)) || r.townReturnPending {
		t.Fatal("revive replay/stale callback changed committed assets")
	}
	// Never grant a new completed death from a repeat of the same six bytes.
	r148Complete(t, s, g, r)
	conn.write.Reset()
	r148Dispatch(t, s, g, 40, []byte{218, 1, 234, 0, 0, 0})
	r148Dispatch(t, s, g, 41, []byte{99, 0})
	if conn.write.Len() != 0 || !reflect.DeepEqual(paid, r148Bag(t, s)) || r.deathStateSent {
		t.Fatal("completed old death replay accepted")
	}
}

func TestR148DeathZeroTailCoinSaveFailureRollsBack(t *testing.T) {
	s, g, r, q := r148DeathFixture(t)
	r148Dispatch(t, s, g, 40, []byte{218, 1, 234, 0, 0, 0})
	if !r.deathStateSent {
		t.Fatal("zero-tail death not accepted before failure injection")
	}
	repos, _ := s.repositoryGroup()
	before := r148Bag(t, s)
	failing := repos
	failing.CharacterItems = pickupTestItemUnitOfWork{inventory: pickupFailingInventoryRepository{InventoryRepository: repos.Inventory, err: errors.New("r148 injected save failure")}, equipment: repos.Equipment}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return failing, true }
	conn := g.conn.(*bufferConn)
	conn.write.Reset()
	r148Dispatch(t, s, g, 41, []byte{99, 0})
	if !reflect.DeepEqual(before, r148Bag(t, s)) || !r.deathStateSent || !r.deathReturnWaiting {
		t.Fatal("failed save changed coins/death owner")
	}
	p, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if p.Header.Classification != 1 || p.Header.MsgID != 41 || len(p.Body) == 0 || p.Body[0] != 0 || len(rest) != 0 {
		t.Fatal("failed save must only send failure ACK")
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	r148AssertRevive(t, s, g, r)
	paid := r148Bag(t, s)
	conn.write.Reset()
	r148Dispatch(t, s, g, 41, []byte{99, 0})
	q.fire(q.task(t, 0), true)
	if conn.write.Len() != 0 || !reflect.DeepEqual(paid, r148Bag(t, s)) {
		t.Fatal("recovered transaction debited twice")
	}
}

// Both decoders must preserve the same narrow admission and timer contract.
// These fixtures are synthetic and need neither a private PVF nor host env.
func TestR148DeathBothEntriesAndEnvironment(t *testing.T) {
	for _, mode := range []string{"", "keep", "legacy"} {
		for _, entry := range []string{"current", "legacy"} {
			for _, shape := range []string{"four", "six_zero", "six_nonzero", "short", "long", "completed", "wrong_owner"} {
				t.Run(mode+"/"+entry+"/"+shape, func(t *testing.T) {
					t.Setenv(partyDeathReturnRoomTransitionEnv, mode)
					s, g, r, q := r148DeathFixture(t)
					body := []byte{218, 1, 234, 0, 0, 0}
					switch shape {
					case "four":
						body = body[:4]
					case "six_nonzero":
						body[4] = 1
					case "short":
						body = body[:3]
					case "long":
						body = append(body, 0)
					case "completed":
						r148Complete(t, s, g, r)
					case "wrong_owner":
						g.selectedCharacterID = 100
					}
					conn := g.conn.(*bufferConn)
					conn.write.Reset()
					before := r148Bag(t, s)
					if entry == "legacy" {
						packet := buildLegacyGamePacketForDeathTest(byte(dnfenum.GameCmdCommand), 40, 51, body)
						if err := s.handleLegacyGamePacket(g, packet); err != nil {
							t.Fatal(err)
						}
					} else {
						r148Dispatch(t, s, g, 40, body)
					}
					if !reflect.DeepEqual(before, r148Bag(t, s)) {
						t.Fatal("death admission changed assets")
					}
					if shape != "four" && shape != "six_zero" {
						if conn.write.Len() != 0 || r.deathStateSent || r.deathReturnWaiting {
							t.Fatal("rejected request changed life or emitted response")
						}
						if scheduled, _, active := q.counts(); scheduled != 0 || active != 0 {
							t.Fatal("rejected request scheduled timer")
						}
						return
					}
					p, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
					if p.Header.Classification != 0 || p.Header.MsgID != 32 || !bytes.Equal(p.Body, []byte{99, 0, 0, 0}) || len(rest) != 0 {
						t.Fatal("accepted request has wrong death notification")
					}
					task := q.task(t, 0)
					if task.delay != currentDungeonDeathReturnDelay || !r.deathStateSent || !r.deathReturnWaiting {
						t.Fatal("accepted request did not arm normal death timer")
					}
					conn.write.Reset()
					q.fire(task, false)
					p, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
					if p.Header.Classification != 0 || p.Header.MsgID != 24 || !bytes.Equal(p.Body, wantDungeonTownTransitionBody()) || len(rest) != 0 || !r.townReturnPending {
						t.Fatal("death timer did not use frozen town origin")
					}
				})
			}
		}
	}
}

// Both accepted death shapes retain the same completed-run revival owner.
// A repeated coin request or stale death timer must not debit or return twice.
func TestR148DeathPublicCompletedReviveRetainsOwner(t *testing.T) {
	for _, size := range []int{4, 6} {
		t.Run(fmt.Sprintf("accepted_%d_then_completed", size), func(t *testing.T) {
			s, g, r, q := r148DeathFixture(t)
			r148Dispatch(t, s, g, 40, make([]byte, size))
			if !r.deathStateSent || !r.deathReturnWaiting {
				t.Fatal("active death must be accepted before completion")
			}
			task := q.task(t, 0)
			r148Complete(t, s, g, r)
			phase, entry := r.settlementPhase, r.settlementEntrySent
			r148AssertRevive(t, s, g, r)
			if r.settlementPhase != phase || r.settlementEntrySent != entry || g.dungeon.runtime != r {
				t.Fatal("completed revive changed settlement or run owner")
			}
			before := r148Bag(t, s)
			conn := g.conn.(*bufferConn)
			conn.write.Reset()
			r148Dispatch(t, s, g, 41, []byte{99, 0})
			q.fire(task, true)
			if conn.write.Len() != 0 || !reflect.DeepEqual(before, r148Bag(t, s)) || r.deathStateSent || r.deathReturnWaiting || r.townReturnPending {
				t.Fatal("completed revive replay changed assets or revived stale timer")
			}
			if _, _, active := q.counts(); active != 0 {
				t.Fatal("completion left active death timer")
			}
		})
	}
}

// The integrated live baseline retains a proven ordinary-run death through
// settlement. Both accepted op40 shapes must obey the same narrow owner and
// exactly-once debit contract; an unproved or replaced owner remains rejected.
func TestR148DeathCompletedReviveRequiresCurrentRetainedOwner(t *testing.T) {
	for _, size := range []int{4, 6} {
		for _, proof := range []string{"retained", "no_accepted_death", "unproved_completion", "replaced_run"} {
			t.Run(fmt.Sprintf("accepted_%d_then_completed/%s", size, proof), func(t *testing.T) {
				s, g, r, q := r148DeathFixture(t)
				r148Dispatch(t, s, g, 40, make([]byte, size))
				if !r.deathStateSent || !r.deathReturnWaiting {
					t.Fatal("active death must be accepted before completion")
				}
				task := q.task(t, 0)
				r148Complete(t, s, g, r)
				switch proof {
				case "no_accepted_death":
					r.deathStateSent = false
				case "unproved_completion":
					r.bossDieCheckAccepted = false
				case "replaced_run":
					g.dungeon.runToken++
				}
				before := r148Bag(t, s)
				wasDead, wasWaiting := r.deathStateSent, r.deathReturnWaiting
				phase, result, entry := r.settlementPhase, r.settlementPlayResultReceived, r.settlementEntrySent
				conn := g.conn.(*bufferConn)
				conn.write.Reset()
				if proof == "retained" {
					r148AssertRevive(t, s, g, r)
					before = r148Bag(t, s)
					wasDead, wasWaiting = false, false
					conn.write.Reset()
				}
				r148Dispatch(t, s, g, 41, []byte{99, 0})
				q.fire(task, true)
				if conn.write.Len() != 0 || !reflect.DeepEqual(before, r148Bag(t, s)) || r.deathStateSent != wasDead || r.deathReturnWaiting != wasWaiting || r.townReturnPending {
					t.Fatal("unproved/repeated revive or stale timer changed assets or return owner")
				}
				if r.settlementPhase != phase || r.settlementPlayResultReceived != result || r.settlementEntrySent != entry || g.dungeon.runtime != r {
					t.Fatal("revive altered settlement barriers or runtime ownership")
				}
				if _, _, active := q.counts(); active != 0 {
					t.Fatal("completion left active death timer")
				}
			})
		}
	}
}
