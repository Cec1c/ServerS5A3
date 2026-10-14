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
	runtime.Character.Level = 5
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: strconv.FormatUint(uint64(session.selectedCharacterID), 10), AccountID: "lucky-replay", Level: 5}); err != nil {
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
	s, _, session, conn, body := newLuckyStarSettlementReplayTest(t, repos)
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 11)
	firstWrite := conn.write.Len()
	for i := 0; i < 3; i++ {
		if err := s.handleDungeonSetPlayResult(session, body); err != nil {
			t.Fatal(err)
		}
	}
	assertLuckyStarReplayBalance(t, repos, 11)
	if conn.write.Len() != firstWrite {
		t.Fatal("completed replay reissued lucky-star notices")
	}
	next, _, nextSession, _, nextBody := newLuckyStarSettlementReplayTest(t, repos)
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
	repos.RentalAssets = luckyStarReplayFailingAssets{repos.RentalAssets, failing}
	s, _, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, 10)
	failing.fail = false
	for i := 0; i < 3; i++ {
		if err := s.handleDungeonSetPlayResult(session, body); err != nil {
			t.Fatal(err)
		}
	}
	assertLuckyStarReplayBalance(t, repos, 11)
}
func TestSettlementLuckyStarNotificationFailureDoesNotReaward(t *testing.T) {
	repos := memory.NewMemoryGroup()
	luckyStarReplayAccount(t, repos, 10)
	s, _, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
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
	luckyStarReplayAccount(t, repos, dnfdungeon.LuckyStarMaximum)
	s, _, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, dnfdungeon.LuckyStarMaximum)
	// Spending afterwards does not make the earlier capped clear grantable again.
	luckyStarReplayAccount(t, repos, dnfdungeon.LuckyStarMaximum-1)
	if err := s.handleDungeonSetPlayResult(session, body); err != nil {
		t.Fatal(err)
	}
	assertLuckyStarReplayBalance(t, repos, dnfdungeon.LuckyStarMaximum-1)
}
func TestSettlementLuckyStarUnsuitableDungeonDoesNotAward(t *testing.T) {
	repos := memory.NewMemoryGroup()
	luckyStarReplayAccount(t, repos, 10)
	s, runtime, session, _, body := newLuckyStarSettlementReplayTest(t, repos)
	runtime.Dungeon.Metadata.RecommendedLevels = []int64{20, 30}
	for i := 0; i < 2; i++ {
		if err := s.handleDungeonSetPlayResult(session, body); err != nil {
			t.Fatal(err)
		}
	}
	assertLuckyStarReplayBalance(t, repos, 10)
}

type luckyStarReplayFailingAssets struct {
	dnfrepo.RentalAssetUnitOfWork
	failure *luckyStarReplaySaveFailure
}

func (u luckyStarReplayFailingAssets) WithinRentalAssets(ctx context.Context, account, character string, apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.RentalAssetUnitOfWork.WithinRentalAssets(ctx, account, character, func(a dnfrepo.AccountRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		return apply(&luckyStarReplaySaveFailure{AccountRepository: a, fail: u.failure.fail}, c, i, e)
	})
}
