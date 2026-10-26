package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"sort"
	"testing"
)

// Real PVF topology and rewards. Preparation binds the entry fixture; combat,
// movement, boss completion, result, statistic, cards and exits use full upper
// dispatch. Client payloads are codec-shaped test requests, not a native capture.
func TestCardTownExitOwnerRealPVF316(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the local runtime Script.pvf")
	}
	hash, err := seekerLukeNextFileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PVF SHA256=%s", hash)
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	progressionTables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	initialExp, err := progressionTables.ThresholdToNext(89)
	if err != nil {
		t.Fatal(err)
	}
	initialSP, err := progressionTables.TotalSkillPoints(90)
	if err != nil {
		t.Fatal(err)
	}
	initialTP, err := progressionTables.TotalTechniquePoints(90)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	aiCharacters, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"mixed42", "mixed132", "reward72"} {
		t.Run(mode, func(t *testing.T) {
			dungeon, found := table.FindDungeon(int64(316))
			if !found {
				t.Fatalf("PVF dungeon %d missing", int64(316))
			}
			request := dungeoncmd.SelectDungeonRequest{DungeonID: uint32(int64(316))}
			if len(dungeon.Metadata.DesignatedDifficulties) > 0 {
				request.Difficulty = byte(dungeon.Metadata.DesignatedDifficulties[0])
			}
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "99", AccountID: "account-1", Level: 90, Job: "0",
				Stats: map[string]int64{"fatigue": 100, "gold": 987654, "exp": int64(initialExp),
					"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
			}); err != nil {
				t.Fatal(err)
			}
			slots := map[string]dnfrepo.ItemStack{}
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{
				TotalSP: initialSP, RemainingSP: initialSP, TotalTP: initialTP, RemainingTP: initialTP, SyncedLevel: 90,
			}}); err != nil {
				t.Fatal(err)
			}
			for i, item := range currentTournamentEntryItems(dungeon) {
				slots[fmt.Sprintf("0:%d", 20+i)] = dnfrepo.ItemStack{ItemID: item.ItemID, Count: 100}
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: slots}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{
				2888: {Status: "completed"},
			}}); err != nil {
				t.Fatal(err)
			}
			service := &Service{
				options:       options{currentClientVersion: "official-24.9", accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain, pvfPath: path},
				worldMapTable: table, worldMapResolver: resolver,
				dungeonMonsterTable: monsters, dungeonAICharacterTable: aiCharacters,
				initialEquipmentArchive: archive,
				dungeonChoice:           func(int) (int, error) { return 0, nil },
				dungeonSeed:             func() (uint32, error) { return 1, nil },
				repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
			}
			conn := &bufferConn{}
			channel := channelcatalog.Channel{ServerID: 1, ID: 19, Type: 1, Name: "ch.19", Port: 10019}
			session := &gameSession{conn: conn, connID: "card-town-owner-real-pvf", channel: channel, residentChannel: channel, selectedCharacterID: 99}
			authenticateRound100RetrySession(service, session)
			service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
			runtime, _, err := service.prepareDungeonRuntime(ctx, session, request)
			if err != nil {
				t.Fatalf("prepare real dungeon %d difficulty %d: %v", int64(316), request.Difficulty, err)
			}
			session.townSceneReadyCharacterID = 99
			session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 99, TownID: 38, AreaID: 1, PositionX: 450, PositionY: 234, MovementCode: 5, PositionValid: true}
			if _, ok := bindCurrentTownSelectorOrigin(session); !ok {
				t.Fatal("town selector origin unavailable")
			}
			if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
				t.Fatal(err)
			}

			if err := service.commitDungeonRuntime(session, runtime); err != nil {
				t.Fatal(err)
			}
			dispatch := func(cmd uint16, body []byte) {
				t.Helper()
				if err := service.handleGameUpper(session, buildCurrentDungeonClientUpperForPhaseTest(t, cmd, body)); err != nil {
					t.Fatalf("op%d: %v", cmd, err)
				}
			}
			visited := map[worldmap.RoomCoordinate]bool{}
			deaths := 0
			for steps := 0; steps < 16; steps++ {
				if steps == 0 {
					if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
						t.Fatal(err)
					}
				}
				scene := runtime.Session.Snapshot().Scene
				visited[scene.Coordinate] = true
				t.Logf("room=%s map=%d boss=%t blockers=%d", scene.Coordinate, scene.Map.Map.ID, scene.Boss, len(scene.BlockingHostiles))
				var keys []uint32
				for key := range scene.RuntimeObjects {
					keys = append(keys, key)
				}
				sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
				for _, key := range keys {
					death := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
					binary.LittleEndian.PutUint32(death, key)
					binary.LittleEndian.PutUint16(death[4:], 99)
					dispatch(uint16(dnfenum.CmdPacketDieMonster), death)
					if _, _, dead := currentDungeonDefeatedActor(runtime, runtime.Session.Snapshot().Scene, key); !dead {
						t.Fatalf("death rejected key=%d", key)
					}
					deaths++
				}
				scene = runtime.Session.Snapshot().Scene
				if scene.Boss {
					for _, key := range keys {
						if !runtime.bossDieCheckAccepted {
							dispatch(uint16(dnfenum.CmdPacketBossDieCheck), bossDieCheckRequestBody(99, uint16(key)))
						}
					}
					break
				}
				var next *worldmap.RoomCoordinate
				for _, room := range runtime.Session.Rooms() {
					if room.Coordinate == scene.Coordinate {
						for _, n := range room.Neighbors {
							if !visited[n.Coordinate] {
								v := n.Coordinate
								next = &v
								break
							}
						}
					}
				}
				if next == nil {
					t.Fatal("no unvisited neighbor on bounded PVF316 path")
				}
				move := make([]byte, dungeoncmd.MoveMapRequestSize)
				move[0], move[1] = byte(next.X), byte(next.Y)
				dispatch(uint16(dnfenum.CmdPacketMoveMap), move)
				if runtime.Session.Snapshot().Scene.Coordinate != *next {
					t.Fatalf("move blocked %s -> %s clear=%t", scene.Coordinate, *next, scene.Cleared)
				}
			}
			if !runtime.settlementEntrySent || !currentDungeonSettlementCompletionReady(runtime, runtime.Session.Snapshot().Run.Status) {
				t.Fatalf("accepted completion missing: status=%s boss=%t persisted=%t entry=%t", runtime.Session.Snapshot().Run.Status, runtime.bossDieCheckAccepted, runtime.tutorialCompletionPersisted, runtime.settlementEntrySent)
			}
			result := make([]byte, currentDungeonPlayResultBaseSize)
			dispatch(uint16(dnfenum.CmdPacketSetPlayResult), result)
			dispatch(uint16(dnfenum.CmdPacketCharacterStatistic), make([]byte, currentDungeonCharacterStatisticBodySize))
			if !completedDungeonSettlementResultReady(runtime, 99) {
				err := service.produceCurrentDungeonSettlementPlanLocked(ctx, session, runtime)
				t.Fatalf("real PVF settlement not ready phase=%s plan=%t error=%v", runtime.settlementPhase, runtime.settlementResultPlan != nil, err)
			}

			before := captureRound100RetryAssets(t, service, 316)
			conn.write.Reset()
			first, second := uint16(42), uint16(132)
			if mode == "mixed132" {
				first, second = second, first
			}
			requests := []uint16{first, second, first, second}
			if mode == "reward72" {
				requests = []uint16{first, first}
			}
			for _, op := range requests {
				dispatch(op, nil)
			}
			if session.dungeon.runtime != runtime || runtime.settlementTownReturnRequestMsgID != first || runtime.settlementCardLayoutSent || runtime.settlementCardRewardCommitted || conn.write.Len() != 0 {
				t.Fatalf("early requests lost first owner or crossed barrier: first=%d stored=%d", first, runtime.settlementTownReturnRequestMsgID)
			}
			dispatch(70, nil)
			if runtime.settlementCardLayoutSent || conn.write.Len() != 0 {
				t.Fatal("op70 without op69 crossed layout barrier")
			}
			unchanged := captureRound100RetryAssets(t, service, 316)
			if !reflect.DeepEqual(before.character.Stats, unchanged.character.Stats) || !reflect.DeepEqual(before.inventory, unchanged.inventory) {
				t.Fatal("early requests changed assets")
			}
			provider := service.repositoryProvider
			if mode == "reward72" {
				failing := repos
				failed := cardTownExitFailedAssets{errors.New("public PVF316 reward failure")}
				failing.CharacterAssets, failing.MailboxAssets = failed, failed
				service.repositoryProvider = func() (dnfrepo.Group, bool) { return failing, true }
			}
			dispatch(69, nil)
			if runtime.settlementCardLayoutSent || runtime.settlementCardRewardCommitted || session.dungeon.runtime != runtime {
				t.Fatal("op69 crossed layout barrier")
			}
			dispatch(70, nil)
			if mode == "reward72" {
				if session.dungeon.runtime != runtime || runtime.townReturnPending || runtime.settlementCardRewardCommitted || runtime.settlementTownReturnRequestMsgID != first || cardTownExitOwnerFrameCount(t, conn.write.Bytes(), 0, 24) != 0 {
					t.Fatal("failed reward lost owner or crossed return barrier")
				}
				failedAssets := captureRound100RetryAssets(t, service, 316)
				if !reflect.DeepEqual(before.character.Stats, failedAssets.character.Stats) || !reflect.DeepEqual(before.inventory, failedAssets.inventory) {
					t.Fatal("failed transaction changed assets")
				}
				service.repositoryProvider = provider
				dispatch(72, []byte{1, 2})
			}
			if session.dungeon.runtime != nil || runtime.townReturnRequestMsgID != first || !runtime.townReturnOp24Sent || !runtime.settlementCardSideRewardCommitted[dungeonCardSideFree] || runtime.settlementCardSideRewardCommitted[dungeonCardSidePaid] {
				t.Fatalf("legal return lost first owner/free reward: first=%d stored=%d", first, runtime.townReturnRequestMsgID)
			}
			transitions, layoutPos, selectPos, townPos, position := 0, -1, -1, -1, 0
			for raw := conn.write.Bytes(); len(raw) > 0; {
				p, rest := splitGameServerUpperPacket(t, raw)
				raw = rest
				position++
				if p.Header.Classification == 1 && p.Header.MsgID == 70 {
					layoutPos = position
				}
				if p.Header.Classification == 1 && p.Header.MsgID == 71 {
					selectPos = position
				}
				if p.Header.Classification == 0 && p.Header.MsgID == 24 {
					transitions++
					townPos = position
					want := []byte{38, 1, 1, 0, 99, 0, 194, 1, 234, 0, 5, 3, 1}
					if !bytes.Equal(p.Body, want) {
						t.Fatalf("typed town body=%x want=%x", p.Body, want)
					}
				}
			}
			if transitions != 1 || layoutPos < 0 || selectPos <= layoutPos || townPos <= selectPos {
				t.Fatalf("layout/free-card/town order: layout=%d selection=%d town=%d transitions=%d", layoutPos, selectPos, townPos, transitions)
			}
			// Pinned !177 lacks the separate completed-op42 ACK sender. Assert
			// the retained request owner above; do not import that runtime here.
			plan := runtime.settlementCardRewardState.plan
			if cardTownExitGold(t, service)-before.character.Stats["gold"] != plan.Sides[dungeonCardSideFree].Gold {
				t.Fatal("free gold differs from one real PVF plan")
			}
			after := captureRound100RetryAssets(t, service, 316)
			itemCounts := func(bag dnfrepo.InventoryRecord) map[int64]int64 {
				counts := map[int64]int64{}
				for _, item := range bag.Slots {
					if item.Count > 0 {
						counts[item.ItemID] += int64(item.Count)
					}
				}
				return counts
			}
			wantItems := itemCounts(before.inventory)
			for _, item := range plan.Sides[dungeonCardSideFree].Items {
				wantItems[item.ItemID] += item.Count
			}
			if !reflect.DeepEqual(wantItems, itemCounts(after.inventory)) {
				t.Fatal("inventory differs from one real free-card bundle")
			}
			conn.write.Reset()
			dispatch(42, nil)
			dispatch(132, nil)
			dispatch(69, nil)
			dispatch(70, nil)
			dispatch(71, []byte{0, 0})
			dispatch(72, []byte{1, 2})
			replay := captureRound100RetryAssets(t, service, 316)
			if conn.write.Len() != 0 || !reflect.DeepEqual(after.character.Stats, replay.character.Stats) || !reflect.DeepEqual(after.inventory, replay.inventory) || after.dailyUsed != replay.dailyUsed {
				t.Fatal("retired owner replay changed assets or scene")
			}
			t.Logf("real316 rooms=%d deaths=%d completion=%s owner=%d freeGold=%d freeItems=%d mode=%s", len(visited), deaths, runtime.clearMapCompletionKey, first, plan.Sides[dungeonCardSideFree].Gold, len(plan.Sides[dungeonCardSideFree].Items), mode)
		})
	}
}
