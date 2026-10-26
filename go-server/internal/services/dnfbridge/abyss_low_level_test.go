package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Real resources verify the selection contract; the death/pickup test below uses
// the existing two-wave runtime fixture, not a claim of full real-map admission.
func TestAbyssLowLevelRealEquipment(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to an explicit PVF")
	}
	t.Setenv(abyssItemLevelFloorEnv, "")
	t.Setenv(abyssItemLevelCeilingEnv, "")
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := loadHellPartyRewardEquipmentPool(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id    int64
		count int
	}{{86, 462}, {87, 395}, {190, 138}, {310, 248}} {
		t.Run(fmt.Sprint(tc.id), func(t *testing.T) {
			d, ok := table.FindDungeon(tc.id)
			if !ok {
				t.Fatal(tc.id)
			}
			for seed := uint32(0); seed < 16; seed++ {
				selected, err := pool.selectEquipment(newCurrentDungeonDropLCG(seed), d.Metadata.MinimumRequiredLevel.Value, d.Metadata.BasisLevel.Value, 4, false)
				if err != nil {
					t.Fatal(err)
				}
				if selected.CandidateCount != tc.count {
					t.Fatalf("dungeon=%d seed=%d candidates=%d want=%d", tc.id, seed, selected.CandidateCount, tc.count)
				}
			}
		})
	}
}

func TestAbyssLowLevelDeathPickup(t *testing.T) {
	t.Setenv(abyssItemLevelFloorEnv, "")
	t.Setenv(abyssItemLevelCeilingEnv, "")
	for _, tc := range []struct {
		name           string
		minimum, basis int
		high           bool
	}{{"sea_train_86", 71, 74, false}, {"sea_train_87", 72, 75, false}, {"high_190", 85, 88, true}, {"high_310", 87, 90, true}} {
		t.Run(tc.name, func(t *testing.T) {
			source := bridgeAbyssEpicFragmentPVF()
			// Fixture definitions use grades observed in the real PVF: 70/76,
			// 75/81 and 90/96. IDs, waves and room topology remain synthetic.
			for i, v := range []struct{ level, grade int }{{70, 76}, {75, 81}, {90, 96}} {
				p := fmt.Sprintf("equipment/piece/%d.equ", 450100+i)
				source[p] = strings.Replace(source[p], "[minimum level]\n85\n", fmt.Sprintf("[minimum level]\n%d\n", v.level), 1)
				source[p] = strings.Replace(source[p], "[grade]\n90\n", fmt.Sprintf("[grade]\n%d\n", v.grade), 1)
			}
			for p, body := range source {
				if strings.HasSuffix(p, ".dgn") {
					body = strings.ReplaceAll(body, fmt.Sprintf("[minimum required level]\n%d\n", hellPartyRewardTestMinimumLevel), fmt.Sprintf("[minimum required level]\n%d\n", tc.minimum))
					body = strings.ReplaceAll(body, fmt.Sprintf("[basis level]\n%d\n", hellPartyRewardTestBasisLevel), fmt.Sprintf("[basis level]\n%d\n", tc.basis))
					source[p] = body
				}
			}
			service, session, conn, runtime, _, repos := prepareLowLevelAbyssRuntime(t, source)
			if runtime.Dungeon.Metadata.MinimumRequiredLevel.Value != int64(tc.minimum) || runtime.Dungeon.Metadata.BasisLevel.Value != int64(tc.basis) {
				t.Fatal("fixture metadata not applied")
			}
			first, last := hellPartyRewardActorKeys(t, runtime)
			abyssEpicFragmentDeath(t, service, session, conn, first[0])
			for _, key := range last[:len(last)-1] {
				abyssEpicFragmentDeath(t, service, session, conn, key)
			}
			rows := abyssEpicFragmentDeath(t, service, session, conn, last[len(last)-1])
			_, fragments := abyssEpicFragmentSplit(t, rows)
			if len(fragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
				t.Fatalf("fragments=%d", len(fragments))
			}
			for _, row := range fragments {
				if (row.ItemID == abyssEpicFragmentTestPieceC) != tc.high {
					t.Fatalf("wrong-level fragment=%d high=%t", row.ItemID, tc.high)
				}
				if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(row.ObjectKey)); err != nil {
					t.Fatal(err)
				}
				if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(row.ObjectKey)); err != nil {
					t.Fatal(err)
				}
			}
			before := abyssEpicFragmentLedgerTotal(t, repos)
			if before != len(fragments) {
				t.Fatalf("pickup/replay balance=%d want=%d", before, len(fragments))
			}
			abyssEpicFragmentDeath(t, service, session, conn, last[len(last)-1])
			if after := abyssEpicFragmentLedgerTotal(t, repos); after != before {
				t.Fatalf("death replay balance=%d want=%d", after, before)
			}
		})
	}
}

// Local fixture uses a legal level-90 character; does not bypass entry level checks.
func prepareLowLevelHellPartyRuntime(
	t *testing.T,
	source bridgePVFSource,
) (*Service, *gameSession, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene) {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	hellPartyTable, err := newPVFDungeonHellPartyCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       90,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:               options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:         table,
		worldMapResolver:      resolver,
		dungeonMonsterTable:   monsters,
		dungeonHellPartyTable: hellPartyTable,
		dungeonChoice:         func(int) (int, error) { return 0, nil },
		dungeonSeed:           func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:    func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "hell-party-wave-test", selectedCharacterID: 99}
	runtime, scene, err := service.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{
		DungeonID:            testHellPartyDungeonID,
		Difficulty:           1,
		HellPartyRequestFlag: 1,
	})
	if err != nil {
		t.Fatalf("prepare hell party dungeon runtime: %v", err)
	}
	// `prepareDungeonRuntime` 只负责造 runtime；把它挂到会话上是上层（commit）那一步。
	// op235 的判据读 `session.dungeon.runtime`，所以这里显式挂上。
	session.dungeon.runtime = runtime
	return service, session, conn, runtime, scene
}

func prepareLowLevelAbyssRuntime(
	t *testing.T,
	source bridgePVFSource,
) (*Service, *gameSession, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene, dnfrepo.Group) {
	t.Helper()
	service, session, conn, runtime, scene := prepareLowLevelHellPartyRuntime(t, source)
	repositories, ok := service.repositoryProvider()
	if !ok {
		t.Fatal("memory repository group is missing")
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{
		AccountID: abyssEpicFragmentTestAccountID,
		Metadata:  map[string]string{"unrelated": "keep"},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if !runtime.HellRoomSet || len(runtime.HellPartyWaves) != 2 {
		t.Fatalf("hell party runtime not frozen: set=%t waves=%+v", runtime.HellRoomSet, runtime.HellPartyWaves)
	}
	if count, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatalf("announce hell party actors: %v count=%d", err, count)
	}
	return service, session, conn, runtime, scene, repositories
}
