package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Shortcut selection reuses the original op256 body. Repeated clicks on a
// stack represent individual inputs, not distinct inventory slots or stacks.
func TestCurrentEmblemShortcutSelectionConsumesExactUnitsAndRejectsReplay(t *testing.T) {
	for _, tc := range []struct{ grade, count int }{{2, 2}, {2, 5}, {4, 2}} {
		t.Run(fmt.Sprintf("grade%d_count%d", tc.grade, tc.count), func(t *testing.T) {
			repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
				"0:289": {ItemID: 8289, Count: int64(tc.count)},
				"0:290": {ItemID: 8289, Count: 7}, // Not selected.
				"0:291": {ItemID: 8289, Count: 3, Extra: map[string]string{"equipment_lock_state": "1"}},
			})
			source := catalog.source.(bridgePVFSource)
			source["stackable/stackable.lst"] += "8289 `shortcut_emblem.stk`\n"
			source["stackable/shortcut_emblem.stk"] = fmt.Sprintf("[stackable type] `[avatar emblem]`\n[grade] %d\n[stack limit] 999\n", tc.grade)
			source["etc/emblemcompound.etc"] = "[emblem compound info]\n2 5 9000 9000 9000 9000\n4 2 9000\n[/emblem compound info]\n"
			catalog, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			service := currentDisjointTestService(repositories, catalog)
			inputs := make([]currentEmblemCompoundInput, tc.count)
			for i := range inputs {
				inputs[i] = currentEmblemCompoundInput{ItemID: 8289, Slot: 289}
			}
			request, err := parseCurrentEmblemCompoundRequest(currentEmblemCompoundTestBody(inputs))
			if err != nil {
				t.Fatal(err)
			}
			session := &gameSession{selectedCharacterID: 19}
			if _, err := service.commitCurrentEmblemCompound(context.Background(), session, catalog, request); err != nil {
				t.Fatal(err)
			}
			if _, err := service.commitCurrentEmblemCompound(context.Background(), session, catalog, request); err == nil {
				t.Fatal("replayed selection granted twice")
			}
			inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("inventory found=%t err=%v", found, err)
			}
			assertCurrentDisjointInventoryAmount(t, inventory, 8289, 10)
			assertCurrentDisjointInventoryAmount(t, inventory, 9001, 1)
			if inventory.Slots["0:290"].Count != 7 || inventory.Slots["0:291"].Count != 3 || inventory.Slots["0:291"].Extra["equipment_lock_state"] != "1" {
				t.Fatal("unselected/locked source changed")
			}
		})
	}
}

func TestRealPVFEmblemShortcutLimits(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentDisjointPVFConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	for grade := 1; grade <= 4; grade++ {
		for count := 2; count <= 5; count++ {
			_, found := config.EmblemBoosters[[2]int{grade, count}]
			if want := grade != 4 || count == 2; found != want {
				t.Fatalf("grade=%d count=%d recipe=%t want=%t", grade, count, found, want)
			}
		}
	}
}
