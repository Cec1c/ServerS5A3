package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

func TestEnchanterBeadBatchGrantUpgradeIdentityAndSpill(t *testing.T) {
	definition := dungeonDropItemDefinition{ItemID: 2600313, Kind: dungeonDropItemStackable, StackLimit: 2, SlotStart: 65, SlotEnd: 120, StackableType: "[enchant waste]"}
	inv := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{
		"0:65": {ItemID: 2600313, Count: 1, Extra: map[string]string{"enchant_upgrade_count": "1"}},
		"0:66": {ItemID: 2600313, Count: 1, Extra: map[string]string{"enchant_upgrade_count": "2"}},
		"0:67": {ItemID: 2600313, Count: 1, Extra: map[string]string{"enchant_upgrade_count": "2", "equipment_lock_state": "1"}},
	}}
	old := dnfrepo.CloneInventory(inv)
	if err := grantCurrentExpertJobBeads(&inv, definition, 4, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inv.Slots["0:65"], old.Slots["0:65"]) || !reflect.DeepEqual(inv.Slots["0:67"], old.Slots["0:67"]) || inv.Slots["0:66"].Count != 2 {
		t.Fatal("changed other upgrade/locked bead, or failed compatible partial merge")
	}
	var total int64
	for _, stack := range inv.Slots {
		if currentExpertJobEnchantUpgradeCount(stack) == 2 && !currentNPCShopItemLocked(stack) {
			total += stack.Count
			if stack.Count > 2 {
				t.Fatal("exceeded stack limit")
			}
		}
	}
	if total != 5 {
		t.Fatalf("upgrade2 total=%d", total)
	}
}
