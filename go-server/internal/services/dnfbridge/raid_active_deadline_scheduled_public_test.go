package dnfbridge

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// PREP ONLY: this adds normal scheduled callback/registry assertions atop the
// reviewed V10 public entrance. No production changes or qualification writes.
func TestRealPVFRaidDeadlineScheduledPublicPartyRegistry(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	a := catalog.source.(*platformpvf.Archive)
	f, owners, conns, q := deadlineAuditV10Admit(t, a, false)
	s := f.service
	b := f.state.Battle
	request := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: 210, Difficulty: 2})
	deadlineAuditV10Event(t, s, owners[0], func() error { return s.handleDungeonSelectUpper(owners[0], request) })
	runs := make([]*runtimeDungeonState, 2)
	for i := 0; i < 2; i++ {
		p := owners[i]
		deadlineAuditV10Event(t, s, p, func() error {
			runs[i] = p.dungeon.runtime
			if runs[i] == nil || runs[i].raidEntry == nil || runs[i].Dungeon.ID != 210 {
				return fmt.Errorf("public shared210 runtime missing: %d", i)
			}
			return s.sendFinishLoadingStatus(p, make([]byte, 8))
		})
	}
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		ready := true
		s.raidMu.Lock()
		for i := 0; i < 2; i++ {
			e := b.Participation[owners[i].selectedCharacterID]
			if e.Runtime != runs[i] || e.Session != owners[i] {
				ready = false
			}
		}
		s.raidMu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.raidMu.Lock()
	for i := 0; i < 2; i++ {
		e := b.Participation[owners[i].selectedCharacterID]
		if e.Runtime != runs[i] || e.Session != owners[i] {
			s.raidMu.Unlock()
			t.Fatalf("normal37 placement missing: %d", i)
		}
	}
	s.raidMu.Unlock()
	owners[0].dungeon.mu.Lock()
	instance := runs[0].partyInstance
	owners[0].dungeon.mu.Unlock()
	owners[1].dungeon.mu.Lock()
	same := runs[1].partyInstance == instance
	owners[1].dungeon.mu.Unlock()
	if instance == nil || !same {
		t.Fatal("actual public runs do not share exact instance")
	}
	registry := s.partyDungeonInstanceRegistryForService()
	registry.mu.Lock()
	instance.mu.Lock()
	key := instance.key
	valid := registry.byKey[key] == instance && len(instance.members) == 2 && len(instance.sessions) == 2 && len(instance.scenes) == 2
	instance.mu.Unlock()
	registry.mu.Unlock()
	if !valid {
		t.Fatal("initial shared registry owner/count mismatch")
	}
	party, exists := s.runtimePartyManagerForService().SnapshotByUser(owners[0].selectedCharacterID, 0)
	if !exists {
		t.Fatal("normal party missing before deadline")
	}
	for _, c := range conns {
		c.reset()
	}
	// This invokes the actual production timer callback, rather than calling
	// advance/queue independently. Queue's Now is the exact admitted deadline.
	s.startCurrentRaidClock(owners[0].ctx)
	q.mu.Lock()
	task := q.active[currentRaidClockName]
	q.now = b.Deadline
	q.mu.Unlock()
	if task == nil {
		t.Fatal("production named raid clock not scheduled")
	}
	q.fire(task, false)
	for i := 0; i < 2; i++ {
		deadlineAuditV10Wait(t, s, owners[i], runs[i], true)
	}
	s.raidMu.Lock()
	terminal := b.TimedOut && !b.PhaseCompleted && len(b.ClearedRuns) == 0 && len(b.MemberClears) == 0
	s.raidMu.Unlock()
	if !terminal {
		t.Fatal("scheduled deadline fabricated completion/clear")
	}
	q.mu.Lock()
	next := q.active[currentRaidClockName]
	q.mu.Unlock()
	if next == nil || next == task {
		t.Fatal("actual callback did not reschedule named clock")
	}
	for i := 0; i < 2; i++ {
		if runs[i].Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatal("typed24 alone abandoned active run")
		}
		stream := conns[i].bytes()
		failureAt, returnAt, index := -1, -1, 0
		for len(stream) > 0 {
			p, rest := splitGameServerUpperPacket(t, stream)
			if p.Header.Classification == 0 && p.Header.MsgID == 601 && failureAt < 0 {
				failureAt = index
			}
			if p.Header.Classification == 0 && p.Header.MsgID == 24 && returnAt < 0 {
				returnAt = index
			}
			index++
			stream = rest
		}
		if failureAt < 0 || returnAt <= failureAt {
			t.Fatalf("scheduled failure-before24 missing owner%d: %d/%d", i, failureAt, returnAt)
		}
	}
	for n, i := range []int{1, 0} {
		deadlineAuditV10Event(t, s, owners[i], func() error { return s.sendEnterSelectDungeonState(owners[i], "upper_15", false, true) })
		if runs[i].Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
			t.Fatal("normal scene did not abandon own run")
		}
		owners[i].dungeon.mu.Lock()
		detached := owners[i].dungeon.runtime == nil && runs[i].partyInstance == nil
		owners[i].dungeon.mu.Unlock()
		if !detached {
			t.Fatal("normal scene did not detach own instance")
		}
		registry.mu.Lock()
		instance.mu.Lock()
		_, member := instance.members[owners[i].selectedCharacterID]
		_, session := instance.sessions[owners[i].selectedCharacterID]
		_, scene := instance.scenes[owners[i].selectedCharacterID]
		_, position := instance.positions[owners[i].selectedCharacterID]
		remaining := len(instance.members)
		registered := registry.byKey[key] == instance
		instance.mu.Unlock()
		registry.mu.Unlock()
		if member || session || scene || position || remaining != 1-n || registered != (n == 0) {
			t.Fatalf("exact registry cleanup mismatch scene%d remaining%d registered%v", n, remaining, registered)
		}
		if n == 0 && runs[0].Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatal("first scene abandoned surviving peer")
		}
	}
	current, exists := s.runtimePartyManagerForService().SnapshotByUser(owners[0].selectedCharacterID, 0)
	if !exists || !reflect.DeepEqual(current, party) {
		t.Fatal("automatic scheduled deadline lost retained party")
	}
	s.raidMu.Lock()
	clean := len(b.Participation) == 0 && len(b.ClearedRuns) == 0 && len(b.MemberClears) == 0
	s.raidMu.Unlock()
	if !clean {
		t.Fatal("scheduled cleanup retained journal or fabricated clears")
	}
}
