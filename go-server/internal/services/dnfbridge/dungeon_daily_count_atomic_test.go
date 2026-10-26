package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// DOC-083/094 审阅阻断点：每日次数（异次元裂缝共享池 / 艾肯副本循环）必须与同一轮入场的
// 其他资产"一起提交或一起不提交"。改动前的顺序是「入场房间疲劳 → prepare 阶段门票 → 次数」，
// 次数写失败或被同角色另一个会话抢走最后一次额度时，疲劳/门票已经落库而入场被拒绝。
//
// 本文件的每个子场景都用**真实疲劳**（先断言该副本确实扣疲劳，排除无疲劳 fixture）与真实
// 门票，并在桥级核对 rejected 入场前后的完整角色 Stats 与背包槽位：
//
//	control      ：同一条链成功时确实扣疲劳、扣票、记次数（证明 fixture 不是空转）；
//	save failure ：次数落库失败（与疲劳同一次角色写入）时，三项资产必须全部不变；
//	race         ：prepare 之后由同角色"另一会话"用尽额度，再 commit，必须同样全部不变。
//
// 两类入场都不使用补偿写回或事后返还。

var errInjectedCounterSave = errors.New("injected daily-count save failure")

// injectedCharacterSave 只拦角色的 Save：把"次数（与入场疲劳同一次）写库失败"注入到既有的
// WithinCharacterAssets 事务里，其余读写原样转发给事务内的角色仓储。
type injectedCharacterSave struct {
	dnfrepo.CharacterRepository
	armed bool
	err   error
}

func (s *injectedCharacterSave) Save(ctx context.Context, record dnfrepo.CharacterRecord) error {
	if s.armed {
		return s.err
	}
	return s.CharacterRepository.Save(ctx, record)
}

type injectableCharacterAssets struct {
	inner dnfrepo.CharacterAssetUnitOfWork
	save  *injectedCharacterSave
}

func (u *injectableCharacterAssets) WithinCharacterAssets(ctx context.Context, characterID string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.inner.WithinCharacterAssets(ctx, characterID, func(chars dnfrepo.CharacterRepository, bags dnfrepo.InventoryRepository, equip dnfrepo.EquipmentRepository) error {
		u.save.CharacterRepository = chars
		return apply(u.save, bags, equip)
	})
}

func newDailyCountAtomicTestService(t *testing.T, path string) (*Service, dnfrepo.Group, *injectedCharacterSave, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	adventure, err := adventuregroup.LoadComplete(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	repos := memory.NewMemoryGroup()
	save := &injectedCharacterSave{err: errInjectedCounterSave}
	repos.CharacterAssets = &injectableCharacterAssets{inner: repos.CharacterAssets, save: save}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := &Service{options: options{currentClientVersion: "official-24.9", pvfPath: path, gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, initialEquipmentArchive: a, adventureGroupTable: adventure, gameplayTimers: queue, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	return s, repos, save, queue
}

func itemCountInSlots(slots map[string]dnfrepo.ItemStack, itemID int64) int64 {
	var total int64
	for _, stack := range slots {
		if stack.ItemID == itemID {
			total += stack.Count
		}
	}
	return total
}

type dailyCountAssetSnapshot struct {
	stats map[string]int64
	slots map[string]dnfrepo.ItemStack
}

func snapshotDailyCountAssets(t *testing.T, repos dnfrepo.Group, characterID string) dailyCountAssetSnapshot {
	t.Helper()
	character, found, err := repos.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character %s: err=%v found=%t", characterID, err, found)
	}
	bag, _, err := repos.Inventory.Load(context.Background(), characterID)
	if err != nil {
		t.Fatal(err)
	}
	stats := make(map[string]int64, len(character.Stats))
	for key, value := range character.Stats {
		stats[key] = value
	}
	slots := make(map[string]dnfrepo.ItemStack, len(bag.Slots))
	for key, value := range bag.Slots {
		slots[key] = value
	}
	return dailyCountAssetSnapshot{stats: stats, slots: slots}
}

func (s dailyCountAssetSnapshot) log(t *testing.T, label string) {
	t.Helper()
	t.Logf("%s fatigue=%d slots=%v", label, s.stats["fatigue"], s.slots)
}

func requireSameAssets(t *testing.T, before, after dailyCountAssetSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(before.stats, after.stats) {
		t.Fatalf("character stats changed on a rejected entry:\n before=%v\n after=%v", before.stats, after.stats)
	}
	if !reflect.DeepEqual(before.slots, after.slots) {
		t.Fatalf("inventory changed on a rejected entry:\n before=%v\n after=%v", before.slots, after.slots)
	}
}

type dailyCountAtomicCase struct {
	name      string
	dungeonID uint32
	capacity  int64
	tickets   map[string]dnfrepo.ItemStack
	itemIDs   []int64
	shared    bool
	// fatigue 是该真档副本是否扣入场房间疲劳：真档 16 个异次元裂缝副本都声明 `[no fatigue]`
	//（7100/7165-7179，门票固定 3332 × 1/3/5 按难度），艾肯 5097/5098 扣疲劳。
	fatigue bool
}

// seedEntryAssets 只准备角色与门票，不做 prepare：prepare 阶段的预扣（改动前）必须能在快照里
// 直接观察到，因此不把它塞进同一个 helper。
func (c dailyCountAtomicCase) seedEntryAssets(t *testing.T, s *Service, repos dnfrepo.Group, characterID string, selected uint16, account string) *gameSession {
	t.Helper()
	session := seedDungeonDailyCountCharacter(t, repos, characterID, selected, account)
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: characterID, Slots: c.tickets}); err != nil {
		t.Fatal(err)
	}
	return session
}

func (c dailyCountAtomicCase) prepareEntry(t *testing.T, s *Service, session *gameSession) *runtimeDungeonState {
	t.Helper()
	runtime, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: c.dungeonID, Difficulty: 0})
	if err != nil {
		t.Fatalf("prepare rejected: %v", err)
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func (c dailyCountAtomicCase) used(t *testing.T, repos dnfrepo.Group, characterID string, now time.Time) int64 {
	t.Helper()
	character, found, err := repos.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character %s: err=%v found=%t", characterID, err, found)
	}
	if c.shared {
		used, err := dnfdungeon.DimensionGateDailyUsed(character, now)
		if err != nil {
			t.Fatal(err)
		}
		return used
	}
	used, err := dnfdungeon.DailyEntryUsed(character, int64(c.dungeonID), now)
	if err != nil {
		t.Fatal(err)
	}
	return used
}

// exhaustAllowance 用同角色的另一个 owner/session 口径把额度全部用尽。
func (c dailyCountAtomicCase) exhaustAllowance(t *testing.T, repos dnfrepo.Group, characterID string, now time.Time) {
	t.Helper()
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < c.capacity; i++ {
		if c.shared {
			if _, err := owner.ConsumeDimensionGateEntry(context.Background(), dnfdungeon.DimensionGateEntryCommand{CharacterID: characterID, Limit: c.capacity, UpdatedAt: now}); err != nil {
				t.Fatalf("other session entry %d rejected: %v", i, err)
			}
			continue
		}
		if _, err := owner.ConsumeDailyEntry(context.Background(), dnfdungeon.DailyEntryCommand{CharacterID: characterID, DungeonID: int64(c.dungeonID), Limit: c.capacity, UpdatedAt: now}); err != nil {
			t.Fatalf("other session entry %d rejected: %v", i, err)
		}
	}
}

func TestDailyCountEntryRejectionKeepsFatigueAndTicketsUnchanged(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	// 门票在改动前由 prepare 阶段预扣，因此显式打开既有锦标赛开关，覆盖最严的情况。
	t.Setenv("DNFBRIDGE_DUNGEON_TOURNAMENT", "vendor")

	cases := []dailyCountAtomicCase{
		{
			name: "dimension_7100", dungeonID: 7100, capacity: 5, shared: true, fatigue: false,
			tickets: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 3332, Count: 1}},
			itemIDs: []int64{3332},
		},
		{
			name: "ekern_5097", dungeonID: 5097, capacity: 3, fatigue: true,
			tickets: map[string]dnfrepo.ItemStack{"0:22": {ItemID: 10158700, Count: 5}, "0:23": {ItemID: 10161610, Count: 5}},
			itemIDs: []int64{10158700, 10161610},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s, repos, save, queue := newDailyCountAtomicTestService(t, path)
			dungeon, found := s.worldMapTable.FindDungeon(int64(test.dungeonID))
			if !found {
				t.Fatalf("dungeon %d missing from the real PVF", test.dungeonID)
			}
			if got := currentDungeonUsesNoFatigue(dungeon); got == test.fatigue {
				t.Fatalf("dungeon %d fatigue-free=%t, want %t (real PVF fact)", test.dungeonID, got, !test.fatigue)
			}

			// ── control：成功入场必须真的扣疲劳、扣票、记次数 ─────────────────────────
			controlSession := test.seedEntryAssets(t, s, repos, "19", 19, "atomic-control")
			controlBefore := snapshotDailyCountAssets(t, repos, "19")
			controlBefore.log(t, "control_before")
			controlRuntime, err := enterDungeonOnce(t, s, controlSession, test.dungeonID)
			if err != nil {
				t.Fatalf("control entry rejected: %v", err)
			}
			if err := controlRuntime.Session.Abandon(); err != nil {
				t.Fatal(err)
			}
			controlAfter := snapshotDailyCountAssets(t, repos, "19")
			controlAfter.log(t, "control_after")
			wantFatigue := controlBefore.stats["fatigue"]
			if test.fatigue {
				wantFatigue--
			}
			if controlAfter.stats["fatigue"] != wantFatigue {
				t.Fatalf("control fatigue=%d want %d (fatigue-free=%t)", controlAfter.stats["fatigue"], wantFatigue, !test.fatigue)
			}
			// 异次元裂缝按**入场**计数；艾肯按**通关**计数（真档 `[condition under clear]` 每日任务）。
			wantUsed := int64(0)
			if test.shared {
				wantUsed = 1
			}
			if got := test.used(t, repos, "19", queue.now); got != wantUsed {
				t.Fatalf("control counter=%d want %d after entry", got, wantUsed)
			}
			if !test.shared {
				dungeon, found := s.worldMapTable.FindDungeon(int64(test.dungeonID))
				if !found {
					t.Fatalf("dungeon %d missing from the real PVF", test.dungeonID)
				}
				if err := s.consumeCurrentDungeonCycleClearCount(controlSession, &runtimeDungeonState{Dungeon: dungeon}); err != nil {
					t.Fatalf("control clear rejected: %v", err)
				}
				if got := test.used(t, repos, "19", queue.now); got != 1 {
					t.Fatalf("control counter=%d want 1 after clear", got)
				}
			}
			for _, itemID := range test.itemIDs {
				if got := itemCountInSlots(controlAfter.slots, itemID); got != 0 {
					t.Fatalf("control did not consume ticket %d (left %d)", itemID, got)
				}
			}

			// ── 场景 A：次数落库失败（与疲劳同一次角色写入）────────────────────────────
			saveFailureSession := test.seedEntryAssets(t, s, repos, "40", 40, "atomic-save-failure")
			saveFailureRuntime := test.prepareEntry(t, s, saveFailureSession)
			afterPrepare := snapshotDailyCountAssets(t, repos, "40")
			afterPrepare.log(t, "save_failure_after_prepare")
			for _, itemID := range test.itemIDs {
				if got := itemCountInSlots(afterPrepare.slots, itemID); got == 0 {
					// Errorf 而不是 Fatalf：旧代码在 prepare 阶段就预扣门票，这里要留下一份完整回执
					//（继续跑完后面两个场景，把 commit 阶段的疲劳损失也一起暴露出来）。
					t.Errorf("prepare pre-deducted ticket %d: %v", itemID, afterPrepare.slots)
				}
			}
			save.armed = true
			commitErr := s.commitDungeonRuntime(saveFailureSession, saveFailureRuntime)
			save.armed = false
			if commitErr == nil {
				t.Fatal("commit accepted while the counter save was failing")
			}
			saveFailureAfter := snapshotDailyCountAssets(t, repos, "40")
			saveFailureAfter.log(t, "save_failure_after_commit")
			if saveFailureSession.dungeon.runtime != nil {
				t.Fatal("rejected entry adopted the runtime")
			}
			if got := test.used(t, repos, "40", queue.now); got != 0 {
				t.Fatalf("rejected entry advanced the counter to %d", got)
			}
			requireSameAssets(t, afterPrepare, saveFailureAfter)

			// ── 场景 B：同角色另一会话抢走最后一次额度 ────────────────────────────────
			raceSession := test.seedEntryAssets(t, s, repos, "41", 41, "atomic-race")
			raceRuntime := test.prepareEntry(t, s, raceSession)
			test.exhaustAllowance(t, repos, "41", queue.now)
			raceBefore := snapshotDailyCountAssets(t, repos, "41")
			raceBefore.log(t, "race_before")
			if err := s.commitDungeonRuntime(raceSession, raceRuntime); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
				t.Fatalf("raced entry must be rejected with the daily limit: %v", err)
			}
			raceAfter := snapshotDailyCountAssets(t, repos, "41")
			raceAfter.log(t, "race_after")
			if raceSession.dungeon.runtime != nil {
				t.Fatal("raced entry adopted the runtime")
			}
			requireSameAssets(t, raceBefore, raceAfter)
		})
	}
}
