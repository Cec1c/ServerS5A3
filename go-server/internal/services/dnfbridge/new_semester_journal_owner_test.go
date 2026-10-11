package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"sync"
	"testing"
	"time"
)

type semesterLateClearFault struct {
	repo.SemesterClearUnitOfWork
	fault      error
	cancel     context.CancelFunc
	panicAfter bool
	called     int
}

func (u *semesterLateClearFault) WithinSemesterClear(ctx context.Context, source repo.SemesterClearOwner, owners []repo.SemesterClearOwner, scope string, apply func(repo.Group) error) error {
	return u.SemesterClearUnitOfWork.WithinSemesterClear(ctx, source, owners, scope, func(tx repo.Group) error {
		if e := apply(tx); e != nil {
			return e
		}
		u.called++
		if u.cancel != nil {
			u.cancel()
		}
		if u.panicAfter {
			panic("semester-late-callback-panic")
		}
		return u.fault
	})
}

func semesterActualBossBody(t *testing.T, r *runtimeDungeonState, character uint16) []byte {
	t.Helper()
	for _, m := range r.Room.Snapshot().Monsters {
		if m.Definition.ID == 63851 {
			b := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
			binary.LittleEndian.PutUint32(b, m.ObjectKey)
			binary.LittleEndian.PutUint16(b[4:], character)
			b[30] = 1
			return b
		}
	}
	t.Fatal("real boss missing")
	return nil
}

func TestNewSemesterActualLateJournalAbortStableRetry(t *testing.T) {
	s, a, campaign := prepareSemesterActualReady(t)
	r := completeSemesterActualRun(t, s, a, true)
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("premature actual completion")
	}
	g, _ := s.repositoryGroup()
	fault := errors.New("semester-late-journal-fault")
	modified := g
	late := &semesterLateClearFault{SemesterClearUnitOfWork: g.SemesterClear, fault: fault}
	modified.SemesterClear = late
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	e := s.handleDungeonMonsterDeath(a, semesterActualBossBody(t, r, 99))
	if late.called != 1 || r.clearMapCompletionPhaseAPersisted {
		t.Fatalf("late fault did not leave barrier open err=%v phase=%t", e, r.clearMapCompletionPhaseAPersisted)
	}
	// The existing op39 owner logs/defer-retries PhaseA errors rather than
	// returning them to the transport. The direct retry must preserve the error.
	if e = s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); !errors.Is(e, fault) {
		t.Fatalf("PhaseA fault was not preserved: %v", e)
	}
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !r.bossDieCheckAccepted {
		t.Fatal("did not reach actual completed clear")
	}
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(campaign)] != 0 {
		t.Fatal("late fault journal escaped")
	}
	if _, found, _ := g.Settings.Load(context.Background(), semesterReceiptScope(r.semesterRun.nonce)); found {
		t.Fatal("uncommitted receipt escaped")
	}
	key, at, nonce := r.clearMapCompletionKey, r.clearMapCompletionAt, r.semesterRun.nonce
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	if e = s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e != nil {
		t.Fatal(e)
	}
	if key != r.clearMapCompletionKey || !at.Equal(r.clearMapCompletionAt) || nonce != r.semesterRun.nonce {
		t.Fatal("retry changed owner identity")
	}
	ch, _, _ = g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(campaign)] != 1 {
		t.Fatal("retry lost journal", ch.Stats)
	}
}

func TestNewSemesterActualOwnerRejectsTamperedCompletedRun(t *testing.T) {
	for _, kind := range []string{"request", "dungeon", "account", "generation", "canonical", "missing-boss-receipt"} {
		t.Run(kind, func(t *testing.T) {
			s, a, r, _ := prepareSemesterActualCompleted(t)
			r.clearMapCompletionPhaseAPersisted = false
			switch kind {
			case "request":
				r.Request.DungeonID = 6063
			case "dungeon":
				r.Dungeon.ID = 6063
			case "account":
				r.AccountID = "foreign"
			case "generation":
				s.mu.Lock()
				a.characterGeneration++
				s.mu.Unlock()
			case "canonical":
				s.mu.Lock()
				delete(s.gameSessions, 99)
				s.mu.Unlock()
			case "missing-boss-receipt":
				r.Room = &runtimeDungeonRoom{}
			}
			if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e == nil {
				t.Fatal("tampered run accepted")
			}
			if r.clearMapCompletionPhaseAPersisted {
				t.Fatal("tamper consumed barrier")
			}
		})
	}
}

func TestNewSemesterActualConcurrentPeerReceiptReplay(t *testing.T) {
	s, peers, runtimes, c := prepareSemesterActualPartyCompleted(t)
	var wg sync.WaitGroup
	out := make(chan error, 4)
	for i := range peers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			peers[i].dungeon.mu.Lock()
			defer peers[i].dungeon.mu.Unlock()
			runtimes[i].clearMapCompletionPhaseAPersisted = false
			out <- s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), peers[i], runtimes[i], runtimes[i].Session.Snapshot())
		}(i)
	}
	wg.Wait()
	close(out)
	for e := range out {
		if e != nil {
			t.Fatal(e)
		}
	}
	g, _ := s.repositoryGroup()
	for _, peer := range peers {
		ch, _, _ := g.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if ch.Stats[semesterQualifiedKey(c)] != 1 {
			t.Fatal("concurrent peer duplicate", ch)
		}
	}
}

func TestNewSemesterActualSharedCampaignExpiryNoNewDay(t *testing.T) {
	s, a, r, c := prepareSemesterActualCompleted(t)
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	q.mu.Lock()
	q.now = c.Ends
	q.mu.Unlock()
	next := completeSemesterActualRun(t, s, a)
	if next.semesterRun != nil {
		t.Fatal("expired campaign froze new journal owner")
	}
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 1 {
		t.Fatal("expiry added day")
	}
	if next == r {
		t.Fatal("not new run")
	}
}

func TestNewSemesterActualRebindReadsDurableJournal(t *testing.T) {
	s, a, _, c := prepareSemesterActualCompleted(t)
	g, _ := s.repositoryGroup()
	replacement := &gameSession{conn: &bufferConn{}, accountID: a.accountID, accountFromToken: true, loginPacketSeen: true, channel: a.channel}
	s.bindGameSessionCharacter(replacement, 99)
	ch, _, e := g.Character.Load(context.Background(), "99")
	if e != nil || ch.AccountID != replacement.accountID || ch.Stats[semesterQualifiedKey(c)] != 1 {
		t.Fatal("rebind lost physical journal", e, ch)
	}
	if c.Ends.Sub(c.Starts) != 28*24*time.Hour {
		t.Fatal("campaign policy")
	}
}

var _ = oe.ChinaCalendar

func TestNewSemesterActualParticipantAbandonedBeforeClearExcluded(t *testing.T) {
	s, peers, runtimes, campaign := prepareSemesterActualPartyCompleted(t, func(s *Service, peers []*gameSession, runtimes []*runtimeDungeonState) {
		if e := runtimes[3].Session.Abandon(); e != nil {
			t.Fatal(e)
		}
	})
	if runtimes[3].Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
		t.Fatal("not actual abandon")
	}
	g, _ := s.repositoryGroup()
	for i, peer := range peers {
		ch, _, e := g.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if e != nil {
			t.Fatal(e)
		}
		want := int64(1)
		if i == 3 {
			want = 0
		}
		if ch.Stats[semesterQualifiedKey(campaign)] != want {
			t.Fatalf("abandon participant %d day=%d want%d", peer.selectedCharacterID, ch.Stats[semesterQualifiedKey(campaign)], want)
		}
	}
	if len(runtimes[0].semesterRun.receipt.Owners) != 3 {
		t.Fatal("abandoned member in receipt")
	}
}

func TestNewSemesterActualPartyListOnlyMemberExcluded(t *testing.T) {
	s, _, runtimes, _ := prepareSemesterActualPartyCompleted(t, func(s *Service, peers []*gameSession, runtimes []*runtimeDungeonState) {
		for _, peer := range peers {
			state := runtimePartyStateSnapshot(peer)
			state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: 103, UserState: 1})
			storeRuntimePartyState(peer, state)
		}
	})
	if len(runtimes[0].semesterRun.receipt.Owners) != 4 {
		t.Fatal("party list mutated frozen actual participants")
	}
	g, _ := s.repositoryGroup()
	if _, found, e := g.Character.Load(context.Background(), "103"); e != nil || found {
		t.Fatal("list-only participant created", e)
	}
}

func TestNewSemesterActualFreshNonceAtSameClockAndSeed(t *testing.T) {
	s, a, first, c := prepareSemesterActualCompleted(t)
	second := completeSemesterActualRun(t, s, a)
	if first.Seed != second.Seed || !first.clearMapCompletionAt.Equal(second.clearMapCompletionAt) {
		t.Fatal("collision fixture not same seed and time")
	}
	if first.semesterRun.nonce == second.semesterRun.nonce || first.clearMapCompletionKey == second.clearMapCompletionKey {
		t.Fatal("new actual run reused nonce or completion key")
	}
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 1 {
		t.Fatal("same-day fresh nonce duplicated days")
	}
}

func TestNewSemesterActualIncompleteClearCannotJournal(t *testing.T) {
	s, a, c := prepareSemesterActualReady(t)
	r := completeSemesterActualRun(t, s, a, true)
	if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e == nil {
		t.Fatal("uncleared boss accepted")
	}
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 0 || r.clearMapCompletionPhaseAPersisted {
		t.Fatal("incomplete clear journal authorized")
	}
	if _, known, _ := g.Settings.Load(context.Background(), semesterReceiptScope(r.semesterRun.nonce)); known {
		t.Fatal("incomplete clear physical receipt exists")
	}
}

func TestNewSemesterActualClockBeforeCampaignActivationNoCount(t *testing.T) {
	s, a, c := prepareSemesterActualReady(t)
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	q.mu.Lock()
	q.now = c.CreatedAt.Add(-time.Minute)
	q.mu.Unlock()
	r := completeSemesterActualRun(t, s, a)
	if r.semesterRun != nil {
		t.Fatal("backward clock froze eligibility")
	}
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 0 {
		t.Fatal("clock before activation added day")
	}
}

func TestNewSemesterActualJournalSendsNoUnprovedProjection(t *testing.T) {
	_, a, _, _ := prepareSemesterActualCompleted(t)
	for _, frame := range splitUpperFrames(t, a.conn.(*bufferConn).write.Bytes()) {
		if frame.msgID == 654 || frame.msgID == 688 || frame.msgID == 689 {
			t.Fatalf("journal emitted unproved left packet%d", frame.msgID)
		}
	}
}

type semesterUnknownAfterCommit struct{ repo.SemesterClearUnitOfWork }

func (u semesterUnknownAfterCommit) WithinSemesterClear(ctx context.Context, source repo.SemesterClearOwner, owners []repo.SemesterClearOwner, scope string, apply func(repo.Group) error) error {
	if e := u.SemesterClearUnitOfWork.WithinSemesterClear(ctx, source, owners, scope, apply); e != nil {
		return e
	}
	return repo.ErrSemesterClearCommitUnknown
}

// This is an explicit simulated durable-commit/unknown-result boundary, not a
// claim to have caused a real MySQL network failure. The SQL driver test covers
// database/sql's actual Commit-error / TxDone behavior separately.
func TestNewSemesterActualUnknownCommitSameNonceResolvesCommittedReceipt(t *testing.T) {
	s, a, c := prepareSemesterActualReady(t)
	r := completeSemesterActualRun(t, s, a, true)
	g, _ := s.repositoryGroup()
	modified := g
	modified.SemesterClear = semesterUnknownAfterCommit{g.SemesterClear}
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	if e := s.handleDungeonMonsterDeath(a, semesterActualBossBody(t, r, 99)); e != nil {
		t.Fatal(e)
	}
	if r.clearMapCompletionPhaseAPersisted {
		t.Fatal("unknown result closed barrier")
	}
	ch, _, _ := g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 1 {
		t.Fatal("simulation did not durably commit")
	}
	if _, found, _ := g.Settings.Load(context.Background(), semesterReceiptScope(r.semesterRun.nonce)); !found {
		t.Fatal("simulation missing durable receipt")
	}
	nonce, at := r.semesterRun.nonce, r.clearMapCompletionAt
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot()); e != nil {
		t.Fatal(e)
	}
	ch, _, _ = g.Character.Load(context.Background(), "99")
	if ch.Stats[semesterQualifiedKey(c)] != 1 || nonce != r.semesterRun.nonce || !at.Equal(r.clearMapCompletionAt) {
		t.Fatal("unknown-result retry reused new owner or double counted")
	}
}

func TestNewSemesterActualReleasedMemberBeforeBossExcluded(t *testing.T) {
	s, peers, runtimes, c := prepareSemesterActualPartyCompleted(t, func(s *Service, peers []*gameSession, runtimes []*runtimeDungeonState) {
		s.releasePartyDungeonInstance(runtimes[3], peers[3].selectedCharacterID, "member_left_before_actual_boss")
	})
	if runtimes[3].Session.Snapshot().Run.Status == worldmap.DungeonRunAbandoned {
		t.Fatal("release fixture substituted abandonment")
	}
	g, _ := s.repositoryGroup()
	for i, peer := range peers {
		ch, _, _ := g.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		want := int64(1)
		if i == 3 {
			want = 0
		}
		if ch.Stats[semesterQualifiedKey(c)] != want {
			t.Fatalf("released member%d count%d want%d", peer.selectedCharacterID, ch.Stats[semesterQualifiedKey(c)], want)
		}
	}
	if len(runtimes[0].semesterRun.receipt.Owners) != 3 {
		t.Fatal("retained old scene authorized released member")
	}
}

func TestNewSemesterActualFrozenContributionSurvivesDisconnectAfterAbort(t *testing.T) {
	var original repo.Group
	late := &semesterLateClearFault{fault: errors.New("party-late-abort")}
	s, peers, runtimes, c := prepareSemesterActualPartyCompleted(t, func(s *Service, peers []*gameSession, runtimes []*runtimeDungeonState) {
		original, _ = s.repositoryGroup()
		late.SemesterClearUnitOfWork = original.SemesterClear
		modified := original
		modified.SemesterClear = late
		s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	})
	if late.called < 1 || len(runtimes[0].semesterRun.receipt.Owners) != 4 {
		t.Fatal("no actual frozen party late-failure receipt")
	}
	for i, peer := range peers {
		if runtimes[i].clearMapCompletionPhaseAPersisted {
			t.Fatal("failed phase closed")
		}
		ch, _, _ := original.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if ch.Stats[semesterQualifiedKey(c)] != 0 {
			t.Fatal("party staged journal escaped")
		}
	}
	if _, known, _ := original.Settings.Load(context.Background(), semesterReceiptScope(runtimes[0].semesterRun.nonce)); known {
		t.Fatal("failed party receipt physically existed")
	}
	s.mu.Lock()
	delete(s.gameSessions, peers[3].selectedCharacterID)
	s.mu.Unlock()
	s.repositoryProvider = func() (repo.Group, bool) { return original, true }
	if e := s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), peers[0], runtimes[0], runtimes[0].Session.Snapshot()); e != nil {
		t.Fatal(e)
	}
	for _, peer := range peers {
		ch, _, _ := original.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if ch.Stats[semesterQualifiedKey(c)] != 1 {
			t.Fatal("frozen completed contribution lost after disconnect", ch)
		}
	}
}
