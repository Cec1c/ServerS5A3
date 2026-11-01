package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// TestGroundGoldPickupGrantsWalletExactlyOnceAndLeavesItemsUnchanged drives
// the normal game-upper pickup entry. The gold drop is created by the existing
// runtime drop planner, so this does not call the private wallet helper in its
// place.
func TestGroundGoldPickupGrantsWalletExactlyOnceAndLeavesItemsUnchanged(t *testing.T) {
	const (
		characterID     = "99"
		startingGold    = int64(1234)
		goldDropAmount  = uint32(100)
		goldWalletAfter = startingGold + int64(goldDropAmount)
	)

	service, runtime, repositories, dropObjectKey := prepareGroundGoldPickupTest(t, startingGold)
	if got := loadGroundGoldPickupWallet(t, repositories, characterID); got != startingGold {
		t.Fatalf("initial wallet=%d want=%d", got, startingGold)
	}
	assertGroundGoldPickupInventoryEmpty(t, repositories, characterID)

	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "ground-gold-pickup-audit-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	pickupBody := currentDungeonPickupTestBody(dropObjectKey)

	if err := service.handleCurrentDungeonPickup(session, pickupBody); err != nil {
		t.Fatalf("first pickup through game-upper entry: %v", err)
	}
	drop := runtime.DropOwner.byObjectKey[dropObjectKey]
	if drop == nil {
		t.Fatalf("gold drop %d disappeared after pickup", dropObjectKey)
	}
	if !drop.isGold() || drop.Amount != goldDropAmount {
		t.Fatalf("pickup drop is_gold=%t amount=%d want=100", drop.isGold(), drop.Amount)
	}
	if drop.Status != runtimeDungeonDropConsumed || drop.GoldAfter != goldWalletAfter {
		t.Fatalf("consumed gold drop status=%d gold_after=%d want=%d", drop.Status, drop.GoldAfter, goldWalletAfter)
	}
	if got := loadGroundGoldPickupWallet(t, repositories, characterID); got != goldWalletAfter {
		t.Fatalf("wallet after first pickup=%d want=%d", got, goldWalletAfter)
	}
	assertGroundGoldPickupInventoryEmpty(t, repositories, characterID)
	if connection.write.Len() == 0 {
		t.Fatal("first pickup committed wallet but emitted no response")
	}

	connection.write.Reset()
	if err := service.handleCurrentDungeonPickup(session, pickupBody); err != nil {
		t.Fatalf("duplicate pickup through game-upper entry: %v", err)
	}
	if got := loadGroundGoldPickupWallet(t, repositories, characterID); got != goldWalletAfter {
		t.Fatalf("duplicate pickup changed wallet=%d want=%d", got, goldWalletAfter)
	}
	if drop.Status != runtimeDungeonDropConsumed || drop.GoldAfter != goldWalletAfter {
		t.Fatalf("duplicate changed consumed drop status=%d gold_after=%d", drop.Status, drop.GoldAfter)
	}
	assertGroundGoldPickupInventoryEmpty(t, repositories, characterID)
	if connection.write.Len() == 0 {
		t.Fatal("duplicate pickup emitted no replay response")
	}
}

func TestGroundGoldPickupWalletOverflowLeavesDropAvailableAndCommitsNothing(t *testing.T) {
	const maxWallet int64 = 1<<63 - 1

	service, runtime, repositories, dropObjectKey := prepareGroundGoldPickupTest(t, maxWallet)
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "ground-gold-overflow-audit-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}

	if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(dropObjectKey)); err != nil {
		t.Fatalf("overflow pickup through game-upper entry: %v", err)
	}
	if got := loadGroundGoldPickupWallet(t, repositories, "99"); got != maxWallet {
		t.Fatalf("overflow pickup changed wallet=%d want=%d", got, maxWallet)
	}
	assertGroundGoldPickupInventoryEmpty(t, repositories, "99")
	drop := runtime.DropOwner.byObjectKey[dropObjectKey]
	if drop == nil {
		t.Fatalf("overflow gold drop %d disappeared", dropObjectKey)
	}
	if drop.Status != runtimeDungeonDropAvailable || drop.GoldAfter != 0 || len(drop.PickupResponseBody) != 0 {
		t.Fatalf("overflow consumed drop status=%d gold_after=%d response_len=%d", drop.Status, drop.GoldAfter, len(drop.PickupResponseBody))
	}
	if connection.write.Len() != 0 {
		t.Fatalf("overflow pickup emitted packet=%x", connection.write.Bytes())
	}
}

func prepareGroundGoldPickupTest(
	t *testing.T,
	startingGold int64,
) (*Service, *runtimeDungeonState, dnfrepo.Group, uint32) {
	t.Helper()
	service, runtime, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
	ctx := context.Background()

	character, found, err := repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load pickup character found=%t err=%v", found, err)
	}
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats["gold"] = startingGold
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatalf("seed pickup wallet: %v", err)
	}

	source, ok := service.dungeonMonsterTable.source.(bridgePVFSource)
	if !ok {
		t.Fatalf("test source type=%T", service.dungeonMonsterTable.source)
	}
	source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
	currentDungeonGoldReferencesByCatalog.Delete(service.dungeonMonsterTable)

	monster := runtime.Room.Snapshot().Monsters[0]
	wires, err := service.planCurrentDungeonMonsterDrops(runtime, monster, currentSceneActorObjectKey(99))
	if err != nil {
		t.Fatalf("plan gold drop: %v", err)
	}
	for index := range wires {
		if binary.LittleEndian.Uint32(wires[index].Item.data[0x02:0x06]) != 0 {
			continue
		}
		drop := runtime.DropOwner.byObjectKey[wires[index].SceneObjectKey]
		if drop == nil {
			t.Fatalf("planned gold object %d was not registered", wires[index].SceneObjectKey)
		}
		if !drop.isGold() || drop.Amount != 100 {
			t.Fatalf("registered gold drop=%+v wire_amount=%d", drop, binary.LittleEndian.Uint32(wires[index].Item.data[0x06:0x0A]))
		}
		return service, runtime, repositories, drop.ObjectKey
	}
	t.Fatalf("gold drop was not planned: wires=%+v", wires)
	return nil, nil, dnfrepo.Group{}, 0
}

func loadGroundGoldPickupWallet(t *testing.T, repositories dnfrepo.Group, characterID string) int64 {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load wallet character %q found=%t err=%v", characterID, found, err)
	}
	return character.Stats["gold"]
}

func assertGroundGoldPickupInventoryEmpty(t *testing.T, repositories dnfrepo.Group, characterID string) {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load pickup inventory found=%t err=%v", found, err)
	}
	if len(inventory.Slots) != 0 {
		t.Fatalf("gold pickup changed item inventory: slots=%+v", inventory.Slots)
	}
}
