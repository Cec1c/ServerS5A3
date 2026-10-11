package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

func TestBoosterLastBoxReplacedByOrbRemovesNativeSourceBeforeReward(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "500 `box.stk`\n600 `orb.stk`\n",
		"stackable/box.stk":       "[stackable type]\n`[booster]` 0\n[stack limit]\n100\n[booster info]\n[etc]\n1 600 10000 1\n[/etc]\n[/booster info]\n",
		"stackable/orb.stk":       "[stackable type]\n`[enchant waste]` 0\n[stack limit]\n100\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int64{1, 2} {
		repositories := dnfrepomemory.NewMemoryGroup()
		ctx := context.Background()
		if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 500, Count: count}}}); err != nil {
			t.Fatal(err)
		}
		connection := &bufferConn{}
		service := currentBoosterTestService(catalog, repositories)
		if err := service.handleCurrentBoosterItem(&gameSession{conn: connection, selectedCharacterID: 19}, []byte{65, 0}); err != nil {
			t.Fatal(err)
		}
		packets := splitItemGrantNoticeStream(t, service, connection.write.Bytes())
		if count == 1 {
			if len(packets) != 3 || packets[0].msgID != uint16(dnfenum.CmdPacketUseBoosterItem) {
				t.Fatalf("last box packets=%+v", packets)
			}
			requireItemGrantNoticeRow(t, itemGrantNoticeUpdateRows(t, packets[1:2]), 0, 65, 0xffffffff, 0)
			requireItemGrantNoticeRow(t, itemGrantNoticeUpdateRows(t, packets[2:]), 0, 65, 600, 1)
		} else {
			if len(packets) != 2 {
				t.Fatalf("remaining box packets=%+v", packets)
			}
			rows := itemGrantNoticeUpdateRows(t, packets[1:])
			requireItemGrantNoticeRow(t, rows, 0, 65, 500, 1)
			requireItemGrantNoticeRow(t, rows, 0, 66, 600, 1)
		}
		inventory, found, err := repositories.Inventory.Load(ctx, "19")
		if err != nil || !found {
			t.Fatalf("inventory found=%t err=%v", found, err)
		}
		var boxes, orbs int64
		for _, stack := range inventory.Slots {
			if stack.ItemID == 500 {
				boxes += stack.Count
			}
			if stack.ItemID == 600 {
				orbs += stack.Count
			}
		}
		if boxes != count-1 || orbs != 1 {
			t.Fatalf("boxes=%d orbs=%d inventory=%+v", boxes, orbs, inventory)
		}
	}
}
