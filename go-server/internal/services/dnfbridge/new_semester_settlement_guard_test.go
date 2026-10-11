package dnfbridge

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestNewSemesterActualOp46CannotSettleBeforeJournal(t *testing.T) {
	s, a, campaign := prepareSemesterActualReady(t)
	r := completeSemesterActualRun(t, s, a, true)
	g, _ := s.repositoryGroup()
	fault := errors.New("semester-op46-journal-abort")
	modified := g
	modified.SemesterClear = &semesterLateClearFault{SemesterClearUnitOfWork: g.SemesterClear, fault: fault}
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	if e := s.handleDungeonMonsterDeath(a, semesterActualBossBody(t, r, 99)); e != nil {
		t.Fatal(e)
	}
	if r.clearMapCompletionPhaseAPersisted {
		t.Fatal("fault fixture committed journal")
	}
	before, _, _ := g.Character.Load(context.Background(), "99")
	request := make([]byte, currentDungeonPlayResultBaseSize)
	if e := s.handleDungeonSetPlayResult(a, request); e != nil {
		t.Fatal(e)
	}
	if r.settlementPlayResultReceived || r.settlementResultPlan != nil {
		t.Fatal("op46 accepted uncommitted semester journal")
	}
	resources, e := s.currentDungeonSettlementRewardResources(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1); !errors.Is(e, errCurrentDungeonSettlementStateInvalid) {
		t.Fatalf("direct settlement bypassed journal barrier: %v", e)
	}
	after, _, _ := g.Character.Load(context.Background(), "99")
	if after.Stats["exp"] != before.Stats["exp"] || after.Stats[semesterQualifiedKey(campaign)] != 0 {
		t.Fatal("uncommitted clear consumed settlement/journal")
	}
	key, nonce, at := r.clearMapCompletionKey, r.semesterRun.nonce, r.clearMapCompletionAt
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	if e = s.handleDungeonSetPlayResult(a, request); e != nil {
		t.Fatal(e)
	}
	if !r.clearMapCompletionPhaseAPersisted {
		t.Fatal("op46 retry did not commit pending journal")
	}
	if key != r.clearMapCompletionKey || nonce != r.semesterRun.nonce || !at.Equal(r.clearMapCompletionAt) {
		t.Fatal("retry changed clear identity")
	}
	// The actual op46 retry must authorize the real resource-derived gain once.
	actualGain, e := currentDungeonClearExperience(r, resources)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.handleDungeonSetPlayResult(a, request); e != nil {
		t.Fatal(e)
	}
	// Direct-owner replays must also recognize the existing completion receipt.
	first, e := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
	if e != nil {
		t.Fatal(e)
	}
	_ = first
	_ = second
	final, _, _ := g.Character.Load(context.Background(), "99")
	if final.Stats[semesterQualifiedKey(campaign)] != 1 {
		t.Fatal("retry did not retain one qualified day")
	}
	if final.Stats["exp"] != before.Stats["exp"]+int64(actualGain) {
		t.Fatalf("settlement replay changed exp: before=%d final=%d", before.Stats["exp"], final.Stats["exp"])
	}
}
