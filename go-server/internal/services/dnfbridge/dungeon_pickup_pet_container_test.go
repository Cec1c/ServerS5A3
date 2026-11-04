package dnfbridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestGroundPickupPetContainersAndSeal(t *testing.T) {
	for _, family := range []string{"[artifact red]", "[artifact blue]", "[artifact green]", "[creature]"} {
		t.Run(family, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			def := dungeonDropItemDefinition{ItemID: 9001, Kind: dungeonDropItemEquipment, EquipmentType: family, AttachType: "[sealing]", Rarity: 3, SlotStart: 9, SlotEnd: 64, Grade: 50}
			container := currentPVFItemTargetContainer(def)
			if got := currentDungeonPickupListType(def); got != container.ListType {
				t.Fatalf("pickup notification container=%d want=%d", got, container.ListType)
			}
			slot, _, err := grantCurrentDungeonPickupItem(ctx, g, "19", def, 1, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			inv, _, _ := g.Inventory.Load(ctx, "19")
			stack, found := inv.Slots[fmt.Sprintf("%d:%d", container.ListType, slot)]
			if !found || container.ListType != 7 || int16(slot) < container.SlotStart || int16(slot) > container.SlotEnd {
				t.Fatal("pet pickup in wrong container", slot, inv.Slots)
			}
			if container.PetArtifact && (stack.Extra["seal_flag"] != "1" || !currentAuctionSealed(stack)) {
				t.Fatal("sealed pet drop lost seal")
			}
		})
	}
}
