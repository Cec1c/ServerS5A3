package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPetSceneReadyEnchantFollowsEquipmentObjectCreation(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "after_scene_ready", true: "rollback"}[rollback], func(t *testing.T) {
			if rollback {
				t.Setenv(loginEquipmentSceneReadyReplayEnv, "before_transition_only")
			} else {
				t.Setenv(loginEquipmentSceneReadyReplayEnv, "after_scene_ready")
			}
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			worn := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
				"26": {SlotIndex: 26, ItemID: 400990167, RawEntry: testCurrentEquippedCreatureEnchantRaw(26, 400990167, 37, 0, 0), Extra: map[string]string{"current_exe_equipment_type": "26"}},
			}}
			pets := dnfrepo.PetRecord{CharacterID: "19", EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
				"37": {PetKey: "37", CreatureKey: 37, ItemID: 400990167, Level: 7, Extra: map[string]string{"pet_enchant_card_item_id": "10008663"}},
			}}
			if err := repos.Equipment.Save(ctx, worn); err != nil {
				t.Fatal(err)
			}
			if err := repos.Pet.Save(ctx, pets); err != nil {
				t.Fatal(err)
			}
			service := newLoginEquipmentTestService(repos)
			conn := &bufferConn{}
			session := newLoginEquipmentTestSession(conn, 19)
			session.initialTownRouteCharacterID = 19
			session.initialTownRouteStage = currentInitialTownRouteTransitionSent
			if err := service.replayLoginEquipmentSceneReadyContainer(session, "owned_cold_login"); err != nil {
				t.Fatal(err)
			}
			stream := conn.write.Bytes()
			if rollback {
				if len(stream) != 0 {
					t.Fatal("rollback emitted scene-ready refresh")
				}
				return
			}
			created, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
			if created.Header.Classification != 0 || created.Header.MsgID != 13 || len(created.Body) < 3 || created.Body[0] != 3 {
				t.Fatalf("first packet did not create worn container: %+v", created.Header)
			}
			if card := binary.LittleEndian.Uint32(created.Body[3+0x0E : 3+0x12]); card != 10008663 {
				t.Fatalf("pet object creation card=%d, want durable enchant 10008663", card)
			}
			refreshed, trailing := splitCurrentGameServerUpperPacketAuto(t, rest)
			assertEquippedCreatureItemRefreshPacket(t, refreshed, 400990167, 37, 10008663, 0)
			if len(trailing) != 0 {
				t.Fatal("scene-ready replay emitted actor or creature initialization")
			}
			afterWorn, _, err := repos.Equipment.Load(ctx, "19")
			if err != nil || !reflect.DeepEqual(worn, afterWorn) {
				t.Fatal("scene-ready projection changed worn state")
			}
			afterPets, _, err := repos.Pet.Load(ctx, "19")
			if err != nil || !reflect.DeepEqual(pets, afterPets) {
				t.Fatal("scene-ready projection changed pet state")
			}
		})
	}
}

func TestPetContainerCreationUsesExactDurableEnchantIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		legacyCard, petItem, petSerial, wantCard uint32
	}{
		{"legacy_worn_card", 10008663, 400990167, 37, 10008663},
		{"durable_exact_match", 0, 400990167, 37, 10008663},
		{"different_pet_serial", 0, 400990167, 38, 0},
		{"different_template", 0, 400990168, 37, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			worn := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
				"26": {SlotIndex: 26, ItemID: 400990167, RawEntry: testCurrentEquippedCreatureEnchantRaw(26, 400990167, 37, tc.legacyCard, 0), Extra: map[string]string{"current_exe_equipment_type": "26"}},
			}}
			pets := dnfrepo.PetRecord{CharacterID: "19", Entries: map[string]dnfrepo.PetEntry{
				"pet": {PetKey: "pet", ItemID: int64(tc.petItem), Extra: map[string]string{"creature_serial": fmt.Sprint(tc.petSerial), "pet_enchant_card_item_id": "10008663"}},
			}}
			if err := repos.Equipment.Save(ctx, worn); err != nil {
				t.Fatal(err)
			}
			if err := repos.Pet.Save(ctx, pets); err != nil {
				t.Fatal(err)
			}
			entries, known, err := currentItemListEntriesFromEquipment(ctx, repos.Equipment, "19", repos.Pet)
			if err != nil || !known || len(entries) != 1 {
				t.Fatalf("entries=%d known=%t err=%v", len(entries), known, err)
			}
			row := entries[0].data[:]
			if serial := binary.LittleEndian.Uint32(row[6:10]); serial != 37 {
				t.Fatalf("serial=%d", serial)
			}
			if card := binary.LittleEndian.Uint32(row[14:18]); card != tc.wantCard {
				t.Fatalf("card=%d want=%d", card, tc.wantCard)
			}
			if row[18] != 0 {
				t.Fatalf("legacy card byte polluted upgrade=%d", row[18])
			}
			if seconds := binary.LittleEndian.Uint32(row[currentPetRemainSecondsOffset : currentPetRemainSecondsOffset+4]); seconds != 0 {
				t.Fatalf("legacy repeated serial became duration=%d", seconds)
			}
			afterWorn, _, _ := repos.Equipment.Load(ctx, "19")
			afterPets, _, _ := repos.Pet.Load(ctx, "19")
			if !reflect.DeepEqual(worn, afterWorn) || !reflect.DeepEqual(pets, afterPets) {
				t.Fatal("creation projection changed repository state")
			}
		})
	}
}
