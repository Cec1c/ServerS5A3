package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

func TestP144FlagUpgradeSupportedLevelBoundary(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	def, err := items.ResolveItem(100380051)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []byte{9, 10, 19, 20} {
		t.Run(fmt.Sprintf("old_level_%d", level), func(t *testing.T) {
			inv := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
			if _, err := grantCurrentGuildMedalPageItem(&inv, def, currentPVFItemTargetContainer(def), 1); err != nil {
				t.Fatal(err)
			}
			medal := inv.Slots["38:0"]
			medal.RawEntry[10] = level
			inv.Slots["38:0"] = medal
			inv.Slots["0:177"] = dnfrepo.ItemStack{ItemID: 10154439, Count: 10000}
			repos := mustCurrentNPCShopRepositories(t, 0, inv.Slots)
			s := currentNPCShopTestService(repos, shop, items)
			s.initialEquipmentArchive = a
			ctx := context.Background()
			character, _, _ := repos.Character.Load(ctx, "19")
			before, _, _ := repos.Inventory.Load(ctx, "19")
			got, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 80, Body: p144FlagUpgradeBody(), AccountID: character.AccountID, SelectedCharacterID: 19, Repositories: repos, GuildMedalUpgradeResolver: s.currentGuildMedalUpgradeResolver()})
			if err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Inventory.Load(ctx, "19")
			afterChar, _, _ := repos.Character.Load(ctx, "19")
			if !reflect.DeepEqual(character, afterChar) {
				t.Fatal("character/wallet changed")
			}
			if level == 9 {
				ack := got.UpperResponses[0].Body
				if ack[0] != 1 || ack[11] != 9 || !((ack[12] == 0 && ack[13] == 10) || (ack[12] == 2 && ack[13] == 6)) || after.Slots["0:177"].Count != 9817 {
					t.Fatalf("+9 boundary failed: ack=%x reason=%s", ack, got.Reason)
				}
			} else if len(got.UpperResponses) != 1 || got.UpperResponses[0].Body[0] != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("unverified row enabled: %s", got.Reason)
			}
			t.Logf("level=%d material_have=10000 reason=%s", level, got.Reason)
		})
	}
}
