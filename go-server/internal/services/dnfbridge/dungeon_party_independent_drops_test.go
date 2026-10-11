package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPartyOrdinaryKillGivesNonKillerOwnedPickableDrops(t *testing.T) {
	service, runtime, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
	source := service.dungeonMonsterTable.source.(bridgePVFSource)
	source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
	currentDungeonGoldReferencesByCatalog.Delete(service.dungeonMonsterTable)
	monster := runtime.Room.Snapshot().Monsters[0]
	source[currentDungeonIndependentDropPath] = "[independent drop]\n0 " + strconv.FormatInt(monster.Spawn.MonsterID, 10) + " 3227 1000000 1000000 0 0 0 1 1 0 0 0 1 90 -1 0\n[/independent drop]\n"
	currentDungeonSpecialDropsByMonsterCatalog.Delete(service.dungeonMonsterTable)
	character := runtime.Character
	character.CharacterID = "100"
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "100", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	leader := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 100}
	peerRuntime, _, err := service.prepareDungeonRuntime(context.Background(), peer, runtime.Request)
	if err != nil {
		t.Fatal(err)
	}
	peer.dungeon.runtime = peerRuntime
	state := runtimePartyStateForSyncFixture()
	for _, member := range []*gameSession{leader, peer} {
		storeRuntimePartyState(member, state)
		service.bindGameSessionCharacter(member, member.selectedCharacterID)
		current := member.dungeon.runtime
		if _, err := current.Room.AnnounceAllActors(current.Session); err != nil && !errors.Is(err, errDungeonActorAlreadyAnnounced) {
			t.Fatal(err)
		}
		scene, _ := current.Session.Scene()
		if service.adoptPartyDungeonInstance(member, current, scene) == nil {
			t.Fatal("shared instance missing")
		}
	}
	runtime.partyInstance.deadMembers = map[uint16]bool{100: true}
	killDungeonActors(t, service, leader, monster.ObjectKey)
	keys := map[uint16]uint32{}
	for _, member := range []*gameSession{leader, peer} {
		for _, body := range abyssOp38Bodies(t, member.conn.(*bufferConn).write.Bytes()) {
			for index, row := range abyssEpicFragmentDropRows(t, body) {
				if owner := binary.LittleEndian.Uint16(body[abyssDropRowOwnerOffset(index):]); owner != member.selectedCharacterID {
					t.Fatalf("foreign drop delivered to %d: owner %d", member.selectedCharacterID, owner)
				}
				if row.ItemID == 3227 {
					keys[member.selectedCharacterID] = row.ObjectKey
				}
			}
		}
	}
	if keys[99] == 0 || keys[100] == 0 || keys[99] == keys[100] {
		t.Fatalf("nonkiller must get a separate material: %v", keys)
	}
	scene, _ := runtime.Session.Scene()
	roomKey := runtimeDungeonRoomKeyFromScene(scene)
	if _, err := runtime.DropOwner.owned(keys[100], roomKey, 99); !errors.Is(err, errDungeonDropActorMismatch) {
		t.Fatalf("foreign pickup admitted: %v", err)
	}
	before := len(runtime.DropOwner.byObjectKey)
	killDungeonActors(t, service, peer, monster.ObjectKey)
	if len(runtime.DropOwner.byObjectKey) != before {
		t.Fatal("duplicate death rolled drops twice")
	}
	// Reviving the nonkiller permits picking up the already owned ground item.
	runtime.partyInstance.deadMembers[100] = false
	for _, member := range []*gameSession{leader, peer} {
		if err := service.handleCurrentDungeonPickup(member, currentDungeonPickupTestBody(keys[member.selectedCharacterID])); err != nil {
			t.Fatal(err)
		}
		inventory, found, err := repositories.Inventory.Load(context.Background(), strconv.Itoa(int(member.selectedCharacterID)))
		if err != nil || !found {
			t.Fatalf("inventory unavailable: %v", err)
		}
		var amount int64
		for _, stack := range inventory.Slots {
			if stack.ItemID == 3227 {
				amount += stack.Count
			}
		}
		if amount != 1 {
			t.Fatalf("member %d picked amount %d", member.selectedCharacterID, amount)
		}
	}
}

func TestPartyEpicFragmentContentsAreRolledSeparately(t *testing.T) {
	service, leader, _, leaderConn, peerConn, runtime, _, _ := partyAbyssPillarFixture(t)
	killDungeonActors(t, service, leader, waveHellPartyActorKeys(t, runtime)...)
	sequences := make([][]uint32, 2)
	for member, data := range [][]byte{leaderConn.write.Bytes(), peerConn.write.Bytes()} {
		for _, body := range abyssOp38Bodies(t, data) {
			for _, row := range abyssEpicFragmentDropRows(t, body) {
				if row.ItemID >= 10300000 {
					sequences[member] = append(sequences[member], row.ItemID)
				}
			}
		}
	}
	if len(sequences[0]) != abyssEpicFragmentTestDefaultAtDifficultyOne || len(sequences[1]) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("each member's guarantee changed: %v", sequences)
	}
	if len(sequences[0]) == len(sequences[1]) {
		same := true
		for index, id := range sequences[0] {
			if id != sequences[1][index] {
				same = false
			}
		}
		if same {
			t.Fatalf("fixed fixture reproduced cloned fragment contents: %v", sequences)
		}
	}
}

// Keep a death notification's native optional-room tail through per-owner filtering.
func TestPartyDropProjectionRetainsStoryProgressionTail(t *testing.T) {
	_, runtime, _ := prepareCurrentDungeonDropTest(t, 0, 3227)
	runtime.partyInstance = &dungeonPartyInstance{members: map[uint16]time.Time{}}
	item, err := currentDungeonDeathDropItemState(1, dungeonDropItemDefinition{ItemID: 3227, Kind: dungeonDropItemStackable}, 1)
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(500, currentDungeonDeathResponseMonster, []currentDungeonDeathDropWire{{SceneObjectKey: 600, Item: item, OwnerActorObjectKey: 99}})
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-1] = 1
	body = append(body, 2, 3)
	filtered := partyDungeonDeathBodyForRecipient(runtime, body, 100)
	if len(filtered) != 9 || filtered[2] != 0 || filtered[6] != 1 || filtered[7] != 2 || filtered[8] != 3 {
		t.Fatalf("progression lost: %x", filtered)
	}
}
