package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFTournamentBlackDragonAPCFourRoundHandlerCurrentPVF exercises
// the production op39 handler against the real 122 APC bracket. Each accepted
// kill must advance exactly one tour, while future and duplicate targets must
// not mutate the bracket or clear the room early.
func TestRealScriptPVFTournamentBlackDragonAPCFourRoundHandlerCurrentPVF(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the 122 APC handler test")
	}
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)

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
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:20": {ItemID: 3329, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		dungeonAICharacterTable: aiCharacters,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 0x5EED1234, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	channel := channelcatalog.Channel{ServerID: 1, ID: 207, Type: 1, Name: "ch.207", Port: 10207}
	session := &gameSession{
		conn:                  connection,
		connID:                "real-122-apc-four-round-handler",
		channel:               channel,
		residentChannel:       channel,
		selectedCharacterID:   19,
		townActorOwnerChannel: byte(channel.ID),
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)

	selectBody := make([]byte, 21)
	binary.LittleEndian.PutUint32(selectBody[0:4], 122)
	binary.LittleEndian.PutUint16(selectBody[9:11], 0xffff)
	request, err := dungeoncmd.DecodeSelectDungeonRequest(selectBody)
	if err != nil {
		t.Fatal(err)
	}
	runtimeState, scene, err := service.prepareDungeonRuntime(context.Background(), session, request)
	if err != nil {
		t.Fatalf("prepare real 122: %v", err)
	}
	if runtimeState.Tournament == nil || runtimeState.Tournament.Definition == nil {
		t.Fatal("real 122 prepared without a tournament runtime")
	}
	if runtimeState.Tournament.Definition.PartyLimit != 1 {
		t.Fatalf("real 122 party limit=%d, test currently proves the actual one-player four-round bracket",
			runtimeState.Tournament.Definition.PartyLimit)
	}
	if scene.Map.Map.ID != runtimeState.Tournament.Definition.MapID {
		t.Fatalf("scene map=%d tournament map=%d", scene.Map.Map.ID, runtimeState.Tournament.Definition.MapID)
	}
	session.dungeon.runtime = runtimeState
	if _, err := runtimeState.Room.AnnounceAllActors(runtimeState.Session); err != nil {
		t.Fatalf("announce tournament APC actors: %v", err)
	}

	room := runtimeState.Room.Snapshot()
	pathActors := runtimeState.Tournament.snapshot().PathActors
	if len(room.Monsters) != 0 || len(room.ExtendedActors) != tournamentRoundCount ||
		len(pathActors) != tournamentRoundCount {
		t.Fatalf("122 projection monsters=%d extended=%d path=%d want=(0,%d,%d)",
			len(room.Monsters), len(room.ExtendedActors), len(pathActors),
			tournamentRoundCount, tournamentRoundCount)
	}
	if got := runtimeState.Tournament.boundFirstSequence(); got != uint16(room.ExtendedActors[0].ObjectKey) {
		t.Fatalf("firstSequence=%d want first extended APC=%d", got, room.ExtendedActors[0].ObjectKey)
	}
	for index, actor := range room.ExtendedActors {
		want := pathActors[index]
		if actor.Kind != runtimeDungeonActorAICharacter ||
			actor.HostileReference == nil ||
			actor.Packet.Code != uint32(want.Code) ||
			actor.Packet.Level != want.Level ||
			actor.Packet.Type != want.Type ||
			actor.Packet.Blocking == 0 {
			t.Fatalf("APC actor %d=%+v want code/level/type/blocking=%d/%d/%d/nonzero",
				index, actor, want.Code, want.Level, want.Type)
		}
		if actor.AICharacter == nil || actor.AICharacter.Faction != "[monster]" {
			t.Fatalf("APC actor %d lost hostile AICharacter ownership: %+v", index, actor.AICharacter)
		}
	}

	ownerKey := currentSceneActorObjectKey(session.selectedCharacterID)
	kill := func(objectKey uint32) {
		t.Helper()
		if err := service.handleDungeonMonsterDeath(
			session,
			seekerQST016OfficialCombatDeathBody(objectKey, ownerKey),
		); err != nil {
			t.Fatalf("op39 target=%d: %v", objectKey, err)
		}
	}
	futureKey := room.ExtendedActors[1].ObjectKey
	connection.write.Reset()
	kill(futureKey)
	afterFuture := runtimeState.Room.Snapshot()
	if afterFuture.ExtendedActors[1].State != runtimeDungeonMonsterAnnounced ||
		runtimeState.Tournament.currentRoundNumber() != 1 ||
		connection.write.Len() != 0 {
		t.Fatalf("future-round APC was accepted: state=%s round=%d reply_len=%d",
			afterFuture.ExtendedActors[1].State,
			runtimeState.Tournament.currentRoundNumber(),
			connection.write.Len())
	}

	defeatedBeforeInvalid := len(runtimeState.Session.Snapshot().Scene.DefeatedObjects)
	kill(uint32(0xfffe))
	if got := len(runtimeState.Session.Snapshot().Scene.DefeatedObjects); got != defeatedBeforeInvalid {
		t.Fatalf("invalid target changed defeated objects=%d want=%d", got, defeatedBeforeInvalid)
	}

	for roundIndex := 0; roundIndex < tournamentRoundCount; roundIndex++ {
		before := runtimeState.Room.Snapshot()
		key := before.ExtendedActors[roundIndex].ObjectKey
		kill(key)
		after := runtimeState.Room.Snapshot()
		if after.ExtendedActors[roundIndex].State != runtimeDungeonMonsterDefeated {
			t.Fatalf("round %d APC state=%s", roundIndex+1, after.ExtendedActors[roundIndex].State)
		}
		if roundIndex < tournamentRoundCount-1 {
			if runtimeState.Tournament.currentRoundNumber() != byte(roundIndex+2) {
				t.Fatalf("after round %d current=%d want=%d",
					roundIndex+1, runtimeState.Tournament.currentRoundNumber(), roundIndex+2)
			}
			if runtimeState.Session.Snapshot().Scene.Cleared {
				t.Fatalf("round %d cleared the shared room before the final APC", roundIndex+1)
			}
		}

		currentRound := runtimeState.Tournament.currentRoundNumber()
		defeated := len(runtimeState.Session.Snapshot().Scene.DefeatedObjects)
		reward := runtimeState.tournamentReward
		connection.write.Reset()
		kill(key)
		if runtimeState.Tournament.currentRoundNumber() != currentRound ||
			len(runtimeState.Session.Snapshot().Scene.DefeatedObjects) != defeated ||
			runtimeState.tournamentReward != reward ||
			connection.write.Len() != 0 {
			t.Fatalf("duplicate round %d request re-settled: round=%d/%d defeated=%d/%d reward_changed=%t reply_len=%d",
				roundIndex+1,
				runtimeState.Tournament.currentRoundNumber(), currentRound,
				len(runtimeState.Session.Snapshot().Scene.DefeatedObjects), defeated,
				runtimeState.tournamentReward != reward,
				connection.write.Len())
		}
	}

	finalScene := runtimeState.Session.Snapshot().Scene
	if !finalScene.Cleared || !runtimeState.Tournament.isChampion() {
		t.Fatalf("final APC did not reach the original champion/clear path: cleared=%t terminal=%+v",
			finalScene.Cleared, runtimeState.Tournament.snapshot())
	}
	if runtimeState.tournamentReward == nil || !runtimeState.ordinaryFinalRoomClearAccepted {
		t.Fatalf("final APC did not freeze the reward and enter the original clear flow: reward=%t clear_accepted=%t",
			runtimeState.tournamentReward != nil, runtimeState.ordinaryFinalRoomClearAccepted)
	}
	if runtimeState.Dungeon.ID != 122 {
		t.Fatalf("tournament runtime changed dungeon identity to %d", runtimeState.Dungeon.ID)
	}
}

func seekerQST016OfficialCombatDeathBody(objectKey uint32, ownerObjectKey uint16) []byte {
	// Current writer: 31-byte prefix, one 14-byte row, 39-byte tail and 4-byte footer.
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
	binary.LittleEndian.PutUint32(body[0:4], objectKey)
	binary.LittleEndian.PutUint16(body[4:6], ownerObjectKey)
	body[30] = 1
	return body
}

func TestTournamentActorSequenceRejectsReorderedOrGappedProjection(t *testing.T) {
	ref := worldmap.HostileReference{Kind: worldmap.HostileAICharacter, Index: 0}
	for _, tc := range []struct {
		name   string
		key    uint32
		path   []tournamentPathActor
		wantOK bool
	}{
		{"contiguous", 102, []tournamentPathActor{{Code: 11, Type: 0}, {Code: 22, Type: 5}}, true},
		{"reordered mixed path", 102, []tournamentPathActor{{Code: 22, Type: 5}, {Code: 11, Type: 0}}, false},
		{"special actor gap", 103, []tournamentPathActor{{Code: 11, Type: 0}, {Code: 22, Type: 5}}, false},
		{"wrong APC rank", 102, []tournamentPathActor{{Code: 11, Type: 0}, {Code: 22, Type: 8}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := runtimeDungeonRoomSnapshot{
				Monsters:       []runtimeDungeonMonster{{ObjectKey: 101, Spawn: worldmap.MonsterSpawn{MonsterID: 11}}},
				ExtendedActors: []runtimeDungeonExtendedActor{{ObjectKey: tc.key, HostileReference: &ref, Packet: currentDungeonStartMapActor{Code: 22, Type: 5}}},
			}
			first, err := tournamentFirstActorObjectKey(snapshot, tc.path)
			if (err == nil) != tc.wantOK || (tc.wantOK && first != 101) {
				t.Fatalf("first=%d err=%v wantOK=%t", first, err, tc.wantOK)
			}
		})
	}
}
