package dnfbridge

import (
	"context"
	"maps"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// These tests are added through a Go overlay. They exercise the production op72
// dispatcher and the existing dungeon admission code without editing either
// source tree. The normal-dungeon fixture is a synthetic control-flow model. The
// rift fixture uses synthetic map data with the production rift gate; neither is
// evidence that a particular player map or native UI was tested.
func TestRound100RetryRejectionKeepsCompletedReturnOwner(t *testing.T) {
	t.Run("ordinary_no_fatigue", func(t *testing.T) {
		service, oldRuntime, session, conn := prepareOrdinaryCompletedSettlementRuntime(t)
		runtime, connection := oldRuntime, conn
		runtime.settlementPlayResultReceived = true
		runtime.settlementStatisticReceived = true
		markCurrentDungeonCardRewardCommittedForTest(runtime)

		repos, ok := service.repositoryProvider()
		if !ok {
			t.Fatal("repository group unavailable")
		}
		character, found, err := repos.Character.Load(context.Background(), "99")
		if err != nil || !found {
			t.Fatalf("load character found=%t err=%v", found, err)
		}
		character.Stats["fatigue"] = 0
		character.Stats["fatigue_limit"] = 156
		character.Stats["fatigue_used"] = 156
		character.Stats["fatigue_display_update"] = 156
		character.Stats[fatigue.DayKey] = premium.ServiceDayID(service.gameplayNow())
		character.Stats["gold"] = 987654321
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: "99",
			Slots: map[string]dnfrepo.ItemStack{
				"0:20": {ItemID: 10158964, Count: 7},
			},
		}); err != nil {
			t.Fatal(err)
		}
		authenticateRound100RetrySession(service, session)

		before := captureRound100RetryAssets(t, service, 700)
		connection.write.Reset()
		runRound100RetryRejectionAndTownReturn(t, service, session, connection, oldRuntime, before, 700, 0x16, true)
	})

	t.Run("rift_daily_limit", func(t *testing.T) {
		service, oldRuntime, session, conn, repos := prepareSyntheticRiftCompletedSettlementRuntime(t)
		runtime, connection := oldRuntime, conn
		runtime.settlementPlayResultReceived = true
		runtime.settlementStatisticReceived = true
		markCurrentDungeonCardRewardCommittedForTest(runtime)

		ctx := context.Background()
		if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{
			CharacterID: "99",
			States: map[int64]dnfrepo.QuestState{
				2888: {Status: "completed"},
			},
		}); err != nil {
			t.Fatal(err)
		}
		character, found, err := repos.Character.Load(ctx, "99")
		if err != nil || !found {
			t.Fatalf("load character found=%t err=%v", found, err)
		}
		dailyDayKey := "dungeon_daily_entry_316_day"
		dailyUsedKey := "dungeon_daily_entry_316_used"
		character.Stats[dailyDayKey] = premium.ServiceDayID(service.gameplayNow())
		character.Stats[dailyUsedKey] = currentRiftDailyLimit
		character.Stats["gold"] = 987654321
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: "99",
			Slots: map[string]dnfrepo.ItemStack{
				"0:20": {ItemID: 10158964, Count: 9},
			},
		}); err != nil {
			t.Fatal(err)
		}

		service.options.currentClientVersion = "official-24.9"
		authenticateRound100RetrySession(service, session)
		before := captureRound100RetryAssets(t, service, 316)
		connection.write.Reset()
		runRound100RetryRejectionAndTownReturn(t, service, session, connection, oldRuntime, before, 316, currentSelectDungeonMemberEntryLimitReason, false)
	})
}

func authenticateRound100RetrySession(service *Service, session *gameSession) {
	session.accountID = "account-1"
	session.loginPacketSeen = true
	session.accountFromToken = true
	service.bindGameSessionCharacter(session, session.selectedCharacterID)
}

type round100RetryAssets struct {
	character dnfrepo.CharacterRecord
	inventory dnfrepo.InventoryRecord
	dailyUsed int64
}

func captureRound100RetryAssets(t *testing.T, service *Service, dungeonID int64) round100RetryAssets {
	t.Helper()
	repos, ok := service.repositoryProvider()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	ctx := context.Background()
	character, found, err := repos.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	inventory, invFound, err := repos.Inventory.Load(ctx, "99")
	if err != nil || !invFound {
		t.Fatalf("load inventory found=%t err=%v", invFound, err)
	}
	used, err := dnfdungeon.DailyEntryUsed(character, dungeonID, service.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	return round100RetryAssets{
		character: dnfrepo.CloneCharacter(character),
		inventory: cloneRound100Inventory(inventory),
		dailyUsed: used,
	}
}

func cloneRound100Inventory(in dnfrepo.InventoryRecord) dnfrepo.InventoryRecord {
	copy := in
	copy.ContainerState = nil
	copy.Slots = make(map[string]dnfrepo.ItemStack, len(in.Slots))
	for key, stack := range in.Slots {
		if stack.RawEntry != nil {
			stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		}
		if stack.Extra != nil {
			stack.Extra = maps.Clone(stack.Extra)
		}
		copy.Slots[key] = stack
	}
	return copy
}

func runRound100RetryRejectionAndTownReturn(
	t *testing.T,
	service *Service,
	session *gameSession,
	connection *bufferConn,
	oldRuntime *runtimeDungeonState,
	before round100RetryAssets,
	dungeonID int64,
	wantReason byte,
	backToVillage bool,
) {
	t.Helper()
	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 0}); err != nil {
		t.Fatal(err)
	}

	retryAck, retryReject := false, false
	for stream := connection.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketEplpCommand) && reflect.DeepEqual(packet.Body, buildCurrentDungeonOp72SuccessBody(1, 0)) {
			retryAck = true
		}
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketSelectDungeon) && len(packet.Body) >= 2 && packet.Body[0] == 0 && packet.Body[1] == wantReason {
			retryReject = true
		}
		stream = rest
	}
	runtimeAfterRetry := session.dungeon.runtime
	readyAfterRetry := oldRuntime != nil && service.currentCompletedDungeonActionReady(session, oldRuntime)

	connection.write.Reset()
	if backToVillage {
		request, err := dnfproto.BuildChannelPacket(
			uint16(dnfenum.CmdPacketBack2Village),
			nil,
			0,
			dnfproto.DefaultChannelClassification,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.handleGameUpper(session, request); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 2}); err != nil {
			t.Fatal(err)
		}
	}
	townTransition := false
	for stream := connection.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID {
			townTransition = true
		}
		stream = rest
	}
	after := captureRound100RetryAssets(t, service, dungeonID)
	assetsEqual := reflect.DeepEqual(before.character.Stats, after.character.Stats) &&
		reflect.DeepEqual(before.inventory, after.inventory) && before.dailyUsed == after.dailyUsed

	if !retryAck || !retryReject {
		t.Fatalf("retry did not reach the expected refusal: ack=%t reject=%t dungeon=%d want_reason=%#x stream=%x",
			retryAck, retryReject, dungeonID, wantReason, connection.write.Bytes())
	}
	if runtimeAfterRetry != oldRuntime || !readyAfterRetry {
		t.Fatalf("retry refusal detached the completed return owner: runtime_after=%p old=%p ready=%t",
			runtimeAfterRetry, oldRuntime, readyAfterRetry)
	}
	if !townTransition {
		t.Fatalf("town return emitted no op24 after retry refusal: back_to_village=%t runtime=%p old=%p ready=%t",
			backToVillage, session.dungeon.runtime, oldRuntime, readyAfterRetry)
	}
	if !assetsEqual {
		t.Fatalf("retry refusal deducted assets: before_stats=%v after_stats=%v before_bag=%+v after_bag=%+v daily_before=%d daily_after=%d",
			before.character.Stats, after.character.Stats, before.inventory, after.inventory, before.dailyUsed, after.dailyUsed)
	}
	t.Logf("dungeon=%d refused=%#x back_to_village=%t, old completed owner retained, op24 emitted, assets unchanged", dungeonID, wantReason, backToVillage)
}

// The rift test intentionally uses synthetic map data. It exercises the real
// currentRiftEntryEnabled/currentRiftDailyLimit/daily-counter gate but does not
// claim that PVF dungeon 316 or a player's native run was validated.
func prepareSyntheticRiftCompletedSettlementRuntime(
	t *testing.T,
) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source[worldmap.DefaultMapList] = "100 `dungeon/test/start.map`\n"
	source["map/dungeon/test/start.map"] = strings.ReplaceAll(
		source["map/dungeon/test/start.map"],
		"[dungeon]\n700\n",
		"[dungeon]\n316\n",
	)
	source[worldmap.DefaultDungeonList] = "316 `test.dgn`\n"
	source["worldmap/test.wdm"] = "[name]\n`Synthetic Rift Flow`\n[dungeon]\n316 2888\n[/dungeon]\n"
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       20,
		Stats: map[string]int64{
			"fatigue":    100,
			"town_id":    38,
			"area_id":    1,
			"pos_x":      450,
			"pos_y":      234,
			"direction":  5,
			"area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repos, true },
	}
	conn := &bufferConn{}
	channel := channelcatalog.Channel{ServerID: 1, ID: 19, Type: 1, Name: "ch.19", Port: 10019}
	session := &gameSession{
		conn:                conn,
		connID:              "round100-synthetic-rift-retry",
		channel:             channel,
		residentChannel:     channel,
		selectedCharacterID: 99,
	}
	runtime, _, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{DungeonID: 316, Difficulty: 1},
	)
	if err != nil {
		t.Fatalf("prepare synthetic rift runtime: %v", err)
	}
	session.dungeon.runtime = runtime
	session.townMu.Lock()
	session.townSceneReadyCharacterID = 99
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID:   99,
		TownID:        38,
		AreaID:        1,
		PositionX:     450,
		PositionY:     234,
		MovementCode:  5,
		PositionValid: true,
	}
	session.townMu.Unlock()
	if _, bound := bindCurrentTownSelectorOrigin(session); !bound {
		t.Fatal("synthetic rift selector origin did not bind")
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatalf("synthetic rift origin freeze: %v", err)
	}
	completeSettlementRuntimeForTest(t, service, runtime, session)
	return service, runtime, session, conn, repos
}
