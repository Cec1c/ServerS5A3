package dnfbridge

import "testing"

func TestMailboxItemResolutionUsesStackableTypeAsWireType(t *testing.T) {
	got, err := mailboxItemResolutionFromPVF(dungeonDropItemDefinition{
		ItemID: 25, Kind: dungeonDropItemStackable, StackableType: "[creature]",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != string(dungeonDropItemStackable) || got.EquipmentType != "[creature]" {
		t.Fatalf("resolution kind/type = %q/%q", got.Kind, got.EquipmentType)
	}
}

func TestMailboxItemResolutionLeavesOtherStackableTypesOutOfEquipmentType(t *testing.T) {
	got, err := mailboxItemResolutionFromPVF(dungeonDropItemDefinition{
		ItemID: 3166, Kind: dungeonDropItemStackable, StackableType: "[material]",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.EquipmentType != "" {
		t.Fatalf("non-creature stackable polluted equipment type: %q", got.EquipmentType)
	}
}
