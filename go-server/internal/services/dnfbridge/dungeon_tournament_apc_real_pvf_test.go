package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Real PVF and the production official-24.9 entry/death/reward handlers.
// Covers the five tournament definitions, APC ownership, out-of-order deaths,
// both reward selections, persisted assets, replay safety and the exit gate.
// No runtime state is forced to champion and no player database is used.
func TestRealPVFTournamentFamilyActorOwnersAndRewards(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the tournament family regression")
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
	items, err := monsters.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	resolveAPC := func(code int64) (byte, bool) {
		definition, found, findErr := aiCharacters.Find(code)
		if findErr != nil || !found {
			return 0, false
		}
		return definition.Level, true
	}

	for _, dungeonID := range []int64{120, 121, 122, 123, 124} {
		t.Run(fmt.Sprintf("dungeon-%d", dungeonID), func(t *testing.T) {
			t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
			dng, ok := table.FindDungeon(dungeonID)
			if !ok {
				t.Fatalf("real PVF dungeon %d missing", dungeonID)
			}
			t.Logf("PVF: id=%d name=%q tournamentFlag=%t noFatigue=%t enterWithoutFatigue=%t requiredItems=%+v",
				dungeonID, dng.Metadata.Name, dng.Metadata.Flags["tournament dungeon"],
				dng.Metadata.NoFatigue, dng.Metadata.Flags["enter without fatigue"], dng.Metadata.RequiredItems)

			topology, err := worldmap.BuildDungeonLayout(resolver, dungeonID, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
				return choice.Candidates[0].ID, nil
			})
			if err != nil {
				t.Fatalf("BuildDungeonLayout: %v", err)
			}
			if topology.Start == nil {
				t.Fatalf("no start room")
			}
			room, ok := topology.Room(*topology.Start)
			if !ok || room.Map == nil {
				t.Fatalf("start room unresolved")
			}
			mapValue := room.Map.Map
			t.Logf("MAP: id=%d malformed=%t candidates=%d startAreas=%d",
				mapValue.ID, mapValue.TournamentDefinitionMalformed,
				len(mapValue.TournamentEnemyCandidates), len(mapValue.TournamentStartAreas))

			definition, reason, ok := resolveTournamentDungeonDefinition(dng, mapValue, resolveAPC)
			if !ok {
				t.Fatalf("definition projection REJECTED: %s", reason)
			}
			runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(uint32(dungeonID)))
			if !ok {
				t.Fatalf("tournament runtime REJECTED: %s", reason)
			}
			t.Logf("DEF: mapID=%d partyLimit=%d candidates=%d pathActors=%d",
				definition.MapID, definition.PartyLimit, len(definition.Candidates),
				len(runtime.snapshot().PathActors))

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
				slots := map[string]dnfrepo.ItemStack{}
				for index, item := range dng.Metadata.RequiredItems {
					slots[fmt.Sprintf("0:%d", 3+index)] = dnfrepo.ItemStack{ItemID: item.ItemID, Count: item.Count + 1}
				}
				if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
					CharacterID: "19",
					Slots:       slots,
				}); err != nil {
					t.Fatal(err)
				}
				return &Service{
					options:                 options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain},
					worldMapTable:           table,
					worldMapResolver:        resolver,
					dungeonMonsterTable:     monsters,
					pvfItemCatalog:          items,
					dungeonAICharacterTable: aiCharacters,
					gameplayTimers:          newFakeCurrentDungeonDeathTimerQueue(),
					dungeonChoice:           func(int) (int, error) { return 0, nil },
					dungeonSeed:             func() (uint32, error) { return 0x5EED1234, nil },
					repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
				}, repositories
			}

			body := make([]byte, 21)
			binary.LittleEndian.PutUint32(body[0:4], uint32(dungeonID))
			binary.LittleEndian.PutUint16(body[9:11], 0xffff)
			request, err := dungeoncmd.DecodeSelectDungeonRequest(body)
			if err != nil {
				t.Fatalf("decode op16 body: %v", err)
			}

			service, repositories := newFixture(t)
			prepareSession := &gameSession{selectedCharacterID: 19}
			runtimeState, scene, err := service.prepareDungeonRuntime(context.Background(), prepareSession, request)

			if err != nil {
				t.Fatal(err)
			}
			if runtimeState.Tournament == nil {
				t.Fatalf("prepareDungeonRuntime attached no tournament runtime")
			}
			packets, err := buildCurrentDungeonEntryPackets(runtimeState, scene)
			if err != nil {
				t.Fatalf("entry packets: %v", err)
			}
			t.Logf("PREPARE: sceneMap=%d definitionMap=%d startMapSuppressed=%t startMapLen=%d dungeonInfoLen=%d",
				scene.Map.Map.ID, runtimeState.Tournament.Definition.MapID,
				packets.StartMapSuppressed, len(packets.StartMap), len(packets.DungeonInfo))

			service, repositories = newFixture(t)
			logger, logPath := testTournamentRewardPacketLogger(t)
			service.packetLog = logger
			defer func() {
				logged := closeTournamentRewardPacketLogger(t, logger, logPath)
				if t.Failed() {
					t.Log(logged)
				}
			}()
			connection := &bufferConn{}
			channel := channelcatalog.Channel{ServerID: 1, ID: 207, Type: 1, Name: "ch.207", Port: 10207}
			session := &gameSession{
				conn:                  connection,
				connID:                fmt.Sprintf("tournament-family-%d", dungeonID),
				channel:               channel,
				residentChannel:       channel,
				selectedCharacterID:   19,
				townActorOwnerChannel: byte(channel.ID),
			}
			bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
			if err := service.handleDungeonSelectUpper(session, body); err != nil {
				t.Fatalf("handleDungeonSelectUpper REJECTED (offline blockage): %v", err)
			}

			if session.dungeon.runtime == nil || session.dungeon.runtime.Dungeon.ID != dungeonID || session.dungeon.runtime.Room == nil || session.dungeon.runtime.Tournament == nil {
				t.Fatal("handler did not commit matching dungeon runtime")
			}
			snapshot := session.dungeon.runtime.Room.Snapshot()

			keys := func() []uint32 {
				snapshot := session.dungeon.runtime.Room.Snapshot()
				result := make([]uint32, 0, 4)
				for _, m := range snapshot.Monsters {
					result = append(result, m.ObjectKey)
				}
				for _, m := range snapshot.ExtendedActors {
					if m.Kind == runtimeDungeonActorAICharacter {
						result = append(result, m.ObjectKey)
					}
				}
				sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
				return result
			}
			if len(keys()) != 4 {
				t.Fatalf("path keys=%v", keys())
			}
			for index, actor := range session.dungeon.runtime.Tournament.snapshot().PathActors {
				key := keys()[index]
				if key != uint32(session.dungeon.runtime.Tournament.boundFirstSequence())+uint32(index) {
					t.Fatal("non-contiguous path sequence")
				}
				if actor.Type == 5 || actor.Type == 8 {
					matched := false
					for _, owner := range snapshot.ExtendedActors {
						if owner.ObjectKey != key {
							continue
						}
						matched = true
						if owner.Packet.Code != uint32(actor.Code) || owner.Packet.Level != actor.Level || owner.Packet.Type != actor.Type || uint32(owner.Packet.ObjectKey) != key || owner.AICharacter == nil || owner.AICharacter.Faction != "[monster]" {
							t.Fatalf("APC owner differs from path: owner=%+v path=%+v", owner, actor)
						}
					}
					if !matched {
						t.Fatal("APC path owner absent")
					}
				} else {
					matched := false
					for _, owner := range snapshot.Monsters {
						if owner.ObjectKey == key {
							matched = true
							if owner.Spawn.MonsterID != actor.Code || owner.Spawn.Level != int64(actor.Level) {
								t.Fatal("monster owner differs from path")
							}
						}
					}
					if !matched {
						t.Fatal("monster path owner absent")
					}
				}
			}

			if dungeonID == 122 {
				if len(snapshot.Monsters) != 0 || len(snapshot.ExtendedActors) != 4 {
					t.Fatalf("APC ownership lost: %+v", snapshot)
				}
				sc, _ := session.dungeon.runtime.Session.Scene()
				if sc.Cleared || len(sc.BlockingHostiles) != 4 {
					t.Fatalf("APC tournament initially cleared: %+v", sc)
				}
				for i, m := range snapshot.ExtendedActors {
					want := byte(5)
					if i == 3 {
						want = 8
					}
					if m.Packet.Type != want || m.State != runtimeDungeonMonsterAnnounced {
						t.Fatalf("APC type/state %+v", m)
					}
				}
			}

			var selectACK, dungeonInfo, tournamentInfo, tournamentMapInfo, ordinaryStartMap bool
			remaining := connection.write.Bytes()
			for len(remaining) > 0 {
				packet, rest := splitGameServerUpperPacket(t, remaining)
				switch packet.Header.MsgID {
				case uint16(dnfenum.CmdPacketSelectDungeon):
					selectACK = packet.Header.Classification == 1 && len(packet.Body) > 0 && packet.Body[0] == 1
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
			t.Logf("HANDLER TAIL: op16ACK=%t op28=%t op386=%t op387=%t op29startMap=%t",
				selectACK, dungeonInfo, tournamentInfo, tournamentMapInfo, ordinaryStartMap)

			inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			if len(dng.Metadata.RequiredItems) == 0 {
				t.Logf("TICKET: DGN declares no [required item]")
			}
			for index, item := range dng.Metadata.RequiredItems {
				key := fmt.Sprintf("0:%d", 3+index)
				if got := inventory.Slots[key].Count; got != item.Count {
					t.Fatalf("ticket %d count=%d want=%d (consume-on-entry)", item.ItemID, got, item.Count)
				}
				t.Logf("TICKET: item=%d consumed %d->%d", item.ItemID, item.Count+1, inventory.Slots[key].Count)
			}
			if ordinaryStartMap {
				t.Fatalf("dungeon %d still sent ordinary START_MAP(op29) on a tournament map", dungeonID)
			}
			if !selectACK || !dungeonInfo || !tournamentInfo || !tournamentMapInfo {
				t.Fatalf("dungeon %d entry tail incomplete", dungeonID)
			}

			for _, future := range []int{1, 3} {
				before := session.dungeon.runtime.Room.Snapshot()
				sceneBefore, _ := session.dungeon.runtime.Session.Scene()
				outputBefore := connection.write.Len()
				body := make([]byte, 74)
				binary.LittleEndian.PutUint32(body, keys()[future])
				binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(session.selectedCharacterID))
				if decoded, err := service.decodeCurrentDungeonMonsterDeath(body); err != nil || decoded.OwnerObjectKey != currentSceneActorObjectKey(session.selectedCharacterID) || len(decoded.OpaqueTail) != 0 {
					t.Fatalf("death fixture invalid: %+v %v", decoded, err)
				}
				if err := service.handleDungeonMonsterDeath(session, body); err != nil {
					t.Fatal(err)
				}
				sceneAfter, _ := session.dungeon.runtime.Session.Scene()
				if !reflect.DeepEqual(before, session.dungeon.runtime.Room.Snapshot()) || !reflect.DeepEqual(sceneBefore, sceneAfter) || connection.write.Len() != outputBefore || session.dungeon.runtime.Tournament.currentRoundNumber() != 1 {
					t.Fatalf("future %d mutated dungeon %d", future, dungeonID)
				}
			}
			first := keys()[0]
			body = make([]byte, 74)
			binary.LittleEndian.PutUint32(body, first)
			binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(session.selectedCharacterID))
			if decoded, err := service.decodeCurrentDungeonMonsterDeath(body); err != nil || decoded.OwnerObjectKey != currentSceneActorObjectKey(session.selectedCharacterID) || len(decoded.OpaqueTail) != 0 {
				t.Fatalf("death fixture invalid: %+v %v", decoded, err)
			}
			if err := service.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if len(tournamentDefeatedObjectKeys(session.dungeon.runtime.Room.Snapshot())) != 1 || session.dungeon.runtime.Tournament.currentRoundNumber() != 2 {
				t.Fatal("valid first death did not advance")
			}

			afterFirst := session.dungeon.runtime.Room.Snapshot()
			afterFirstScene, _ := session.dungeon.runtime.Session.Scene()
			outputAfterFirst := connection.write.Len()
			if err := service.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			replayFirstScene, _ := session.dungeon.runtime.Session.Scene()
			if !reflect.DeepEqual(afterFirst, session.dungeon.runtime.Room.Snapshot()) || !reflect.DeepEqual(afterFirstScene, replayFirstScene) || outputAfterFirst != connection.write.Len() || session.dungeon.runtime.Tournament.currentRoundNumber() != 2 {
				t.Fatal("regular-owner duplicate death mutated or advanced")
			}

			for round := 2; round <= 4; round++ {
				binary.LittleEndian.PutUint32(body, keys()[round-1])
				if err := service.handleDungeonMonsterDeath(session, body); err != nil {
					t.Fatal(err)
				}
				if n := len(tournamentDefeatedObjectKeys(session.dungeon.runtime.Room.Snapshot())); n != round {
					t.Fatalf("round %d defeated=%d", round, n)
				}
				if round < 4 && session.dungeon.runtime.Tournament.currentRoundNumber() != byte(round+1) {
					t.Fatalf("round %d did not advance", round)
				}
			}
			if !session.dungeon.runtime.Tournament.isChampion() {
				t.Fatal("not champion")
			}
			if session.dungeon.runtime.tournamentReward == nil {
				t.Fatal("champion reward state was not created")
			}
			if session.dungeon.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
				t.Fatalf("champion run not completed: %+v", session.dungeon.runtime.Session.Snapshot().Run)
			}
			rewardPackets := 0
			remaining = connection.write.Bytes()
			for len(remaining) > 0 {
				packet, rest := splitGameServerUpperPacket(t, remaining)
				remaining = rest
				if packet.Header.MsgID == a21NotiTournamentClearReward {
					rewardPackets++
				}
			}
			if rewardPackets != 1 {
				t.Fatalf("reward packets=%d", rewardPackets)
			}
			connection.write.Reset()
			if handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelectState, nil); err != nil || !handled {
				t.Fatalf("457 handled=%t err=%v", handled, err)
			}
			statePacket, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
			if statePacket.Header.MsgID != a21CmdTournamentRewardSelectState || len(statePacket.Body) != 9 || statePacket.Body[0] != 1 || len(trailing) != 0 {
				t.Fatal("invalid reward state response")
			}
			beforeRewardInventory, _, err := repositories.Inventory.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			beforeRewardCharacter, _, err := repositories.Character.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			countItem := func(inventory dnfrepo.InventoryRecord, item int64) int64 {
				var n int64
				for _, stack := range inventory.Slots {
					if stack.ItemID == item {
						n += stack.Count
					}
				}
				return n
			}
			wantGold := int64(0)
			wantItems := map[int64]int64{}
			for _, cards := range session.dungeon.runtime.tournamentReward.Cards {
				reward := cards[0]
				if reward.IsGold {
					wantGold += reward.GoldAmount
				} else if reward.ItemID > 0 {
					wantItems[reward.ItemID] += reward.StackCount
				}
			}
			for _, cardType := range []byte{0, 1} {
				if handled, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{cardType, 0}); err != nil || !handled {
					t.Fatalf("458 type %d handled=%t err=%v", cardType, handled, err)
				}
			}
			if !session.dungeon.runtime.tournamentReward.isSelectionComplete() {
				t.Fatal("reward selection incomplete")
			}
			afterRewardInventory, _, err := repositories.Inventory.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			afterRewardCharacter, _, err := repositories.Character.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			if afterRewardCharacter.Stats["gold"]-beforeRewardCharacter.Stats["gold"] != wantGold {
				t.Fatalf("gold delta=%d want=%d", afterRewardCharacter.Stats["gold"]-beforeRewardCharacter.Stats["gold"], wantGold)
			}
			for item, n := range wantItems {
				if delta := countItem(afterRewardInventory, item) - countItem(beforeRewardInventory, item); delta != n {
					t.Fatalf("item%d delta=%d want=%d", item, delta, n)
				}
			}
			for _, cardType := range []byte{0, 1} {
				if _, err := service.handleCurrentTournamentRewardCommand(session, a21CmdTournamentRewardSelect, []byte{cardType, 0}); err != nil {
					t.Fatal(err)
				}
			}
			replayInventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
			replayCharacter, _, _ := repositories.Character.Load(context.Background(), "19")
			if !reflect.DeepEqual(afterRewardInventory, replayInventory) || !reflect.DeepEqual(afterRewardCharacter, replayCharacter) {
				t.Fatal("duplicate selection changed assets")
			}
			if !currentDungeonTournamentResultExitReady(session.dungeon.runtime, session.selectedCharacterID) || !completedDungeonSettlementExitReady(session.dungeon.runtime, session.selectedCharacterID) {
				t.Fatal("exit gate not ready")
			}
			// Continue the actual PVF entry/four-round/selection chain through the
			// user's result-page town action, rather than only checking readiness.
			completedRuntime := session.dungeon.runtime
			connection.write.Reset()
			if handled, err := currentGameplayModules.DispatchLegacy(service, session, 72, []byte{1, 2}); err != nil || !handled {
				t.Fatalf("result town handled=%t err=%v", handled, err)
			}
			if session.dungeon.runtime != nil || !completedRuntime.townReturnOp24Sent {
				t.Fatal("real completed tournament retained runtime after result town")
			}
			op24, op72 := 0, 0
			for remaining := connection.write.Bytes(); len(remaining) > 0; {
				packet, rest := splitGameServerUpperPacket(t, remaining)
				remaining = rest
				switch packet.Header.MsgID {
				case 24:
					op24++
				case 72:
					op72++
					if len(packet.Body) != 3 || packet.Body[0] != 1 || packet.Body[1] != 1 || packet.Body[2] != 2 {
						t.Fatalf("town action ACK %x", packet.Body)
					}
				}
			}
			if op24 != 1 || op72 != 1 {
				t.Fatalf("result town op24=%d op72=%d", op24, op72)
			}
			afterTownInventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
			afterTownCharacter, _, _ := repositories.Character.Load(context.Background(), "19")
			if !reflect.DeepEqual(replayInventory, afterTownInventory) || !reflect.DeepEqual(replayCharacter, afterTownCharacter) {
				t.Fatal("result town changed committed real-PVF rewards")
			}
			t.Logf("RESULT: dungeon %d passed offline four-round deaths, champion reward, both selections and op72 town", dungeonID)
		})
	}
}
