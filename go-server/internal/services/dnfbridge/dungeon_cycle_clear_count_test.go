package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 艾肯两项已证准入的桥级回归：
//
//	A 试炼前置：5098/3571 需要同服务日通关 5097/3570 三次（真档每日任务文案与 [int data]）；
//	  校验既在 prepare，也在门票/疲劳的同一个事务里；事务内失败不得扣任何资产。
//	B 通关计数：每轮 nonce 幂等；持久化失败**不确认这一轮**（可重试），重试只计一次，
//	  且失败/重试都不能绕过每日上限。

func ekernTicketSlots() map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"0:22": {ItemID: 10158700, Count: 5},
		"0:23": {ItemID: 10161610, Count: 5},
	}
}

func TestCurrentDungeonCycleEkernPrerequisiteBlocksEntryWithoutConsumingTickets(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	t.Setenv("DNFBRIDGE_DUNGEON_TOURNAMENT", "vendor")
	s, repos, _, queue := newDailyCountAtomicTestService(t, path)
	trial, found := s.worldMapTable.FindDungeon(5098)
	if !found {
		t.Fatal("5098 missing from the real PVF")
	}
	prerequisiteDungeon, found := s.worldMapTable.FindDungeon(5097)
	if !found {
		t.Fatal("5097 missing from the real PVF")
	}
	session := seedDungeonDailyCountCharacter(t, repos, "61", 61, "prereq-account")
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "61", Slots: ekernTicketSlots()}); err != nil {
		t.Fatal(err)
	}
	// prepare 阶段就必须拒绝（RED：旧代码没有前置校验，这里会放行）。
	if _, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: 5098, Difficulty: 0}); !errors.Is(err, dnfdungeon.ErrEntryPrerequisiteMissing) {
		t.Fatalf("prepare 5098 without three 5097 clears: %v", err)
	}
	// 事务内复核：同样拒绝，且门票/疲劳一分不扣。
	before := snapshotDailyCountAssets(t, repos, "61")
	assets, err := s.currentDailyCountEntryAssets(session, trial, false)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeDungeonState{Dungeon: trial, lifecycleToken: 11}
	if _, _, err := s.consumeCurrentDungeonCycleEntryAssets(session, runtime, assets); !errors.Is(err, dnfdungeon.ErrEntryPrerequisiteMissing) {
		t.Fatalf("in-transaction prerequisite check: %v", err)
	}
	requireSameAssets(t, before, snapshotDailyCountAssets(t, repos, "61"))
	// 三次 5097 通关之后放行，并真正扣掉门票。
	for i := 0; i < 3; i++ {
		if err := s.consumeCurrentDungeonCycleClearCount(session, &runtimeDungeonState{Dungeon: prerequisiteDungeon, lifecycleToken: uint64(20 + i)}); err != nil {
			t.Fatalf("5097 clear %d: %v", i+1, err)
		}
	}
	if _, _, err := s.consumeCurrentDungeonCycleEntryAssets(session, runtime, assets); err != nil {
		t.Fatalf("5098 entry after the three 5097 clears: %v", err)
	}
	storedBag, _, err := repos.Inventory.Load(context.Background(), "61")
	if err != nil {
		t.Fatal(err)
	}
	for _, itemID := range []int64{10158700, 10161610} {
		if got := itemCountInSlots(storedBag.Slots, itemID); got != 0 {
			t.Fatalf("allowed 5098 entry did not consume ticket %d (left %d)", itemID, got)
		}
	}
	_ = queue
}

func TestCurrentDungeonCycleClearCountRetryIsIdempotentAndCannotBypassLimit(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	s, repos, save, queue := newDailyCountAtomicTestService(t, path)
	dungeon, found := s.worldMapTable.FindDungeon(5097)
	if !found {
		t.Fatal("5097 missing from the real PVF")
	}
	requirement, declared, err := s.currentDungeonCycleRequirementForDungeon(5097)
	if err != nil || !declared {
		t.Fatalf("5097 requirement: declared=%t err=%v", declared, err)
	}
	session := seedDungeonDailyCountCharacter(t, repos, "62", 62, "clear-retry-account")
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
	used := func() int64 {
		t.Helper()
		stored, found, err := repos.Character.Load(context.Background(), "62")
		if err != nil || !found {
			t.Fatalf("load 62: err=%v found=%t", err, found)
		}
		value, err := dnfdungeon.DailyEntryUsed(stored, 5097, queue.now)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	// 失败：不确认这一轮（可重试），也不推进计数。
	runtime := &runtimeDungeonState{Dungeon: dungeon, lifecycleToken: 7}
	save.armed = true
	if err := s.commitCurrentDungeonCycleClearCountLocked(session, runtime); err == nil {
		t.Fatal("clear was confirmed while the count save was failing")
	}
	save.armed = false
	if runtime.dungeonCycleClearCountCommitted {
		t.Fatal("failed clear was marked committed (would never retry)")
	}
	if got := used(); got != 0 {
		t.Fatalf("failed clear advanced the counter to %d", got)
	}
	// 重试成功：只计一次；同一轮再次提交（内存标记在）仍是 1。
	if err := s.commitCurrentDungeonCycleClearCountLocked(session, runtime); err != nil {
		t.Fatalf("retry rejected: %v", err)
	}
	if got := used(); got != 1 {
		t.Fatalf("retry counted %d times, want 1", got)
	}
	if err := s.commitCurrentDungeonCycleClearCountLocked(session, runtime); err != nil {
		t.Fatal(err)
	}
	if got := used(); got != 1 {
		t.Fatalf("re-commit double counted: %d", got)
	}
	// 内存标记丢失后的重放（同一 nonce）也不重复计。
	if err := s.consumeCurrentDungeonCycleClearCount(session, &runtimeDungeonState{Dungeon: dungeon, lifecycleToken: 7}); err != nil {
		t.Fatal(err)
	}
	if got := used(); got != 1 {
		t.Fatalf("nonce replay double counted: %d", got)
	}
	// 新的一轮继续计到上限，之后再计被拒（失败/重试都不能绕过上限）。
	for _, nonce := range []uint64{8, 9} {
		if err := s.consumeCurrentDungeonCycleClearCount(session, &runtimeDungeonState{Dungeon: dungeon, lifecycleToken: nonce}); err != nil {
			t.Fatalf("clear nonce %d: %v", nonce, err)
		}
	}
	if got := used(); got != requirement.DailyCount {
		t.Fatalf("counter=%d want %d", got, requirement.DailyCount)
	}
	if err := s.consumeCurrentDungeonCycleClearCount(session, &runtimeDungeonState{Dungeon: dungeon, lifecycleToken: 10}); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("clear over the daily limit: %v", err)
	}
	if got := used(); got != requirement.DailyCount {
		t.Fatalf("limit rejection changed the counter: %d", got)
	}
	// 入场也被同一额度拒绝。
	if _, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: 5097, Difficulty: 0}); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("entry after the daily limit: %v", err)
	}
	_ = time.Time{}
}
