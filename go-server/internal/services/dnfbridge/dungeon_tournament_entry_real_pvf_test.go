package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档回归（P8）：`Script.pvf` 的 120 黄龙 / 121 青龙必须能**从头走到进图**。
//
// 复现线上那次失败所需的全部条件都在这里：真档 DGN/MAP、真档 `[required item]`
// 证物，以及 op16 选图请求体（difficulty=0，与操作者 2026-09-27 18:10 那次实测同形）。
//
// 运行方式（必须显式给路径，否则 skip）：
//
//	$env:DNFBRIDGE_REAL_PVF_SMOKE='H:\...\Script.pvf'
//	go test -count=1 -run RealScriptPVFTournamentEntry -v ./internal/services/dnfbridge
func TestRealScriptPVFTournamentEntryBuildsWithoutOrdinaryStartMap(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real tournament entry smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
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

	for _, test := range []struct {
		dungeonID int64
		entryItem int64
		name      string
	}{
		{dungeonID: 120, entryItem: 4726, name: "黄龙大会"},
		{dungeonID: 121, entryItem: 4727, name: "青龙大会"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
			// `prepareDungeonRuntime` 里就有"入场即消耗"这一步，所以 ①② 与 ③ 各用一份
			// 干净的夹具：否则证物会被扣两次，计数断言看不出是哪一次扣的。
			newFixture := func(t *testing.T) (*Service, dnfrepo.Group) {
				t.Helper()
				repositories := dnfrepomemory.NewMemoryGroup()
				if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
					CharacterID: "19",
					AccountID:   "account-1",
					Level:       90,
					Stats: map[string]int64{
						"fatigue": 156, "town_id": 38, "area_id": 1,
						"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
					},
				}); err != nil {
					t.Fatal(err)
				}
				// 证物放在主背包（`[required item]` 的持仓与扣减口径都只数 `Slots`）。
				if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
					CharacterID: "19",
					Slots: map[string]dnfrepo.ItemStack{
						"0:3": {ItemID: test.entryItem, Count: 2},
						"0:4": {ItemID: 3329, Count: 1},
					},
				}); err != nil {
					t.Fatal(err)
				}
				return &Service{
					options:                 options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
					worldMapTable:           table,
					worldMapResolver:        resolver,
					dungeonMonsterTable:     monsters,
					dungeonAICharacterTable: aiCharacters,
					dungeonChoice:           func(int) (int, error) { return 0, nil },
					dungeonSeed:             func() (uint32, error) { return 0x5EED1234, nil },
					repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
				}, repositories
			}
			assertEntryItemConsumedOnce := func(t *testing.T, repositories dnfrepo.Group, stage string) {
				t.Helper()
				inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
				if err != nil || !found {
					t.Fatalf("%s: load inventory found=%t err=%v", stage, found, err)
				}
				if got := inventory.Slots["0:3"].Count; got != 1 {
					t.Fatalf("%s: entry item %d count=%d want 1 (入场即消耗，不返还)",
						stage, test.entryItem, got)
				}
				if got := inventory.Slots["0:4"].Count; got != 1 {
					t.Fatalf("%s: unrelated stack changed: count=%d want 1", stage, got)
				}
			}
			// op16 body：dungeon / difficulty 0 / entry_option 0 / selection_mode 0 /
			// runtime_state 0 / token 0xFFFF / 其余 0（21 字节，当前 EXE 写入器形状）。
			body := make([]byte, 21)
			binary.LittleEndian.PutUint32(body[0:4], uint32(test.dungeonID))
			binary.LittleEndian.PutUint16(body[9:11], 0xffff)
			request, err := dungeoncmd.DecodeSelectDungeonRequest(body)
			if err != nil {
				t.Fatalf("decode op16 body: %v", err)
			}

			// ① `prepareDungeonRuntime` 必须过 —— 线上就断在它内部的
			//    `plan current start-map body: … monster rank is unsupported: rank=""` 前置校验上。
			service, repositories := newFixture(t)
			prepareSession := &gameSession{selectedCharacterID: 19}
			runtimeState, scene, err := service.prepareDungeonRuntime(context.Background(), prepareSession, request)
			if err != nil {
				t.Fatalf("real dungeon %d prepareDungeonRuntime: %v", test.dungeonID, err)
			}
			if runtimeState.Tournament == nil {
				t.Fatalf("real dungeon %d did not attach a tournament runtime", test.dungeonID)
			}
			if scene.Map.Map.ID != runtimeState.Tournament.Definition.MapID {
				t.Fatalf("real dungeon %d scene map=%d definition map=%d",
					test.dungeonID, scene.Map.Map.ID, runtimeState.Tournament.Definition.MapID)
			}
			roomSnapshot := runtimeState.Room.Snapshot()
			if len(roomSnapshot.Monsters) != tournamentRoundCount {
				t.Fatalf("real dungeon %d projected monsters=%d want=%d",
					test.dungeonID, len(roomSnapshot.Monsters), tournamentRoundCount)
			}
			// 票根：前 3 轮的对手来自 `[tournament enemies]`，**没有 rank**；
			// 第 4 轮被升格成 `[boss]`（厂商 `TournamentDungeonRuntime.cs:505-506`）。
			if roomSnapshot.Monsters[0].Spawn.Rank != "" {
				t.Fatalf("real dungeon %d first opponent rank=%q want empty",
					test.dungeonID, roomSnapshot.Monsters[0].Spawn.Rank)
			}
			if roomSnapshot.Monsters[tournamentRoundCount-1].Spawn.Rank != "[boss]" {
				t.Fatalf("real dungeon %d final opponent rank=%q want [boss]",
					test.dungeonID, roomSnapshot.Monsters[tournamentRoundCount-1].Spawn.Rank)
			}

			// ② 入场包：op28 有体、op29 **不生成**（厂商 `DungeonMapHandler.cs:778-779`）。
			packets, err := buildCurrentDungeonEntryPackets(runtimeState, scene)
			if err != nil {
				t.Fatalf("real dungeon %d entry packets: %v", test.dungeonID, err)
			}
			if !packets.StartMapSuppressed || len(packets.StartMap) != 0 {
				t.Fatalf("real dungeon %d start-map suppressed=%t len=%d want (true,0)",
					test.dungeonID, packets.StartMapSuppressed, len(packets.StartMap))
			}
			if len(packets.DungeonInfo) != 36 {
				t.Fatalf("real dungeon %d op28 body len=%d want 36", test.dungeonID, len(packets.DungeonInfo))
			}

			assertEntryItemConsumedOnce(t, repositories, "prepareDungeonRuntime")

			// ③ 走完整 handler：发出去的流里必须**没有 op29**，而有 386/387。
			//    用**另一份**干净夹具（上面那次 prepare 已经扣掉一个证物了）。
			service, repositories = newFixture(t)
			connection := &bufferConn{}
			channel := channelcatalog.Channel{ServerID: 1, ID: 207, Type: 1, Name: "ch.207", Port: 10207}
			session := &gameSession{
				conn:                  connection,
				connID:                "real-tournament-entry",
				channel:               channel,
				residentChannel:       channel,
				selectedCharacterID:   19,
				townActorOwnerChannel: byte(channel.ID),
			}
			bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
			if err := service.handleDungeonSelectUpper(session, body); err != nil {
				t.Fatalf("real dungeon %d op16 entry: %v", test.dungeonID, err)
			}

			var (
				selectACK         bool
				dungeonInfo       bool
				tournamentInfo    bool
				tournamentMapInfo bool
				ordinaryStartMap  bool
			)
			remaining := connection.write.Bytes()
			for len(remaining) > 0 {
				packet, rest := splitGameServerUpperPacket(t, remaining)
				switch packet.Header.MsgID {
				case uint16(dnfenum.CmdPacketSelectDungeon):
					selectACK = true
				case currentDungeonInfoNotification:
					dungeonInfo = len(packet.Body) == 36
				case a21NotiTournamentInfo:
					tournamentInfo = len(packet.Body) == 260
				case a21NotiTournamentMapInfo:
					tournamentMapInfo = len(packet.Body) == 15
				case currentDungeonStartNotification:
					ordinaryStartMap = true
				}
				remaining = rest
			}
			if !selectACK || !dungeonInfo || !tournamentInfo || !tournamentMapInfo {
				t.Fatalf("real dungeon %d response sequence: op16=%t op28=%t op386=%t op387=%t",
					test.dungeonID, selectACK, dungeonInfo, tournamentInfo, tournamentMapInfo)
			}
			if ordinaryStartMap {
				t.Fatalf("real dungeon %d still sent the ordinary START_MAP(op29) on a tournament map",
					test.dungeonID)
			}

			// ④ 证物被扣（行为与上一批一致，本批未改）：4726/4727 由 2 → 1，无关道具原样。
			assertEntryItemConsumedOnce(t, repositories, "handleDungeonSelectUpper")
		})
	}
}
