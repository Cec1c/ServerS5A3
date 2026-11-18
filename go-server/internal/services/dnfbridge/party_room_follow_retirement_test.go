package dnfbridge

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoomFollowAcceptedPlanSurvivesLeaderRetirement(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		name := "personal_return"
		if disconnect {
			name = "disconnect"
		}
		t.Run(name, func(t *testing.T) {
			f := newRoomFollowQueueFixture(t, nil)
			leader, blocked := f.peers[0], f.peers[1]
			if disconnect {
				f.s.shutdownGameSessionEvents(leader, false)
			} else if err := f.s.callGameSession(f.ctx, leader, "leader-returns-after-follow-admission", func() error {
				if err := f.s.handleDungeonGiveupGame(leader, nil); err != nil {
					return err
				}
				return f.s.commitPendingDungeonReturnForSceneRequest(leader, "accepted_follow_leader_return")
			}); err != nil {
				t.Fatal(err)
			}
			if _, found := f.s.runtimePartyManagerForService().SnapshotByUser(99, 0); found {
				t.Fatal("retired leader retained central party membership")
			}
			f.release()
			<-f.gateDone
			waitRoomFollowQueueEmpty(t, blocked)
			if countRoomFollowPackets(t, blocked, 29) != f.initial29+1 {
				t.Fatal("leader retirement cancelled an already-accepted recipient follow")
			}
			survivors := f.peers[1:]
			for _, p := range survivors {
				if err := f.s.callGameSession(f.ctx, p, "retained-room-loading-after-leader-retirement", func() error {
					return f.s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
				}); err != nil {
					t.Fatal(err)
				}
			}
			awaitRoomFanoutLoading(t, f.s, f.ctx, survivors)
			state, found := f.s.runtimePartyManagerForService().SnapshotByUser(blocked.selectedCharacterID, 0)
			if !found || len(state.Members) != 3 || state.Leader != blocked.selectedCharacterID {
				t.Fatal("accepted plan lost retained party or transferred leader")
			}
		})
	}
}

type roomFollowWriteFaultConn struct {
	*partyDungeonSyncConn
	partial bool
	failAt  int32
	fault   error
	once    sync.Once
	first   []byte
	attempt atomic.Int32
	closed  atomic.Bool
}

func (c *roomFollowWriteFaultConn) Write(data []byte) (int, error) {
	fail := false
	attempt := c.attempt.Add(1)
	failAt := c.failAt
	if failAt == 0 {
		failAt = 1
	}
	if attempt == failAt {
		c.once.Do(func() { c.first = append([]byte(nil), data...); fail = true })
	}
	if fail {
		if c.partial {
			_, _ = c.partyDungeonSyncConn.Write(data[:1])
			return 1, c.fault
		}
		return 0, c.fault
	}
	return c.partyDungeonSyncConn.Write(data)
}

func (c *roomFollowWriteFaultConn) Close() error { c.closed.Store(true); return nil }

func TestRoomFollowWriteFailureRecoveryAndStreamRetirement(t *testing.T) {
	for _, mode := range []string{"zero_byte_once", "partial_frame", "post_start_zero"} {
		t.Run(mode, func(t *testing.T) {
			partial, postStart := mode == "partial_frame", mode == "post_start_zero"
			terminal := partial || postStart
			f := newRoomFollowQueueFixture(t, nil)
			p := f.peers[1]
			for _, healthy := range f.peers[2:] {
				if err := f.s.callGameSession(f.ctx, healthy, "wait-healthy-follow-commit", func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			base := p.conn.(*partyDungeonSyncConn)
			before := len(base.frames())
			g, _ := f.s.repositoryGroup()
			characterID := strconv.Itoa(int(p.selectedCharacterID))
			beforeCharacter, _, _ := g.Character.Load(f.ctx, characterID)
			healthyCharacter, _, _ := g.Character.Load(f.ctx, strconv.Itoa(int(f.peers[2].selectedCharacterID)))
			conn := &roomFollowWriteFaultConn{partyDungeonSyncConn: base, partial: partial, fault: errors.New("one-shot room follow write fault")}
			if postStart {
				conn.failAt = 2
			}
			p.wireMu.Lock()
			p.conn = conn
			p.wireMu.Unlock()
			f.release()
			<-f.gateDone
			deadline := time.Now().Add(2 * time.Second)
			for {
				p.dungeon.mu.Lock()
				scene, _ := p.dungeon.runtime.Session.Scene()
				moved := scene.Coordinate.X == 1
				p.dungeon.mu.Unlock()
				if terminal && p.gameTransportContext().Err() != nil || !terminal && moved {
					break
				}
				if time.Now().After(deadline) {
					t.Errorf("follow write recovery state: partial=%t moved=%t transport=%v", partial, moved, p.gameTransportContext().Err())
					break
				}
				time.Sleep(time.Millisecond)
			}
			waitRoomFollowQueueEmpty(t, p)
			if terminal {
				// The memory connection has no live TCP read pump. Invoke the
				// normal shutdown entry explicitly after its transport closes.
				f.s.shutdownGameSessionEvents(p, false)
			} else {
				stopTestGameSessionEvents(t, f.s, p)
			}
			afterCharacter, _, _ := g.Character.Load(f.ctx, characterID)
			first, rest := splitCurrentGameServerUpperPacketAuto(t, conn.first)
			if (!postStart && first.Header.MsgID != 29) || (postStart && first.Header.MsgID == 29) || len(rest) != 0 {
				t.Fatal("write fault was not applied to the actual first start-map packet")
			}
			if terminal {
				if !conn.closed.Load() || partial && (conn.attempt.Load() != 1 || len(base.frames()) != before+1) || postStart && conn.attempt.Load() != 2 {
					t.Fatal("partial frame was retried or failed to retire its recipient stream")
				}
				if partial && afterCharacter.Stats["fatigue"] != beforeCharacter.Stats["fatigue"] {
					t.Fatal("partial uncommitted start-map debited next-room fatigue")
				}
				survivors := []*gameSession{f.peers[0], f.peers[2], f.peers[3]}
				for _, healthy := range survivors {
					if err := f.s.callGameSession(f.ctx, healthy, "load-after-failed-peer-shutdown", func() error {
						return f.s.sendFinishLoadingStatus(healthy, make([]byte, currentFinishLoadingLegacyRequestBodySize))
					}); err != nil {
						t.Fatal(err)
					}
				}
				awaitRoomFanoutLoading(t, f.s, f.ctx, survivors)
				if postStart {
					stream, starts := base.frames(), 0
					for len(stream) > 0 {
						frame, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
						if frame.Header.MsgID == 29 {
							starts++
						}
						stream = rest
					}
					if starts != f.initial29+1 {
						t.Fatal("post-start failure replayed an already delivered start-map")
					}
				}
			} else {
				if conn.closed.Load() || conn.attempt.Load() < 2 {
					t.Fatal("safe zero-byte failure neither recovered nor preserved live recipient")
				}
				stream := base.frames()
				starts := 0
				for len(stream) > 0 {
					frame, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
					if frame.Header.MsgID == 29 {
						starts++
					}
					stream = rest
				}
				if starts != f.initial29+1 {
					t.Fatal("zero-byte retry duplicated a start-map")
				}
				if afterCharacter.Stats["fatigue"] != healthyCharacter.Stats["fatigue"] || afterCharacter.Stats["fatigue"] >= beforeCharacter.Stats["fatigue"] {
					t.Fatal("safe retry did not debit exactly the same one-room fatigue as an ordinary healthy follower")
				}
			}
		})
	}
}

func TestRoomFollowUnsentRetryBudgetRetiresOnlyRecipient(t *testing.T) {
	f := newRoomFollowQueueFixture(t, nil)
	p := f.peers[1]
	q := &p.partyRoomMove
	q.mu.Lock()
	q.pending.writeRetryStarted = time.Now().Add(-gameTransportWriteTimeout - time.Second)
	q.mu.Unlock()
	base := p.conn.(*partyDungeonSyncConn)
	before := len(base.frames())
	conn := &roomFollowWriteFaultConn{partyDungeonSyncConn: base, fault: errors.New("expired unsent room write")}
	p.wireMu.Lock()
	p.conn = conn
	p.wireMu.Unlock()
	f.release()
	<-f.gateDone
	deadline := time.Now().Add(2 * time.Second)
	for p.gameTransportContext().Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("expired zero-byte retry budget left incomplete recipient live")
		}
		time.Sleep(time.Millisecond)
	}
	waitRoomFollowQueueEmpty(t, p)
	stopTestGameSessionEvents(t, f.s, p)
	if !conn.closed.Load() || conn.attempt.Load() != 0 || len(base.frames()) != before {
		t.Fatal("expired retry budget wrote or retried an incomplete start-map")
	}
	for _, healthy := range []*gameSession{f.peers[0], f.peers[2], f.peers[3]} {
		if err := f.s.callGameSession(f.ctx, healthy, "healthy-peer-after-recipient-retirement", func() error {
			scene, _ := healthy.dungeon.runtime.Session.Scene()
			if scene.Coordinate.X != 1 || healthy.gameTransportContext().Err() != nil {
				t.Error("retiring one failed recipient affected a healthy party stream")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

type roomFollowOpaqueProviderError struct{}

func (roomFollowOpaqueProviderError) Error() string { panic("provider Error must not be inspected") }
func (roomFollowOpaqueProviderError) Unwrap() error { panic("provider Unwrap must not be inspected") }

func TestRoomFollowRetryClassifierDoesNotInspectProviderErrors(t *testing.T) {
	f := newRoomFollowQueueFixture(t, nil)
	q := &f.peers[1].partyRoomMove
	q.mu.Lock()
	p := q.pending
	q.mu.Unlock()
	if retained, _ := f.s.retainUnsentPartyRoomMove(p, roomFollowOpaqueProviderError{}); retained {
		t.Fatal("unclassified provider error became replayable")
	}
	q.mu.Lock()
	unchanged := q.pending == p && p.writeRetryStarted.IsZero()
	q.mu.Unlock()
	if !unchanged {
		t.Fatal("provider classification mutated pending delivery")
	}
}

func TestRoomFollowExpiredRetryDoesNotCloseSupersededOwner(t *testing.T) {
	for _, mode := range []string{"runtime", "token", "visit", "instance", "personal_return", "membership"} {
		t.Run(mode, func(t *testing.T) {
			f := newRoomFollowQueueFixture(t, nil)
			p := f.peers[1]
			q := &p.partyRoomMove
			q.mu.Lock()
			q.pending.writeRetryStarted = time.Now().Add(-gameTransportWriteTimeout - time.Second)
			q.mu.Unlock()
			if mode == "membership" {
				manager := f.s.runtimePartyManagerForService()
				before, _ := manager.SnapshotByUser(p.selectedCharacterID, 0)
				if result := manager.LeaveDungeonExpected(p.selectedCharacterID, p.characterGeneration, before.ID); !result.OK {
					t.Fatal(result.Reason)
				}
			} else {
				p.dungeon.mu.Lock()
				switch mode {
				case "runtime":
					replacement := *p.dungeon.runtime
					p.dungeon.runtime = &replacement
				case "token":
					p.dungeon.runToken++
				case "visit":
					p.dungeon.runtime.loadingRoomVisit++
				case "instance":
					p.dungeon.runtime.partyInstance = &dungeonPartyInstance{}
				case "personal_return":
					p.dungeon.runtime.townReturnPending = true
				}
				p.dungeon.mu.Unlock()
			}
			f.release()
			<-f.gateDone
			waitRoomFollowQueueEmpty(t, p)
			stopTestGameSessionEvents(t, f.s, p)
			if p.gameTransportContext().Err() != nil || countRoomFollowPackets(t, p, 29) != f.initial29 {
				t.Fatal("expired old follow retired or wrote to a superseded owner")
			}
		})
	}
}
