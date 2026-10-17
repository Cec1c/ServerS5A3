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
//	7100  旧六房间裂缝；用户已确认 DOC-083 实际目标为下述新版分支地图
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

func TestRealPVFDimensionDungeonSharedDailyCounter(t *testing.T) {
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

	// 艾肯额度仍取自 etc/dungeoncyclesystem.etc；其入场/通关语义由主线
	// cycle_entry_regression_test.go 的独立入场次数实现覆盖。
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

	t.Logf("real PVF: dimension pool 5/day shared by 7100+7165 and rejected on 7171")
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
