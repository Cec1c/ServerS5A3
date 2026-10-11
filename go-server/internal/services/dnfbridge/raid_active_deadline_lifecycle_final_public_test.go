package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func deadlineLifecycleV14PublicSolo(t *testing.T, a *platformpvf.Archive) (*raidStartGuardFixture, *gameSession, *runtimeDungeonState, *deadlineAuditV10Conn, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	f, owners, conns, q := deadlineAuditV10Admit(t, a, true)
	p, s := owners[0], f.service
	if len(f.state.Battle.SingleSequence) == 0 {
		t.Fatal("actual single sequence empty")
	}
	target := f.state.Battle.SingleSequence[0]
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})
	var r *runtimeDungeonState
	deadlineAuditV10Event(t, s, p, func() error {
		if err := s.handleDungeonSelectUpper(p, body); err != nil {
			return err
		}
		r = p.dungeon.runtime
		if r == nil || r.raidEntry == nil || r.Dungeon.ID != int64(target) {
			return fmt.Errorf("actual solo runtime absent")
		}
		return s.sendFinishLoadingStatus(p, make([]byte, 8))
	})
	s.raidMu.Lock()
	e := f.state.Battle.Participation[p.selectedCharacterID]
	s.raidMu.Unlock()
	if e.Runtime != r || e.Session != p {
		t.Fatal("actual solo placement missing")
	}
	return f, p, r, conns[0], q
}

// Every room transition/death/completion goes through the real handlers.
// Only requests from actual admitted topology and announced object keys are
// constructed. No direct Complete/Announce/CommitDeath/settlement flag writes.
func deadlineLifecycleV14ReachAcceptedBoss(t *testing.T, s *Service, p *gameSession, r *runtimeDungeonState) {
	t.Helper()
	_, resolver, err := s.dungeonWorldMap()
	if err != nil {
		t.Fatal(err)
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, r.Dungeon.ID, r.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	start := r.Session.Snapshot().Scene.Coordinate
	queue := []worldmap.RoomCoordinate{start}
	seen := map[worldmap.RoomCoordinate]bool{start: true}
	prev := map[worldmap.RoomCoordinate]worldmap.RoomCoordinate{}
	for len(queue) > 0 && !seen[r.BossCoordinate] {
		c := queue[0]
		queue = queue[1:]
		room, ok := topology.Room(c)
		if !ok {
			t.Fatal("actual path room missing")
		}
		for _, n := range room.Neighbors {
			if !seen[n.Coordinate] {
				seen[n.Coordinate] = true
				prev[n.Coordinate] = c
				queue = append(queue, n.Coordinate)
			}
		}
	}
	if !seen[r.BossCoordinate] {
		t.Fatal("actual boss unreachable")
	}
	path := []worldmap.RoomCoordinate{r.BossCoordinate}
	for path[len(path)-1] != start {
		path = append(path, prev[path[len(path)-1]])
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	clear := func() {
		deadlineAuditV10Event(t, s, p, func() error {
			scene := r.Session.Snapshot().Scene
			for _, ref := range scene.BlockingHostiles {
				var key uint32
				for k, v := range scene.RuntimeObjects {
					if v == ref {
						key = k
						break
					}
				}
				if key == 0 {
					return fmt.Errorf("actual blocker not announced: %+v", ref)
				}
				if _, _, dead := currentDungeonDefeatedActor(r, scene, key); dead {
					continue
				}
				body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
				binary.LittleEndian.PutUint32(body[0:4], key)
				binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(p.selectedCharacterID))
				if err := s.handleDungeonMonsterDeath(p, body); err != nil {
					return err
				}
				scene = r.Session.Snapshot().Scene
			}
			if !r.Session.Snapshot().Scene.Cleared {
				return fmt.Errorf("real op39 did not clear actual room")
			}
			return nil
		})
	}
	for _, c := range path[1:] {
		clear()
		if c.X < 0 || c.X > 255 || c.Y < 0 || c.Y > 255 {
			t.Fatal("actual room outside op45 field bounds")
		}
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = byte(c.X), byte(c.Y)
		deadlineAuditV10Event(t, s, p, func() error {
			if err := s.handleDungeonMoveMap(p, body); err != nil {
				return err
			}
			if r.Session.Snapshot().Scene.Coordinate != c {
				return fmt.Errorf("real op45 did not reach actual neighbor")
			}
			return s.sendFinishLoadingStatus(p, make([]byte, 8))
		})
	}
	var boss uint32
	deadlineAuditV10Event(t, s, p, func() error {
		for _, m := range r.Room.Snapshot().Monsters {
			if normalizeDungeonPVFSymbol(m.Spawn.Rank) == "boss" {
				boss = m.ObjectKey
				break
			}
		}
		if boss == 0 || boss > 65535 {
			return fmt.Errorf("actual announced boss unavailable for117")
		}
		return nil
	})
	clear()
	deadlineAuditV10Event(t, s, p, func() error {
		return s.handleDungeonBossDieCheck(p, bossDieCheckRequestBody(currentSceneActorObjectKey(p.selectedCharacterID), uint16(boss)))
	})
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.bossDieCheckAccepted || !r.bossDieCheckResponseSent || !r.settlementEntrySent {
		t.Fatal("real boss117 did not own completion barriers")
	}
}

func TestRealPVFRaidDeadlinePreacceptedCompletedProjectionOfficialObserved(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	f, p, r, c, q := deadlineLifecycleV14PublicSolo(t, catalog.source.(*platformpvf.Archive))
	s, b := f.service, f.state.Battle
	deadlineLifecycleV14ReachAcceptedBoss(t, s, p, r)
	s.raidMu.Lock()
	accepted, ok := b.ClearedRuns[r.raidEntry.RunID]
	valid := ok && accepted.DungeonID == uint32(r.Dungeon.ID) && b.MemberClears[p.selectedCharacterID] == 1 && !b.TimedOut
	s.raidMu.Unlock()
	if !valid {
		t.Fatal("predeadline real completedrun was not accepted")
	}
	c.reset()
	s.startCurrentRaidClock(p.ctx)
	q.mu.Lock()
	task := q.active[currentRaidClockName]
	q.now = b.Deadline
	q.mu.Unlock()
	if task == nil {
		t.Fatal("actual clock absent")
	}
	q.fire(task, false)
	// Observe the real latest terminal projection, not merely absence of a
	// dedicated queued owner. Generic callbacks are asynchronous/coalesced.
	s.raidMu.Lock()
	terminal := cloneRuntimeRaidState(*f.state)
	version := b.ProgressVersion
	noFlow := b.RewardFlow[b.Phase] == nil
	s.raidMu.Unlock()
	if !terminal.Battle.TimedOut || !noFlow {
		t.Fatal("actual terminal/no-reward-flow contract missing")
	}
	expectedResult, err := prepareCurrentAntonResultProjection(terminal, 1)
	if err != nil || !expectedResult.Failed {
		t.Fatalf("exact actual failure result unavailable: %v", err)
	}
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: p.selectedCharacterID, Generation: p.characterGeneration}
	projectionObserved := func() bool {
		p.raidProjection.mu.Lock()
		defer p.raidProjection.mu.Unlock()
		o := &p.raidProjection
		return !o.failed && o.initialized && o.identity == identity && o.version >= version && o.resultSent && o.resultIdentity == identity
	}
	until := time.Now().Add(createWriteTimeout)
	for !projectionObserved() && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if !projectionObserved() {
		t.Fatal("actual terminal projection/result not observed within original3s")
	}
	var stream []byte
	// Read-only audit through the same existing eventloop. It is queued only
	// after the real callback entered and set its successful result marker.
	// This has no packet/refresh/flush action and does not join all goroutines.
	deadlineAuditV10Event(t, s, p, func() error {
		if !projectionObserved() {
			return fmt.Errorf("observed terminal owner changed before audit")
		}
		if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || p.dungeon.runtime != r {
			return fmt.Errorf("generic callback changed completed runtime")
		}
		stream = c.bytes()
		return nil
	})
	deadlineAuditV10Wait(t, s, p, r, false)
	failureSeen := false
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.Classification == 0 && packet.Header.MsgID == 24 {
			t.Fatal("active deadline sent24 to accepted completedrun")
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == 601 {
			if !bytes.Equal(packet.Body, expectedResult.Body) {
				t.Fatal("terminal601 differs from exact actual failure owner")
			}
			failureSeen = true
		}
		stream = rest
	}
	if !failureSeen {
		t.Fatal("exact failure601 absent after real generic projection audit")
	}
	p.dungeon.mu.Lock()
	same := p.dungeon.runtime == r && !r.townReturnPending && !r.townReturnOp24Sent
	p.dungeon.mu.Unlock()
	if !same || r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("active cleanup changed accepted completedrun")
	}
	s.raidMu.Lock()
	after, exists := b.ClearedRuns[r.raidEntry.RunID]
	count := b.MemberClears[p.selectedCharacterID]
	s.raidMu.Unlock()
	if !exists || !reflect.DeepEqual(after, accepted) || count != 1 {
		t.Fatal("deadline discarded acceptedclear journal")
	}
}

func TestRealPVFRaidDeadlinePublicCharacterResetRebindRejectsOldOwnerObserved(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	f, p, r, c, q := deadlineLifecycleV14PublicSolo(t, catalog.source.(*platformpvf.Archive))
	s, b := f.service, f.state.Battle
	q.mu.Lock()
	q.now = b.Deadline
	q.mu.Unlock()
	_, errs := s.advanceCurrentRaidClocks(q.Now())
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	s.queueCurrentRaidDeadlineReturns(q.Now())
	deadlineAuditV10Wait(t, s, p, r, true)
	var old *currentRaidDeadlineReturn
	deadlineAuditV10Event(t, s, p, func() error {
		old = r.raidDeadlineReturn
		if old == nil || !old.Captured {
			return fmt.Errorf("real deadline owner not captured")
		}
		return s.handleUpperReturnSelectCharacter(p, nil)
	})
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
		t.Fatal("authorized publicop7 did not abandon oldrun")
	}
	deadlineAuditV10Event(t, s, p, func() error { return s.sendUpperCSharpSelectInit(p, make([]byte, 16)) })
	if p.selectedCharacterID != old.Identity.character || s.boundGameSessionCharacterCurrent(old.Identity) {
		t.Fatal("normal op4 did not advance old binding")
	}
	progress := make([]byte, 6)
	progress[0] = currentDungeonTutorialFinalPrefix
	binary.LittleEndian.PutUint32(progress[1:5], currentInitialTownProgress)
	progress[5] = currentDungeonTutorialFinalCommit
	deadlineAuditV10Event(t, s, p, func() error { return s.handleDungeonTutorialFlag(p, progress) })
	p.townMu.Lock()
	before := p.townPositionSnapshot
	p.townMu.Unlock()
	position := make([]byte, currentTownSetUserPositionBodySize)
	binary.LittleEndian.PutUint16(position[:2], before.PositionX)
	binary.LittleEndian.PutUint16(position[2:4], before.PositionY)
	deadlineAuditV10Event(t, s, p, func() error { return s.handleTownSetUserPosition(p, position) })
	if ready, reason := s.currentTownEnterSelectReady(p); !ready {
		t.Fatalf("fresh normaltown not ready:%s", reason)
	}
	p.townMu.Lock()
	before = p.townPositionSnapshot
	p.townMu.Unlock()
	c.reset()
	// Replay only the owner captured by the real previous worker, through the
	// current owning eventloop. Oldrun retirement was authorized op7, not timeout.
	deadlineAuditV10Event(t, s, p, func() error { return s.sendCurrentRaidDeadlineReturn(old) })
	if len(c.bytes()) != 0 {
		t.Fatal("stale oldowner emitted packets into fresh selected scene")
	}
	p.townMu.Lock()
	after := p.townPositionSnapshot
	p.townMu.Unlock()
	p.dungeon.mu.Lock()
	empty := p.dungeon.runtime == nil
	p.dungeon.mu.Unlock()
	if !empty || !reflect.DeepEqual(before, after) || r.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
		t.Fatal("stale owner changed fresh scene")
	}
}
