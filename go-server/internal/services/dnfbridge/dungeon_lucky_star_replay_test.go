package dnfbridge

import (
	"context"
	"errors"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"strconv"
	"testing"
)

func newLuckyStarSettlementReplayTest(t *testing.T, repos dnfrepo.Group) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, []byte) {
	t.Helper()
	s, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
	s.options.accountID = "lucky-replay"
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	runtime.Character.Level = 1
	runtime.entryCharacterLevel = 1
	runtime.ordinaryFinalRoomClearAccepted = true
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: characterID, AccountID: "lucky-replay", Level: 1, Stats: map[string]int64{"exp": 0}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{CharacterID: characterID, Points: dnfrepo.SkillPointState{SyncedLevel: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: characterID, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	runtime.Dungeon.Metadata.RecommendedLevels = []int64{1, 10}
	plan := currentDungeonSettlementPacketPlanForTest(t, runtime, 73)
	runtime.settlementResultPlan = &plan
	body := make([]byte, currentDungeonPlayResultBaseSize)
	body[currentDungeonPlayResultClientRankPointOffset] = 73
	conn.write.Reset()
	return s, runtime, session, conn, body
}

// MR2 commits the wallet with settlement before the frozen packet plan is sent.
// Exercise that durable boundary explicitly; a send-only fixture must not award.
func commitLuckyStarReplay(t *testing.T, s *Service, runtime *runtimeDungeonState, session *gameSession, key string) error {
	t.Helper()
	repos, _ := s.repositoryGroup()
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return err
	}
	var policy *dnfdungeon.LuckyStarPolicy
	if currentDungeonIsRecommendedClear(runtime) {
		policy = &dnfdungeon.LuckyStarPolicy{Limit: 400, Amount: 1}
	}
	result, err := owner.CommitSettlement(context.Background(), dnfdungeon.SettlementCommand{
		AccountID: "lucky-replay", CharacterID: strconv.FormatUint(uint64(session.selectedCharacterID), 10),
		CompletionKey: key, Tables: currentDungeonSettlementTestResources(t).Progression, Experience: 1,
		RecommendedDungeonClear: currentDungeonIsRecommendedClear(runtime), LuckyStar: policy,
	})
	if err == nil {
		runtime.settlementLuckyStar = result.LuckyStar
	}
	return err
}

func luckyStarReplayAccount(t *testing.T, repos dnfrepo.Group, points uint32) {
	t.Helper()
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "lucky-replay", Metadata: map[string]string{dnfdungeon.LuckyStarMetadataKey: strconv.FormatUint(uint64(points), 10)}}); err != nil {
		t.Fatal(err)
	}
}
func assertLuckyStarReplayBalance(t *testing.T, repos dnfrepo.Group, want uint32) {
	t.Helper()
	a, found, err := repos.Account.Load(context.Background(), "lucky-replay")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if got := a.Metadata[dnfdungeon.LuckyStarMetadataKey]; got != strconv.FormatUint(uint64(want), 10) {
		t.Fatalf("lucky stars=%s want=%d", got, want)
	}
}

func TestSettlementLuckyStarReplayAwardsOncePerRun(t *testing.T) {
	repos := memory.NewMemoryGroup()
	luckyStarReplayAccount(t, repos, 10)
	s, runtime, session, conn, body := newLuckyStarSettlementReplayTest(t, repos)
	if err := commitLuckyStarReplay(t, s, runtime, session, "clear-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 11)
	firstWrite := conn.write.Len()
	for i := 0; i < 3; i++ {
		if err := commitLuckyStarReplay(t, s, runtime, session, "clear-1"); err != nil {
			t.Fatal(err)
		}
		if err := s.handleDungeonSetPlayResult(session, body); err != nil {
			t.Fatal(err)
		}
	}
	assertLuckyStarReplayBalance(t, repos, 11)
	if conn.write.Len() != firstWrite {
		t.Fatal("completed replay reissued lucky-star notices")
	}
	next, nextRuntime, nextSession, _, nextBody := newLuckyStarSettlementReplayTest(t, repos)
	if err := commitLuckyStarReplay(t, next, nextRuntime, nextSession, "clear-2"); err != nil {
		t.Fatal(err)
	}
	if err := next.handleDungeonSetPlayResult(nextSession, nextBody); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 12)
}

type luckyStarReplaySaveFailure struct {
	dnfrepo.AccountRepository
	fail bool
}

func (r *luckyStarReplaySaveFailure) Save(ctx context.Context, a dnfrepo.AccountRecord) error {
	if r.fail {
		return errors.New("injected account save failure")
	}
	return r.AccountRepository.Save(ctx, a)
}
func TestSettlementLuckyStarRetryAfterSaveFailure(t *testing.T) {
	repos := memory.NewMemoryGroup()
	luckyStarReplayAccount(t, repos, 10)
	failing := &luckyStarReplaySaveFailure{AccountRepository: repos.Account, fail: true}
	repos.CharacterSettlement = luckyStarReplayFailingAssets{repos.CharacterSettlement, failing}
	s, runtime, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
	if err := commitLuckyStarReplay(t, s, runtime, session, "clear-retry"); err == nil {
		t.Fatal("failed settlement was accepted")
	}
	assertLuckyStarReplayBalance(t, repos, 10)
	failing.fail = false
	for i := 0; i < 3; i++ {
		if err := commitLuckyStarReplay(t, s, runtime, session, "clear-retry"); err != nil {
			t.Fatal(err)
		}
		if err := s.handleDungeonSetPlayResult(session, body); err != nil {
			t.Fatal(err)
		}
	}
	assertLuckyStarReplayBalance(t, repos, 11)
}
func TestSettlementLuckyStarNotificationFailureDoesNotReaward(t *testing.T) {
	repos := memory.NewMemoryGroup()
	luckyStarReplayAccount(t, repos, 10)
	s, runtime, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
	if err := commitLuckyStarReplay(t, s, runtime, session, "clear-send-failure"); err != nil {
		t.Fatal(err)
	}
	session.conn = &failNthDungeonWriteConn{failAt: 4, err: errors.New("injected lucky-star notice failure")}
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 11)
	session.conn = &bufferConn{}
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 11)
}
func TestSettlementLuckyStarCapResolvesOriginalRun(t *testing.T) {
	repos := memory.NewMemoryGroup()
	luckyStarReplayAccount(t, repos, 400)
	s, runtime, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
	if err := commitLuckyStarReplay(t, s, runtime, session, "clear-cap"); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 400)
	// Spending afterwards does not make the earlier capped clear grantable again.
	luckyStarReplayAccount(t, repos, 399)
	if err := commitLuckyStarReplay(t, s, runtime, session, "clear-cap"); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 399)
}
func TestSettlementLuckyStarUnsuitableDungeonDoesNotAward(t *testing.T) {
	repos := memory.NewMemoryGroup()
	luckyStarReplayAccount(t, repos, 10)
	s, runtime, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
	runtime.Dungeon.Metadata.RecommendedLevels = []int64{20, 30}
	if err := commitLuckyStarReplay(t, s, runtime, session, "clear-unsuitable"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.handleDungeonSetPlayResult(session, body); err != nil {
			t.Fatal(err)
		}
	}
	assertLuckyStarReplayBalance(t, repos, 10)
}

type luckyStarReplayFailingAssets struct {
	dnfrepo.CharacterSettlementUnitOfWork
	failure *luckyStarReplaySaveFailure
}

func (u luckyStarReplayFailingAssets) WithinCharacterSettlement(ctx context.Context, character string, apply func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, character, func(tx dnfrepo.Group) error {
		tx.Account = &luckyStarReplaySaveFailure{AccountRepository: tx.Account, fail: u.failure.fail}
		return apply(tx)
	})
}
