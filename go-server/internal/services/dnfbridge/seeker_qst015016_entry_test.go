package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFTournamentEntryMatrixCurrentPVF exercises the production
// op16 entry handler for every tournament dungeon that actually exists in the
// fixed PVF. Names come from the archive's DGN metadata, not from file names.
//
// The positive entry and each applicable negative precondition use fresh
// in-memory repositories so an entry-item deduction from one check cannot hide
// or create a failure in the next check. This is an automatic protocol/handler
// check, not native client acceptance.
func TestRealScriptPVFTournamentEntryMatrixCurrentPVF(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real tournament entry matrix")
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

	tests := []struct {
		dungeonID int64
		name      string
	}{
		{dungeonID: 120, name: "黄龙大会"},
		{dungeonID: 121, name: "青龙大会"},
		{dungeonID: 122, name: "黑龙大会"},
		{dungeonID: 123, name: "魂·青龙大会"},
		{dungeonID: 124, name: "魂·黄龙大会"},
	}
	for _, test := range tests {
		test := test
		t.Run(fmt.Sprintf("%03d_%s", test.dungeonID, test.name), func(t *testing.T) {
			t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
			dungeon, ok := table.FindDungeon(test.dungeonID)
			if !ok {
				t.Fatalf("real PVF tournament dungeon %d is missing", test.dungeonID)
			}
			if dungeon.Metadata.Name != test.name {
				t.Fatalf("real PVF dungeon %d name=%q want=%q path=%s",
					test.dungeonID, dungeon.Metadata.Name, test.name, dungeon.Path)
			}
			if !dungeon.Metadata.Flags["tournament dungeon"] {
				t.Fatalf("real PVF dungeon %d does not carry [tournament dungeon]", test.dungeonID)
			}
			required := currentTournamentEntryItems(dungeon)
			minimumLevel := "unset"
			if dungeon.Metadata.MinimumRequiredLevel.Set {
				minimumLevel = fmt.Sprint(dungeon.Metadata.MinimumRequiredLevel.Value)
			}
			basisLevel := "unset"
			if dungeon.Metadata.BasisLevel.Set {
				basisLevel = fmt.Sprint(dungeon.Metadata.BasisLevel.Value)
			}
			t.Logf("PVF id=%d name=%q path=%q minimum_level=%s basis_level=%s required=%+v",
				test.dungeonID, dungeon.Metadata.Name, dungeon.Path,
				minimumLevel, basisLevel, required)

			newFixture := func(t *testing.T, level int, withEntryItems bool) (*Service, dnfrepo.Group) {
				t.Helper()
				repositories := dnfrepomemory.NewMemoryGroup()
				if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
					CharacterID: "19",
					AccountID:   "account-1",
					Level:       level,
					Stats: map[string]int64{
						"fatigue": 156, "town_id": 38, "area_id": 1,
						"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
					},
				}); err != nil {
					t.Fatal(err)
				}
				slots := map[string]dnfrepo.ItemStack{
					"0:20": {ItemID: 3329, Count: 1},
				}
				if withEntryItems {
					for index, requirement := range required {
						slots[fmt.Sprintf("0:%d", 3+index)] = dnfrepo.ItemStack{
							ItemID: requirement.ItemID,
							Count:  requirement.Count + 1,
						}
					}
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
					dungeonAICharacterTable: aiCharacters,
					dungeonChoice:           func(int) (int, error) { return 0, nil },
					dungeonSeed:             func() (uint32, error) { return 0x5EED1234, nil },
					repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
				}, repositories
			}
			newSession := func(service *Service, suffix string) (*gameSession, *bufferConn) {
				t.Helper()
				connection := &bufferConn{}
				channel := channelcatalog.Channel{ServerID: 1, ID: 207, Type: 1, Name: "ch.207", Port: 10207}
				session := &gameSession{
					conn:                  connection,
					connID:                "real-tournament-entry-" + suffix,
					channel:               channel,
					residentChannel:       channel,
					selectedCharacterID:   19,
					townActorOwnerChannel: byte(channel.ID),
				}
				bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
				return session, connection
			}
			selectBody := make([]byte, 21)
			binary.LittleEndian.PutUint32(selectBody[0:4], uint32(test.dungeonID))
			binary.LittleEndian.PutUint16(selectBody[9:11], 0xffff)
			request, err := dungeoncmd.DecodeSelectDungeonRequest(selectBody)
			if err != nil {
				t.Fatalf("decode op16 body: %v", err)
			}

			// Positive path: prepare the actual DGN/MAP and run the production
			// op16 handler. A handler failure for a valid character is a red case.
			service, repositories := newFixture(t, 90, true)
			prepareSession := &gameSession{selectedCharacterID: 19}
			runtimeState, scene, prepareErr := service.prepareDungeonRuntime(
				context.Background(), prepareSession, request,
			)
			if prepareErr != nil {
				t.Errorf("valid prepareDungeonRuntime dungeon=%d: %v", test.dungeonID, prepareErr)
			} else {
				if runtimeState.Tournament == nil {
					t.Errorf("dungeon %d prepare attached no tournament runtime", test.dungeonID)
				}
				if scene.Map.Map.ID == 0 || runtimeState.Tournament == nil ||
					scene.Map.Map.ID != runtimeState.Tournament.Definition.MapID {
					t.Errorf("dungeon %d scene map=%d tournament map missing/mismatched",
						test.dungeonID, scene.Map.Map.ID)
				}
				roomSnapshot := runtimeState.Room.Snapshot()
				wantActors := tournamentRoundCount
				if runtimeState.Tournament != nil {
					wantActors *= runtimeState.Tournament.Definition.PartyLimit
				}
				if runtimeState.Tournament.Definition.tournamentActorsUseApc() {
					if len(roomSnapshot.Monsters) != 0 || len(roomSnapshot.ExtendedActors) != wantActors {
						t.Errorf("dungeon %d APC projection monsters=%d extended=%d want=(0,%d)",
							test.dungeonID, len(roomSnapshot.Monsters),
							len(roomSnapshot.ExtendedActors), wantActors)
					}
					for index, actor := range roomSnapshot.ExtendedActors {
						if actor.Kind != runtimeDungeonActorAICharacter || actor.Packet.Blocking == 0 {
							t.Errorf("dungeon %d APC actor %d kind=%s blocking=%d want=(ai_character,1)",
								test.dungeonID, index, actor.Kind, actor.Packet.Blocking)
						}
					}
				} else if len(roomSnapshot.Monsters) != wantActors {
					t.Errorf("dungeon %d projected monsters=%d want=%d",
						test.dungeonID, len(roomSnapshot.Monsters), wantActors)
				}
				packets, packetErr := buildCurrentDungeonEntryPackets(runtimeState, scene)
				if packetErr != nil {
					t.Errorf("dungeon %d build entry packets: %v", test.dungeonID, packetErr)
				} else {
					if !packets.StartMapSuppressed || len(packets.StartMap) != 0 {
						t.Errorf("dungeon %d start-map suppressed=%t len=%d want=(true,0)",
							test.dungeonID, packets.StartMapSuppressed, len(packets.StartMap))
					}
					if len(packets.DungeonInfo) != 36 {
						t.Errorf("dungeon %d op28 body len=%d want=36",
							test.dungeonID, len(packets.DungeonInfo))
					}
				}
			}

			service, repositories = newFixture(t, 90, true)
			session, connection := newSession(service, "positive")
			handlerErr := service.handleDungeonSelectUpper(session, selectBody)
			summary := summarizeSeekerTournamentEntryPackets(t, connection)
			t.Logf("positive handler dungeon=%d err=%v packets=%+v", test.dungeonID, handlerErr, summary)
			if handlerErr != nil {
				t.Errorf("valid op16 handler dungeon=%d: %v", test.dungeonID, handlerErr)
			}
			if !summary.selectACK || !summary.dungeonInfo || !summary.tournamentInfo || !summary.tournamentMapInfo {
				t.Errorf("dungeon %d response sequence op16=%t op28=%t op386=%t op387=%t",
					test.dungeonID, summary.selectACK, summary.dungeonInfo,
					summary.tournamentInfo, summary.tournamentMapInfo)
			}
			if summary.ordinaryStartMap {
				t.Errorf("dungeon %d sent ordinary START_MAP(op29)", test.dungeonID)
			}
			if len(summary.rejectReasons) != 0 {
				t.Errorf("valid dungeon %d received reject reasons=%v", test.dungeonID, summary.rejectReasons)
			}

			inventory, found, inventoryErr := repositories.Inventory.Load(context.Background(), "19")
			if inventoryErr != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, inventoryErr)
			}
			for index, requirement := range required {
				slot := fmt.Sprintf("0:%d", 3+index)
				got := inventory.Slots[slot].Count
				want := int64(1)
				if requirement.ConsumeOnEntry {
					if got != want {
						t.Errorf("dungeon %d entry item %d count=%d want=1",
							test.dungeonID, requirement.ItemID, got)
					}
				} else if got != requirement.Count+1 {
					t.Errorf("dungeon %d non-consuming requirement %d count=%d want=%d",
						test.dungeonID, requirement.ItemID, got, requirement.Count+1)
				}
			}
			if kept := inventory.Slots["0:20"]; kept.ItemID != 3329 || kept.Count != 1 {
				t.Errorf("dungeon %d clobbered unrelated stack: %+v", test.dungeonID, kept)
			}

			// Negative 1: level below the PVF's minimum must be rejected before
			// tournament projection. Every current tournament has a set minimum;
			// the assertion is data-driven so a future PVF change is visible.
			if dungeon.Metadata.MinimumRequiredLevel.Set && dungeon.Metadata.MinimumRequiredLevel.Value > 1 {
				service, _ = newFixture(t, 1, true)
				_, _, err := service.prepareDungeonRuntime(context.Background(), &gameSession{selectedCharacterID: 19}, request)
				if !errors.Is(err, dnfdungeon.ErrEntryLevelTooLow) {
					t.Errorf("dungeon %d level=1 prepare err=%v want ErrEntryLevelTooLow", test.dungeonID, err)
				} else {
					t.Logf("level negative dungeon=%d minimum=%d prepare_err=%v",
						test.dungeonID, dungeon.Metadata.MinimumRequiredLevel.Value, err)
				}

				service, _ = newFixture(t, 1, true)
				session, connection = newSession(service, "low-level")
				handlerErr := service.handleDungeonSelectUpper(session, selectBody)
				summary := summarizeSeekerTournamentEntryPackets(t, connection)
				if summary.selectACK || summary.dungeonInfo || summary.tournamentInfo ||
					summary.tournamentMapInfo || summary.ordinaryStartMap {
					t.Errorf("dungeon %d low level was not rejected before entry packets: %+v",
						test.dungeonID, summary)
				}
				if len(summary.rejectReasons) != 1 ||
					summary.rejectReasons[0] != currentSelectDungeonEntryLevelTooLowReason {
					t.Errorf("dungeon %d low level err=%v reject reasons=%v want=[%d]",
						test.dungeonID, handlerErr, summary.rejectReasons,
						currentSelectDungeonEntryLevelTooLowReason)
				} else {
					t.Logf("level negative dungeon=%d op16 reject=%d",
						test.dungeonID, summary.rejectReasons[0])
				}
			}

			// Negative 2: a consuming PVF entry item that is absent must be a
			// rejection, not an entry followed by a silent zero deduction.
			if len(required) != 0 {
				t.Setenv(dungeonEntryRequiredItemReasonEnv, "")
				service, _ = newFixture(t, 90, false)
				session, connection = newSession(service, "missing-entry-item")
				handlerErr := service.handleDungeonSelectUpper(session, selectBody)
				summary := summarizeSeekerTournamentEntryPackets(t, connection)
				t.Logf("missing-entry-item dungeon=%d err=%v packets=%+v",
					test.dungeonID, handlerErr, summary)
				if summary.selectACK || summary.dungeonInfo || summary.tournamentInfo ||
					summary.tournamentMapInfo || summary.ordinaryStartMap {
					t.Errorf("dungeon %d missing entry item was not rejected before entry packets: %+v",
						test.dungeonID, summary)
				}
				if len(summary.rejectReasons) != 1 ||
					summary.rejectReasons[0] != currentSelectDungeonMissingRequiredItemReason {
					t.Errorf("dungeon %d missing entry item reject reasons=%v want=[%d]",
						test.dungeonID, summary.rejectReasons, currentSelectDungeonMissingRequiredItemReason)
				} else {
					t.Logf("missing-entry-item dungeon=%d op16 reject=%d",
						test.dungeonID, summary.rejectReasons[0])
				}
			}
		})
	}
}

type seekerTournamentPacketSummary struct {
	selectACK         bool
	dungeonInfo       bool
	tournamentInfo    bool
	tournamentMapInfo bool
	ordinaryStartMap  bool
	rejectReasons     []byte
}

func summarizeSeekerTournamentEntryPackets(
	t *testing.T,
	connection *bufferConn,
) seekerTournamentPacketSummary {
	t.Helper()
	var summary seekerTournamentPacketSummary
	remaining := connection.write.Bytes()
	for len(remaining) > 0 {
		packet, rest := splitGameServerUpperPacket(t, remaining)
		switch packet.Header.MsgID {
		case uint16(dnfenum.CmdPacketSelectDungeon):
			if len(packet.Body) > 0 && packet.Body[0] == 1 {
				summary.selectACK = true
			} else if len(packet.Body) >= 2 && packet.Body[0] == 0 {
				summary.rejectReasons = append(summary.rejectReasons, packet.Body[1])
			}
		case currentDungeonInfoNotification:
			summary.dungeonInfo = len(packet.Body) == 36
		case a21NotiTournamentInfo:
			summary.tournamentInfo = len(packet.Body) == 260
		case a21NotiTournamentMapInfo:
			summary.tournamentMapInfo = len(packet.Body) == 15
		case currentDungeonStartNotification:
			summary.ordinaryStartMap = true
		}
		remaining = rest
	}
	return summary
}
