package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// TestPartyMultiplayerEntryConsistencyAcrossTwoToFourMembers uses one synthetic
// map and the same deterministic run seed for 2-, 3-, and 4-member parties.
// This is a protocol/runtime regression fixture, not a real-game acceptance run.
func TestPartyMultiplayerEntryConsistencyAcrossTwoToFourMembers(t *testing.T) {
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	t.Cleanup(func() { partyDungeonLoadingBarrierDispatch = restoreDispatch })

	characterIDs := []uint16{0x3a17, 0x68d1, 0x14be, 0x72f3}
	for _, memberCount := range []int{2, 3, 4} {
		memberCount := memberCount
		t.Run(fmt.Sprintf("%d_members", memberCount), func(t *testing.T) {
			runPartyMultiplayerEntryConsistencyFixture(
				t,
				append([]uint16(nil), characterIDs[:memberCount]...),
			)
		})
	}
}

func runPartyMultiplayerEntryConsistencyFixture(t *testing.T, characterIDs []uint16) {
	t.Helper()
	if len(characterIDs) < 2 || len(characterIDs) > 4 {
		t.Fatalf("fixture member count=%d want 2..4", len(characterIDs))
	}

	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(
		source["dungeon/test.dgn"],
		"[limit party count]\n1\n",
		"[limit party count]\n4\n",
		1,
	)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	startMapDefinition, found := table.FindMap(100)
	if !found || len(startMapDefinition.Monsters) == 0 {
		t.Fatalf("synthetic PVF start map=%d found=%t monsters=%d", 100, found, len(startMapDefinition.Monsters))
	}

	ctx := context.Background()
	const accountID = "synthetic-party-entry-consistency-owner"
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID}); err != nil {
		t.Fatal(err)
	}
	for _, characterID := range characterIDs {
		id := strconv.FormatUint(uint64(characterID), 10)
		if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: id,
			AccountID:   accountID,
			Level:       20,
			Stats: map[string]int64{
				"fatigue": 100, "town_id": 38, "area_id": 1,
				"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
			CharacterID: id,
			Slots:       map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}
	}

	const expectedSeed uint32 = 0x6d2b79f5
	service := &Service{
		options:             options{accountID: accountID, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return expectedSeed, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	channel := channelcatalog.Channel{ServerID: 1, ID: 42, Type: 11, Name: "ch.42", Port: 10042}
	partyState := alignedcmd.PartyState{
		PartyID:    0x2d17,
		UserID:     characterIDs[0],
		IsLeader:   true,
		MaxMembers: 4,
		Members:    make([]alignedcmd.PartyMemberState, 0, len(characterIDs)),
	}
	for slot, characterID := range characterIDs {
		partyState.Members = append(partyState.Members, alignedcmd.PartyMemberState{
			UserID:    characterID,
			Slot:      byte(slot),
			UserState: 1,
			HPPercent: 100,
			MPPercent: 100,
		})
	}

	sessions := make([]*gameSession, len(characterIDs))
	connections := make([]*bufferConn, len(characterIDs))
	for index, characterID := range characterIDs {
		connection := &bufferConn{}
		session := &gameSession{
			conn:                connection,
			channel:             channel,
			residentChannel:     channel,
			selectedCharacterID: characterID,
		}
		service.bindGameSessionCharacter(session, characterID)
		storeRuntimePartyState(session, partyState)
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: characterID,
			TownID:      38,
			AreaID:      1,
			Session:     session,
		})
		bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
		sessions[index], connections[index] = session, connection
	}

	requestBody := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(requestBody[:4], 700)
	if err := service.handleDungeonSelectUpper(sessions[0], requestBody); err != nil {
		t.Fatalf("leader handleDungeonSelectUpper: %v", err)
	}

	for index, session := range sessions {
		if !service.partyDungeonPostStartMapPlacementPending(session) {
			t.Fatalf("member index=%d character=%#x post-start-map placement was not deferred", index, session.selectedCharacterID)
		}
		runtime := session.dungeon.runtime
		if runtime == nil {
			t.Fatalf("member index=%d character=%#x has no dungeon runtime", index, session.selectedCharacterID)
		}
		if !runtime.partyMemberIndexed || runtime.partyMemberIndex != byte(index) {
			t.Fatalf("member index=%d character=%#x runtime slot=%d indexed=%t",
				index, session.selectedCharacterID, runtime.partyMemberIndex, runtime.partyMemberIndexed)
		}
		if runtime.Seed != expectedSeed {
			t.Fatalf("member index=%d character=%#x seed=%#x want=%#x",
				index, session.selectedCharacterID, runtime.Seed, expectedSeed)
		}
		if err := service.sendFinishLoadingStatus(
			session,
			make([]byte, currentFinishLoadingLegacyRequestBodySize),
		); err != nil {
			t.Fatalf("member index=%d character=%#x sendFinishLoadingStatus: %v",
				index, session.selectedCharacterID, err)
		}
	}

	op29Bodies := make([][]byte, len(sessions))
	for index, connection := range connections {
		bodies := partyMultiplayerEntryOp29Bodies(t, connection.write.Bytes())
		if len(bodies) != 1 {
			t.Fatalf("member index=%d character=%#x op29 count=%d want=1",
				index, sessions[index].selectedCharacterID, len(bodies))
		}
		op29Bodies[index] = bodies[0]
		// The native reader uses this byte as the affected member, not as the
		// receiving socket's identity. A shared transition must bind all actors.
		if len(op29Bodies[index]) == 0 || op29Bodies[index][len(op29Bodies[index])-1] != 0xff {
			t.Fatalf("member index=%d character=%#x op29 affected-member tail=%x want=ff",
				index, sessions[index].selectedCharacterID, op29Bodies[index])
		}
	}

	canonicalStartMap := op29Bodies[0]
	for index := 1; index < len(op29Bodies); index++ {
		body := op29Bodies[index]
		if len(body) != len(canonicalStartMap) {
			t.Fatalf("member index=%d START_MAP length=%d want=%d", index, len(body), len(canonicalStartMap))
		}
		if !bytes.Equal(body, canonicalStartMap) {
			t.Fatalf("member index=%d shared START_MAP differs\n got=%x\nwant=%x",
				index, body, canonicalStartMap)
		}
	}

	leaderRuntime := sessions[0].dungeon.runtime
	sourceRoom := leaderRuntime.Room
	instance := leaderRuntime.partyInstance
	if sourceRoom == nil || instance == nil {
		t.Fatalf("leader shared room=%p party instance=%p", sourceRoom, instance)
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("party dungeon instance count=%d want=1", got)
	}
	if got := instance.memberCount(); got != len(sessions) {
		t.Fatalf("shared party instance member count=%d want=%d", got, len(sessions))
	}
	if instance.room != sourceRoom {
		t.Fatalf("party instance room=%p leader runtime room=%p want shared", instance.room, sourceRoom)
	}

	sourceSnapshot := sourceRoom.Snapshot()
	if got, want := len(sourceSnapshot.Monsters), len(startMapDefinition.Monsters); got != want {
		t.Fatalf("unique source room monster count=%d want=%d party_size=%d", got, want, len(sessions))
	}
	wantObjectKeys := make([]uint32, len(startMapDefinition.Monsters))
	for index := range wantObjectKeys {
		wantObjectKeys[index] = firstDungeonMonsterObjectKey + uint32(index)
	}
	sourceObjectKeys := partyMultiplayerMonsterObjectKeys(sourceSnapshot.Monsters)
	if !reflect.DeepEqual(sourceObjectKeys, wantObjectKeys) {
		t.Fatalf("unique source room object keys=%v want=%v", sourceObjectKeys, wantObjectKeys)
	}

	for index, session := range sessions {
		runtime := session.dungeon.runtime
		if runtime == nil || runtime.partyInstance != instance || runtime.Room != sourceRoom {
			t.Fatalf("member index=%d character=%#x instance=%p/%p room=%p/%p",
				index, session.selectedCharacterID,
				func() any {
					if runtime == nil {
						return nil
					}
					return runtime.partyInstance
				}(), instance,
				func() any {
					if runtime == nil {
						return nil
					}
					return runtime.Room
				}(), sourceRoom)
		}
		if !runtime.partyMemberIndexed || runtime.partyMemberIndex != byte(index) {
			t.Fatalf("member index=%d character=%#x committed runtime slot=%d indexed=%t",
				index, session.selectedCharacterID, runtime.partyMemberIndex, runtime.partyMemberIndexed)
		}
		roomSnapshot := runtime.Room.Snapshot()
		if len(roomSnapshot.Monsters) != len(startMapDefinition.Monsters) {
			t.Fatalf("member index=%d room monster count=%d want=%d party_size=%d",
				index, len(roomSnapshot.Monsters), len(startMapDefinition.Monsters), len(sessions))
		}
		if got := partyMultiplayerMonsterObjectKeys(roomSnapshot.Monsters); !reflect.DeepEqual(got, wantObjectKeys) {
			t.Fatalf("member index=%d room object keys=%v want=%v", index, got, wantObjectKeys)
		}
		if got := partyMultiplayerStartMapObjectKeys(t, op29Bodies[index]); !reflect.DeepEqual(got, wantObjectKeys) {
			t.Fatalf("member index=%d START_MAP object keys=%v want=%v", index, got, wantObjectKeys)
		}
		assertDungeonPartyRealtimeSnapshot(t, connections[index].write.Bytes(), byte(len(sessions)))
		assertDungeonRoomSlotTableCount(t, connections[index].write.Bytes(), session.selectedCharacterID, 1)
	}
}

func partyMultiplayerEntryOp29Bodies(t *testing.T, stream []byte) [][]byte {
	t.Helper()
	bodies := make([][]byte, 0, 1)
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentDungeonStartNotification {
			bodies = append(bodies, append([]byte(nil), packet.Body...))
		}
		stream = rest
	}
	return bodies
}

func partyMultiplayerStartMapObjectKeys(t *testing.T, body []byte) []uint32 {
	t.Helper()
	if len(body) < 19 {
		t.Fatalf("START_MAP body too short: len=%d body=%x", len(body), body)
	}
	const actorPrefixBytes = 19
	const actorRowBytes = 21
	actorCount := int(body[18])
	actorEnd := actorPrefixBytes + actorCount*actorRowBytes
	if actorEnd > len(body) {
		t.Fatalf("START_MAP actor rows exceed body: count=%d end=%d len=%d body=%x",
			actorCount, actorEnd, len(body), body)
	}
	keys := make([]uint32, actorCount)
	for index := 0; index < actorCount; index++ {
		offset := actorPrefixBytes + index*actorRowBytes
		keys[index] = uint32(binary.LittleEndian.Uint16(body[offset+6 : offset+8]))
	}
	return keys
}

func partyMultiplayerMonsterObjectKeys(monsters []runtimeDungeonMonster) []uint32 {
	keys := make([]uint32, len(monsters))
	for index := range monsters {
		keys[index] = monsters[index].ObjectKey
	}
	return keys
}
