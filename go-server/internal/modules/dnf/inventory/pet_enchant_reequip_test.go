package inventory

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Enchant in the pet bag after unequipping, then equip through the real move
// owner. The typed pet metadata and generic item aliases can agree on B while
// the pet-specific alias still says A; equip deliberately prefers that alias.
func TestOwnerEnchantPetBagReplacementSurvivesReequip(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	savePetEnchantFixture(t, ctx, repos, 2)
	bag, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	stack := bag.Slots["7:24"]
	for _, key := range []string{"value_a", "enchant_card_id", "pet_enchant_card_item_id"} {
		stack.Extra[key] = "10008696"
	}
	for _, key := range []string{"byte_12", "enchant_upgrade_count", "pet_enchant_upgrade_count"} {
		stack.Extra[key] = "2"
	}
	binary.LittleEndian.PutUint32(stack.RawEntry[14:18], 10008696)
	stack.RawEntry[18] = 2
	stack.Extra["raw_entry_hex"] = hex.EncodeToString(stack.RawEntry)
	bag.Slots["7:24"] = stack
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}}); err != nil {
		t.Fatal(err)
	}
	pets, _, err := repos.Pet.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	pet := pets.Entries["37"]
	pet.RawEntry = nil
	pet.Extra = map[string]string{"enchant_card_id": "10008696", "pet_enchant_card_item_id": "10008696", "value_a": "10008696", "enchant_upgrade_count": "2", "pet_enchant_upgrade_count": "2"}
	pets.Entries["37"] = pet
	if err := repos.Pet.Save(ctx, pets); err != nil {
		t.Fatal(err)
	}
	move, err := equip.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	equipCommand := equip.MoveCommand{SelectedCharacterID: 77, SourceListType: 7, SourceSlotIndex: 24,
		DestinationListType: 3, DestinationSlotIndex: 26, MoveCount: 1}
	unequipCommand := equip.MoveCommand{SelectedCharacterID: 77, SourceListType: 3, SourceSlotIndex: 26,
		DestinationListType: 7, DestinationSlotIndex: 24, MoveCount: 1}
	if _, err := move.Move(ctx, equipCommand); err != nil {
		t.Fatal(err)
	}
	for _, card := range []int64{10008730, 10008696} {
		if _, err := move.Move(ctx, unequipCommand); err != nil {
			t.Fatal(err)
		}
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		cmd := NewEnchantCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77}, EnchantByBeadRequest{
			BeadListType: 0, BeadSlotIndex: 12, TargetListType: 7, TargetSlotIndex: 24})
		result, err := owner.Enchant(ctx, cmd, creatureEnchantResolver(card, 400990168))
		if err != nil || !result.Success {
			t.Fatalf("replacement %d: result=%+v err=%v", card, result, err)
		}
		if _, err := move.Move(ctx, equipCommand); err != nil {
			t.Fatal(err)
		}
		worn, _, err := repos.Equipment.Load(ctx, "77")
		if err != nil {
			t.Fatal(err)
		}
		entry := worn.Entries["26"]
		if actual := binary.LittleEndian.Uint32(entry.RawEntry[16:20]); actual != uint32(card) {
			t.Fatalf("reequipped card=%d, want latest %d (old pet-specific alias survived replacement)", actual, card)
		}
		if entry.RawEntry[20] != 0 {
			t.Fatalf("reequipped upgrade=%d, want latest 0", entry.RawEntry[20])
		}
		want := strconv.FormatInt(card, 10)
		assertPetEnchantAliases(t, "replacement bag response", result.TargetUpdatedStack.Extra, want, "0")
		if result.TargetUpdatedStack.Extra["raw_entry_hex"] != hex.EncodeToString(result.TargetUpdatedStack.RawEntry) {
			t.Fatal("bag response raw cache is stale")
		}
		pets, _, err = repos.Pet.Load(ctx, "77")
		if err != nil {
			t.Fatal(err)
		}
		assertPetEnchantAliases(t, "typed pet after re-equip", pets.Entries["37"].Extra, want, "0")
		if len(pets.Entries["37"].RawEntry) != 0 || pets.EquippedKey != "37" {
			t.Fatal("typed pet raw/location changed unexpectedly")
		}
	}
	bag, _, err = repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := bag.Slots["0:12"]; exists {
		t.Fatal("two successful replacements must consume both beads")
	}
}
