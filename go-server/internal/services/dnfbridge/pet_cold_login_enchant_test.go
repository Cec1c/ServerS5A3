package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPetColdLoginRecoversEnchantFromMatchingDurablePet(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	worn := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
		"26": {SlotIndex: 26, ItemID: 400990167, RawEntry: testCurrentEquippedCreatureEnchantRaw(26, 400990167, 37, 0, 0)},
	}}
	if err := repos.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "19", EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
		"37": {PetKey: "37", CreatureKey: 37, ItemID: 400990167, Level: 7, Extra: map[string]string{"pet_enchant_card_item_id": "10008713", "enchant_upgrade_count": "2"}},
	}}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	if err := service.sendSelectedCreatureInitialStateAfterMode1(session, "cold_login_test"); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	assertEquippedCreatureItemRefreshPacket(t, packet, 400990167, 37, 10008713, 2)
	after, _, err := repos.Equipment.Load(ctx, "19")
	if err != nil || !reflect.DeepEqual(worn, after) {
		t.Fatal("read-only projection changed worn inventory")
	}
}

func TestPetColdLoginEnchantDoesNotCrossCreatureIdentityOrOverwriteWornState(t *testing.T) {
	for _, name := range []string{"other_serial", "other_template", "duplicate_serial", "worn_enchant", "missing_metadata"} {
		t.Run(name, func(t *testing.T) {
			worn := dnfrepo.EquipmentEntry{SlotIndex: 26, ItemID: 400990167, RawEntry: testCurrentEquippedCreatureEnchantRaw(26, 400990167, 37, 0, 0)}
			pet := dnfrepo.PetEntry{PetKey: "37", CreatureKey: 37, ItemID: 400990167, Extra: map[string]string{"pet_enchant_card_item_id": "10008713", "enchant_upgrade_count": "2"}}
			pets := dnfrepo.PetRecord{Entries: map[string]dnfrepo.PetEntry{"37": pet}}
			want := uint32(0)
			switch name {
			case "other_serial":
				pet.CreatureKey, pet.PetKey = 38, "38"
				pets.Entries["37"] = pet
			case "other_template":
				pet.ItemID++
				pets.Entries["37"] = pet
			case "duplicate_serial":
				pets.Entries["duplicate"] = pet
			case "worn_enchant":
				worn.Extra = map[string]string{"pet_enchant_card_item_id": "10008705"}
				want = 10008705
			case "missing_metadata":
				pets.Entries = nil
			}
			before := dnfrepo.EquipmentRecord{Entries: map[string]dnfrepo.EquipmentEntry{"26": worn}}
			original := dnfrepo.CloneEquipment(before)
			projected := currentEquippedCreatureWithDurableEnchant(worn, pets)
			row := currentEquippedCreatureItemRefreshEntry(projected)
			if got := binary.LittleEndian.Uint32(row.data[0x0E:0x12]); got != want {
				t.Fatalf("card=%d want=%d", got, want)
			}
			if !reflect.DeepEqual(before, original) {
				t.Fatal("projection mutated original worn state")
			}
		})
	}
}
