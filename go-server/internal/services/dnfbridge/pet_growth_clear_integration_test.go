package dnfbridge

import (
	"context"
	"strconv"
	"testing"

	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func seedClearingCreatureForTest(t *testing.T, service *Service, characterID uint16) dnfrepo.Group {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture repositories unavailable")
	}
	ctx := context.Background()
	id := strconv.Itoa(int(characterID))
	if err := repositories.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: id, EquippedKey: "37",
		Entries: map[string]dnfrepo.PetEntry{"37": {
			PetKey: "37", CreatureKey: 37, ItemID: 63000,
			SourceListType: 3, SourceSlotIndex: 26, Level: 1,
			Satiety: 100, SatietyMicros: 100_000_000,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	equipment, _, err := repositories.Equipment.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	equipment.CharacterID = id
	if equipment.Entries == nil {
		equipment.Entries = make(map[string]dnfrepo.EquipmentEntry)
	}
	equipment.Entries["26"] = dnfrepo.EquipmentEntry{SlotIndex: 26, ItemID: 63000, RawEntry: testCurrentEquippedCreatureRaw(26, 63000, 37)}
	if err := repositories.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	service.petCatalog, err = dnfpet.NewPVFCatalog(bridgePetCatalogFixture())
	if err != nil {
		t.Fatal(err)
	}
	return repositories
}

// This invokes the production op39 handler, not the previously orphaned helper.
// A fresh service loading the same repository proves durable state after a
// reconnect; duplicate death reports and special waves cannot award it again.
func TestPetGrowthProductionRoomClearPersistsAndRejectsDeathReplay(t *testing.T) {
	service, session, conn, runtime, scene := prepareHellPartyClearSettlementRuntime(t)
	repositories := seedClearingCreatureForTest(t, service, session.selectedCharacterID)
	runtime.lifecycleToken = 7
	runtime.fatigueRooms = map[worldmap.RoomCoordinate]bool{scene.Coordinate: true}
	keys := hellPartyRoomMonsterKeys(t, runtime)
	conn.write.Reset()
	killDungeonActors(t, service, session, keys[:len(keys)-1]...)
	if countPacketsWithID(t, conn.write.Bytes(), currentCreatureGrowthMsgID) != 0 {
		t.Fatal("pet experience was sent before the room was cleared")
	}
	killDungeonActors(t, service, session, keys[len(keys)-1])
	if countPacketsWithID(t, conn.write.Bytes(), currentCreatureGrowthMsgID) != 1 {
		t.Fatal("production room clear did not send exactly one growth notification")
	}
	killDungeonActors(t, service, session, keys[len(keys)-1])
	killDungeonActors(t, service, session, waveHellPartyActorKeys(t, runtime)...)
	if countPacketsWithID(t, conn.write.Bytes(), currentCreatureGrowthMsgID) != 1 {
		t.Fatal("death replay or wave retirement duplicated room growth")
	}
	fresh := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true }}
	group, _ := fresh.repositoryGroup()
	entry, err := currentPetGrowthEntry(context.Background(), group, session.selectedCharacterID, "37")
	if err != nil || entry.Exp != 1 || entry.Level != 1 || len(entry.AppliedClearTokens) != 1 {
		t.Fatalf("reconnect projection entry=%+v err=%v", entry, err)
	}
}

func TestPetGrowthClearRequiresAuthoritativeFatigueRoom(t *testing.T) {
	for _, name := range []string{"uncleared", "no_fatigue", "not_debited", "no_run_identity"} {
		t.Run(name, func(t *testing.T) {
			service, session, conn, runtime, scene := prepareHellPartyClearSettlementRuntime(t)
			repos := seedClearingCreatureForTest(t, service, session.selectedCharacterID)
			runtime.lifecycleToken = 7
			runtime.fatigueRooms = map[worldmap.RoomCoordinate]bool{scene.Coordinate: true}
			scene.Cleared = true
			switch name {
			case "uncleared":
				scene.Cleared = false
			case "no_fatigue":
				runtime.Dungeon.Metadata.NoFatigue = true
			case "not_debited":
				runtime.fatigueRooms = nil
			case "no_run_identity":
				runtime.lifecycleToken = 0
			}
			conn.write.Reset()
			service.awardCurrentPetRoomClearLocked(session, runtime, scene, "rejected_fixture")
			entry, err := currentPetGrowthEntry(context.Background(), repos, session.selectedCharacterID, "37")
			if err != nil || entry.Exp != 0 || conn.write.Len() != 0 || len(entry.AppliedClearTokens) != 0 {
				t.Fatalf("invalid clear awarded growth entry=%+v err=%v bytes=%d", entry, err, conn.write.Len())
			}
		})
	}
}
