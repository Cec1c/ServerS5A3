package dnfbridge

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewSemesterOp46AbortRetryDiagnostic(t *testing.T) {
	s, a, campaign := prepareSemesterActualReady(t)
	logPath := filepath.Join(t.TempDir(), "semester-op46-production-events.log")
	logger, logErr := openPacketLogger(logPath)
	if logErr != nil {
		t.Fatal(logErr)
	}
	s.mu.Lock()
	s.packetLog = logger
	s.mu.Unlock()
	defer func() {
		if err := logger.Close(); err != nil {
			t.Logf("DIAG packet logger close=%v", err)
		}
		raw, err := os.ReadFile(logPath)
		t.Logf("DIAG production event log read=%v path=%s\n%s", err, logPath, raw)
	}()
	r := completeSemesterActualRun(t, s, a, true)
	g, _ := s.repositoryGroup()
	fault := errors.New("semester-op46-journal-abort")
	modified := g
	late := &semesterLateClearFault{SemesterClearUnitOfWork: g.SemesterClear, fault: fault}
	modified.SemesterClear = late
	diagnostic := func(stage string) {
		t.Helper()
		s.questCatalogMu.Lock()
		catalogReady, catalogErr := s.questCatalog != nil, s.questCatalogLoadErr
		s.questCatalogMu.Unlock()
		a.dungeon.mu.Lock()
		sameRuntime := a.dungeon.runtime == r
		snapshot := r.Session.Snapshot()
		t.Logf("DIAG stage=%s sameRuntime=%t selected=%d runtimeChar=%s account=%s runtimeAccount=%s gen=%d bossAccepted=%t ordinaryAccepted=%t tutorialPersisted=%t bossResponse=%t entrySent=%t phase=%s runStatus=%s phaseA=%t playReceived=%t planReady=%t completionKey=%s completedAt=%s nonce=%s lateCalled=%d catalogReady=%t catalogErr=%v errCanceled=%t errDeadline=%t", stage, sameRuntime, a.selectedCharacterID, r.Character.CharacterID, a.accountID, r.AccountID, a.characterGeneration, r.bossDieCheckAccepted, r.ordinaryFinalRoomClearAccepted, r.tutorialCompletionPersisted, r.bossDieCheckResponseSent, r.settlementEntrySent, r.settlementPhase.String(), snapshot.Run.Status, r.clearMapCompletionPhaseAPersisted, r.settlementPlayResultReceived, r.settlementResultPlan != nil, r.clearMapCompletionKey, r.clearMapCompletionAt, r.semesterRun.nonce, late.called, catalogReady, catalogErr, errors.Is(catalogErr, context.Canceled), errors.Is(catalogErr, context.DeadlineExceeded))
		a.dungeon.mu.Unlock()
		s.mu.Lock()
		ownerCurrent := s.gameSessions[a.selectedCharacterID] == a
		s.mu.Unlock()
		t.Logf("DIAG stage=%s ownerCurrent=%t fakeNow=%s campaignStarts=%s campaignEnds=%s campaignActiveNow=%t campaignActiveCompletion=%t", stage, ownerCurrent, s.gameplayNow(), campaign.Starts, campaign.Ends, campaign.Active(s.gameplayNow()), campaign.Active(r.clearMapCompletionAt))
		if instance := r.partyInstance; instance != nil {
			instance.mu.Lock()
			t.Logf("DIAG stage=%s partyInstance=%p instanceMembers=%v sceneForSource=%p sourceScene=%p", stage, instance, instance.members, instance.scenes[a.selectedCharacterID], r.Session)
			instance.mu.Unlock()
		} else {
			t.Logf("DIAG stage=%s partyInstance=nil", stage)
		}
	}
	callOp46 := func(request []byte) error {
		t.Helper()
		diagnostic("before-op46")
		err := s.handleDungeonSetPlayResult(a, request)
		t.Logf("DIAG op46Returned=%v canceled=%t deadline=%t", err, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded))
		diagnostic("after-op46")
		return err
	}
	diagnostic("before-boss-fault")
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	if e := s.handleDungeonMonsterDeath(a, semesterActualBossBody(t, r, 99)); e != nil {
		t.Fatal(e)
	}
	diagnostic("after-boss-fault")
	if r.clearMapCompletionPhaseAPersisted {
		t.Fatal("fault fixture committed journal")
	}
	before, _, _ := g.Character.Load(context.Background(), "99")
	request := make([]byte, currentDungeonPlayResultBaseSize)
	if e := callOp46(request); e != nil {
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
	diagnostic("after-direct-invalid-settlement")
	after, _, _ := g.Character.Load(context.Background(), "99")
	if after.Stats["exp"] != before.Stats["exp"] || after.Stats[semesterQualifiedKey(campaign)] != 0 {
		t.Fatal("uncommitted clear consumed settlement/journal")
	}
	key, nonce, at := r.clearMapCompletionKey, r.semesterRun.nonce, r.clearMapCompletionAt
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	diagnostic("provider-restored")
	if e = callOp46(request); e != nil {
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
	if e = callOp46(request); e != nil {
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
