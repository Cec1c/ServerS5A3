package equip

import (
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

// Character-owned legal op19 swaps two distinct items of the same template.
// Quality values distinguish the fixture only and are never used as UID.
func TestInvestigationBuffBindingFollowsActualSameTemplateSwap(t *testing.T) {
	ctx, repos, owner := duplicateIdentityFixture(t, "6", []repo.EquipmentEntry{duplicateIdentityWornEntry(12, duplicateIdentityWeaponItemID)}, map[int16]int64{10: duplicateIdentityWeaponItemID})
	saveTestCharacter(t, ctx, repos, repo.CharacterRecord{CharacterID: "6", AccountID: "account-6"})
	inv := loadTestInventory(t, ctx, repos, "6")
	a := inv.Slots["0:10"]
	binary.LittleEndian.PutUint32(a.RawEntry[6:10], 1234)
	a.Extra["quality_seed"] = "1234"
	inv.Slots["0:10"] = a
	saveTestInventory(t, ctx, repos, inv)
	if err := buffswap.Apply(ctx, repos, "6", "account-6", buffswap.Command{Skill: 38, Slot: 12, List: 0, Index: 10}); err != nil {
		t.Fatal(err)
	}
	result, err := owner.Move(ctx, MoveCommand{SelectedCharacterID: 6, SourceListType: 0, SourceSlotIndex: 10, SourceInstanceValue: int32(duplicateIdentityWeaponItemID), MoveCount: 1, DestinationListType: 3, DestinationSlotIndex: 12, DestinationInstanceValue: int32(duplicateIdentityWeaponItemID)})
	if err != nil || !result.Changed || result.Mode != "equip_swap" {
		t.Fatalf("legal swap failed: %+v %v", result, err)
	}
	worn := loadTestEquipment(t, ctx, repos, "6").Entries["12"]
	if binary.LittleEndian.Uint32(worn.RawEntry[6:10]) != 1234 {
		t.Fatal("registered A did not move to worn12")
	}
	state, _, err := buffswap.Load(ctx, repos, "6", "account-6")
	if err != nil {
		t.Fatal(err)
	}
	resolved, omitted, err := buffswap.Resolve(ctx, repos, "6", state)
	if err != nil || omitted != 0 || resolved.Slots[12].List != 3 || resolved.Slots[12].Index != 12 {
		t.Fatalf("registration followed replacement B instead of moved A: stored=%+v resolved=%+v omitted=%d err=%v", state, resolved, omitted, err)
	}
}
