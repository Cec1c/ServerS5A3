package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// R134 P126/P135/P140: real dungeon/map/admission metadata and full request
// dispatch for retry and return. Completion/reward barriers are seeded by the
// existing settlement fixture; this is not a combat, reward-grant, or native UI
// test. Ordinary dungeon 3 is a control, not the unidentified P126 player map.
func TestR134RetryRefusalReturnRealPVF(t *testing.T) {
	path := os.Getenv("R134_RETRY_REAL_PVF")
	if path == "" {
		t.Skip("set R134_RETRY_REAL_PVF to the local runtime Script.pvf")
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
	for _, gate := range []struct {
		name      string
		dungeonID int64
		reason    byte
	}{
		{"ordinary_no_fatigue", 3, 0x16},
		{"rift_daily_limit", 316, currentSelectDungeonMemberEntryLimitReason},
	} {
		for _, exit := range []struct {
			name string
			cmd  uint16
			body []byte
		}{
			{"op72_town", uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 2}},
			{"op42_completed_exit", uint16(dnfenum.CmdPacketGiveupGame), nil},
			{"op132_back_to_village", uint16(dnfenum.CmdPacketBack2Village), nil},
		} {
			t.Run(gate.name+"/"+exit.name, func(t *testing.T) {
				dungeon, found := table.FindDungeon(gate.dungeonID)
				if !found {
					t.Fatalf("PVF dungeon %d missing", gate.dungeonID)
				}
				request := dungeoncmd.SelectDungeonRequest{DungeonID: uint32(gate.dungeonID)}
				if len(dungeon.Metadata.DesignatedDifficulties) > 0 {
					request.Difficulty = byte(dungeon.Metadata.DesignatedDifficulties[0])
				}
				repos := dnfrepomemory.NewMemoryGroup()
				if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
					CharacterID: "99", AccountID: "account-1", Level: 90,
					Stats: map[string]int64{"fatigue": 100, "gold": 987654,
						"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
				}); err != nil {
					t.Fatal(err)
				}
				slots := map[string]dnfrepo.ItemStack{}
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
				session := &gameSession{conn: conn, connID: "r134-retry-real-pvf", channel: channel, residentChannel: channel, selectedCharacterID: 99}
				authenticateRound100RetrySession(service, session)
				runtime, _, err := service.prepareDungeonRuntime(ctx, session, request)
				if err != nil {
					t.Fatalf("prepare real dungeon %d difficulty %d: %v", gate.dungeonID, request.Difficulty, err)
				}
				session.dungeon.runtime = runtime
				session.townSceneReadyCharacterID = 99
				session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 99, TownID: 38, AreaID: 1, PositionX: 450, PositionY: 234, MovementCode: 5, PositionValid: true}
				if _, ok := bindCurrentTownSelectorOrigin(session); !ok {
					t.Fatal("town selector origin unavailable")
				}
				if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
					t.Fatal(err)
				}
				completePartyRetryFixture(t, service, session)
				// The old helper seeds legacy aggregate flags, but real PVF card
				// rewards have per-side state. Finish the legitimate free-card
				// handler before taking an asset baseline, rather than suppressing
				// the reward which op72 is allowed to auto-flip.
				cardPacket, err := dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketSelectCard), []byte{0, 0}, 0, dnfproto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.handleGameUpper(session, cardPacket); err != nil {
					t.Fatal(err)
				}
				if !runtime.settlementCardSideSelectionSent[dungeonCardSideFree] || !runtime.settlementCardRewardCommitted {
					t.Fatal("real free-card reward must be committed before retry baseline")
				}
				character, found, err := repos.Character.Load(ctx, "99")
				if err != nil || !found {
					t.Fatalf("character found=%t err=%v", found, err)
				}
				day := premium.ServiceDayID(service.gameplayNow())
				if gate.dungeonID == 316 {
					character.Stats["dungeon_daily_entry_316_day"] = day
					character.Stats["dungeon_daily_entry_316_used"] = currentRiftDailyLimit
				} else {
					character.Stats["fatigue"] = 0
					character.Stats["fatigue_limit"] = 156
					character.Stats["fatigue_used"] = 156
					character.Stats["fatigue_display_update"] = 156
					character.Stats[fatigue.DayKey] = day
				}
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				before := captureRound100RetryAssets(t, service, gate.dungeonID)
				conn.write.Reset()
				// Use the full upper-packet dispatcher for both retry and exit.
				dispatch := func(cmd uint16, body []byte) {
					t.Helper()
					packet, err := dnfproto.BuildChannelPacket(cmd, body, 0, dnfproto.DefaultChannelClassification)
					if err != nil {
						t.Fatal(err)
					}
					if err := service.handleGameUpper(session, packet); err != nil {
						t.Fatal(err)
					}
				}
				dispatch(uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 0})
				ack, reject := 0, 0
				for stream := conn.write.Bytes(); len(stream) > 0; {
					packet, rest := splitGameServerUpperPacket(t, stream)
					if packet.Header.Classification == byte(dnfenum.GameCmdCommand) {
						if packet.Header.MsgID == uint16(dnfenum.CmdPacketEplpCommand) && reflect.DeepEqual(packet.Body, buildCurrentDungeonOp72SuccessBody(1, 0)) {
							ack++
						}
						if packet.Header.MsgID == uint16(dnfenum.CmdPacketSelectDungeon) && len(packet.Body) >= 2 && packet.Body[0] == 0 && packet.Body[1] == gate.reason {
							reject++
						}
					} else if packet.Header.MsgID == uint16(currentDungeonStartNotification) || packet.Header.MsgID == currentSceneTransitionMsgID {
						t.Fatalf("refused retry unexpectedly transitioned scene: op%d", packet.Header.MsgID)
					}
					stream = rest
				}
				if ack != 1 || reject != 1 {
					t.Fatalf("expected one retry ACK and refusal %#x, ack=%d reject=%d stream=%x", gate.reason, ack, reject, conn.write.Bytes())
				}
				if session.dungeon.runtime != runtime || !service.currentCompletedDungeonActionReady(session, runtime) {
					t.Errorf("refused retry lost completed return owner: got=%p want=%p", session.dungeon.runtime, runtime)
				}
				afterRefusal := captureRound100RetryAssets(t, service, gate.dungeonID)
				if !reflect.DeepEqual(before.character.Stats, afterRefusal.character.Stats) || !reflect.DeepEqual(before.inventory, afterRefusal.inventory) || before.dailyUsed != afterRefusal.dailyUsed {
					t.Error("refused retry changed stats, inventory or daily entry count")
				}
				conn.write.Reset()
				dispatch(exit.cmd, exit.body)
				transitions := 0
				for stream := conn.write.Bytes(); len(stream) > 0; {
					packet, rest := splitGameServerUpperPacket(t, stream)
					if packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID {
						transitions++
						want := []byte{38, 1, 1, 0, 99, 0, 194, 1, 234, 0, 5, 3, 1}
						if !bytes.Equal(packet.Body, want) {
							t.Errorf("typed op24 lost frozen town origin: got=%x want=%x", packet.Body, want)
						}
					}
					stream = rest
				}
				if transitions != 1 {
					t.Errorf("return must emit one typed op24: got=%d", transitions)
				}
				// All three ordinary completed routes now retire through the
				// same successful settlement exit. A later town position packet
				// must not be needed to release the completed owner.
				if exit.cmd == uint16(dnfenum.CmdPacketBack2Village) {
					if session.dungeon.runtime != nil || !runtime.townReturnOp24Sent {
						t.Error("completed op132 did not retire after its successful town route")
					}
					dispatch(uint16(dnfenum.CmdPacketSetUserPosition), buildTownPositionRequest(450, 234, 5, 100))
				}
				if session.dungeon.runtime != nil {
					t.Errorf("confirmed return retained dungeon owner: %p", session.dungeon.runtime)
				}
				after := captureRound100RetryAssets(t, service, gate.dungeonID)
				if !reflect.DeepEqual(before.character.Stats, after.character.Stats) || !reflect.DeepEqual(before.inventory, after.inventory) || before.dailyUsed != after.dailyUsed {
					t.Error("refusal/return changed stats, inventory or daily entry count")
				}
				t.Logf("dungeon=%d path=%s difficulty=%d reject=%#x ack=%d refusal=%d return_op%d typed_op24=%d", gate.dungeonID, dungeon.Path, request.Difficulty, gate.reason, ack, reject, exit.cmd, transitions)
			})
		}
	}
}
