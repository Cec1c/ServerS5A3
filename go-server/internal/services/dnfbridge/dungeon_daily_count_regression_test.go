package dnfbridge

import (
	"context"
	"errors"
	"os"
	"strings"
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

// DOC-083/094 回归：异次元裂缝（`[dimension dungeon]` 共享角色日计数）与艾肯副本循环
// （`etc/dungeoncyclesystem.etc`）的每日入场次数必须被校验并落库。
//
// 真档 PVF dungeon ids（`worldmap/DimensionGate.wdm` 区域 13 / `worldmap/Legend.wdm` 区域 45）：
//
//	7100  异次元裂缝 - 异界的强者      (DOC-083 上报副本)
//	7165-7179 黑色大地外围 / 黑色大地 / 罗特斯的堡垒 / 巴卡尔之城 / 痛苦之村列瑟芬
//	5097/5098 艾肯（单人）遗落的神殿 / 试炼；3570/3571 组队版 (DOC-094)
//	316   魔界裂缝（`particular hell`，**不是** dimension dungeon；沿用 3 次规则，不得混淆）

func newDungeonDailyCountTestService(t *testing.T, path string) (*Service, dnfrepo.Group, *fakeCurrentDungeonDeathTimerQueue) {
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
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := &Service{options: options{currentClientVersion: "official-24.9", pvfPath: path, gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, initialEquipmentArchive: a, adventureGroupTable: adventure, gameplayTimers: queue, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	return s, repos, queue
}

func seedDungeonDailyCountCharacter(t *testing.T, repos dnfrepo.Group, id string, selected uint16, account string) *gameSession {
	t.Helper()
	character := dnfrepo.CharacterRecord{CharacterID: id, AccountID: account, Level: 90, Stats: map[string]int64{"town_id": 107, "area_id": 0, "pos_x": 250, "pos_y": 224, "direction": 5, "area_state": 3, "fatigue": 100}}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{selectedCharacterID: selected, accountID: account, conn: &bufferConn{}, connID: "daily-count-test"}
	return session
}

// enterDungeonOnce 走与线上同一条 prepare → freeze → commit 链，并在返回前放弃运行时，
// 让下一次进入按"新的一轮"处理。
func enterDungeonOnce(t *testing.T, s *Service, session *gameSession, dungeonID uint32) (*runtimeDungeonState, error) {
	t.Helper()
	runtime, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: dungeonID, Difficulty: 0})
	if err != nil {
		return nil, err
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatal(err)
	}
	if err := s.commitDungeonRuntime(session, runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func TestRealPVFDimensionDungeonSharedDailyCounterAndEkernCycleLimits(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	s, repos, queue := newDungeonDailyCountTestService(t, path)
	ctx := context.Background()

	// 真档结构证据：区域 13 的 16 个副本都带 `[dimension dungeon]`，魔界裂缝 316 不带。
	dimensionIDs := []int64{7100, 7165, 7166, 7167, 7168, 7169, 7170, 7171, 7172, 7173, 7174, 7175, 7176, 7177, 7178, 7179}
	for _, id := range dimensionIDs {
		dungeon, ok := s.worldMapTable.FindDungeon(id)
		if !ok {
			t.Fatalf("real PVF dungeon %d is missing", id)
		}
		if !s.currentDimensionGateDailyEntryEnabled(dungeon) {
			t.Fatalf("dungeon %d is not gated by the dimension daily count", id)
		}
		// 真档事实：16 个异次元裂缝副本全部 `[no fatigue]`，门票据 `[required item]` 固定为 3332
		// 且按难度档位 1/3/5（对应 etc/dungeonetc/dimensiondungeoninfo.etc 的
		// `[num of require item by difficult] 0 1 1 3 2 5`）。次数与门票的分摊由本条与桥侧
		// 原子提交测试共同钉住。
		if !dungeon.Metadata.NoFatigue {
			t.Fatalf("dimension dungeon %d must stay fatigue-free per the real PVF", id)
		}
		required := currentTournamentEntryItems(dungeon)
		if len(required) != 1 || required[0].ItemID != 3332 || !required[0].ConsumeOnEntry {
			t.Fatalf("dimension dungeon %d required items=%+v, want one consuming 3332 ticket", id, required)
		}
		if required[0].Count != 1 && required[0].Count != 3 && required[0].Count != 5 {
			t.Fatalf("dimension dungeon %d ticket count=%d, want the published 1/3/5 difficulty ladder", id, required[0].Count)
		}
	}
	for _, id := range []int64{316, 5097, 5098, 3570, 3571} {
		dungeon, ok := s.worldMapTable.FindDungeon(id)
		if !ok {
			t.Fatalf("real PVF dungeon %d is missing", id)
		}
		if s.currentDimensionGateDailyEntryEnabled(dungeon) {
			t.Fatalf("dungeon %d must not use the dimension daily pool", id)
		}
	}

	limit, source, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		t.Fatal(err)
	}
	if limit != 5 || source != currentDimensionGateEntryLimitSourceVendorPVF {
		t.Fatalf("real etc/dimensiongate.etc limit=%d source=%s, want 5 from the vendor PVF", limit, source)
	}

	// 一、异次元裂缝：共享角色池，7165 与 7100 记同一份计数。
	session := seedDungeonDailyCountCharacter(t, repos, "19", 19, "dimension-account")
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
	for i := int64(1); i <= limit; i++ {
		// 交替进入两个不同的异次元裂缝副本，证明计数按角色共享而不是按副本各记一份。
		entryID := uint32(7100)
		if i%2 == 0 {
			entryID = 7165
		}
		runtime, err := enterDungeonOnce(t, s, session, entryID)
		if err != nil {
			t.Fatalf("dimension entry %d (dungeon %d) rejected: %v", i, entryID, err)
		}
		if err := runtime.Session.Abandon(); err != nil {
			t.Fatal(err)
		}
		stored, _, err := repos.Character.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		used, err := dnfdungeon.DimensionGateDailyUsed(stored, queue.now)
		if err != nil || used != i {
			t.Fatalf("dimension entry %d persisted used=%d err=%v", i, used, err)
		}
	}
	if _, err := enterDungeonOnce(t, s, session, 7171); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("dimension pool accepted entry %d on another rift dungeon: %v", limit+1, err)
	}

	// 二、艾肯：按副本各记一份；额度取 etc/dungeoncyclesystem.etc 的真档数据。
	expected := map[int64]int64{5097: 3, 5098: 1, 3570: 3, 3571: 1}
	for id, want := range expected {
		got, declared, err := s.currentDungeonCycleDailyCountForDungeon(id)
		if err != nil || !declared || got != want {
			t.Fatalf("dungeon cycle %d limit=%d declared=%t err=%v, want %d", id, got, declared, err, want)
		}
	}
	// 2100/316 等其它副本没有被这张表声明。
	if _, declared, err := s.currentDungeonCycleDailyCountForDungeon(316); err != nil || declared {
		t.Fatalf("316 must not be declared by the dungeon cycle table: declared=%t err=%v", declared, err)
	}

	bag := dnfrepo.InventoryRecord{CharacterID: "31", Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 10158700, Count: 5}, "0:21": {ItemID: 10161610, Count: 5}}}
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	ekern := seedDungeonDailyCountCharacter(t, repos, "31", 31, "ekern-account")
	bindDungeonSelectorOriginForTestAt(t, s, ekern, 107, 0, 250, 224)
	for i := int64(1); i <= expected[5097]; i++ {
		runtime, err := enterDungeonOnce(t, s, ekern, 5097)
		if err != nil {
			t.Fatalf("5097 entry %d rejected: %v", i, err)
		}
		if err := runtime.Session.Abandon(); err != nil {
			t.Fatal(err)
		}
		stored, _, err := repos.Character.Load(ctx, "31")
		if err != nil {
			t.Fatal(err)
		}
		used, err := dnfdungeon.DailyEntryUsed(stored, 5097, queue.now)
		if err != nil || used != i {
			t.Fatalf("5097 entry %d persisted used=%d err=%v", i, used, err)
		}
	}
	if _, err := enterDungeonOnce(t, s, ekern, 5097); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("5097 accepted entry %d over the declared cycle limit: %v", expected[5097]+1, err)
	}
	// 同区的试炼额度独立：5097 用尽不影响 5098。
	storedEkern, _, err := repos.Character.Load(ctx, "31")
	if err != nil {
		t.Fatal(err)
	}
	if used, err := dnfdungeon.DailyEntryUsed(storedEkern, 5098, queue.now); err != nil || used != 0 {
		t.Fatalf("5098 must keep its own counter: used=%d err=%v", used, err)
	}
	if err := s.validateCurrentDungeonCycleDailyCount(storedEkern, 5098, expected[5098]); err != nil {
		t.Fatalf("5098 must keep its own allowance: %v", err)
	}
	// 只落计数器，不重复扣票。
	storedBag, _, err := repos.Inventory.Load(ctx, "31")
	if err != nil {
		t.Fatal(err)
	}
	if storedBag.Slots["0:20"].Count != 5 || storedBag.Slots["0:21"].Count != 5 {
		t.Fatalf("cycle counter consumed entry tickets: %+v", storedBag.Slots)
	}
	t.Logf("real PVF: dimension pool 5/day shared by 7100+7165, rejected on 7171; 5097 3/day persisted and rejected, tickets untouched")
}

func TestCurrentDungeonCycleDailyLimitsParseAndFailClosed(t *testing.T) {
	limits, declared, err := parseCurrentDungeonCycleDailyLimits("")
	if err != nil || !declared || len(limits) != 0 {
		t.Fatalf("missing table: %+v declared=%t err=%v", limits, declared, err)
	}
	limits, declared, err = parseCurrentDungeonCycleDailyLimits("[dungeon cycle]\n45 -1 5097 3 0 45 0 3570 3 0\n[/dungeon cycle]\n")
	if err != nil || !declared || len(limits) != 2 || limits[5097] != 3 || limits[3570] != 3 {
		t.Fatalf("well formed rows: %+v declared=%t err=%v", limits, declared, err)
	}
	for _, malformed := range []string{
		"[dungeon cycle]\n45 -1 5097 3\n[/dungeon cycle]\n",
		"[dungeon cycle]\n45 -1 5097 0 0\n[/dungeon cycle]\n",
		"[dungeon cycle]\n45 -1 0 3 0\n[/dungeon cycle]\n",
		"[dungeon cycle]\n45 -1 5097 3 0 45 0 5097 1 0\n[/dungeon cycle]\n",
	} {
		if _, _, err := parseCurrentDungeonCycleDailyLimits(malformed); err == nil {
			t.Fatalf("malformed cycle table accepted: %q", strings.TrimSpace(malformed))
		}
	}
}
