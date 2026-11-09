package dnfbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestFateShopExplicitOpeningFeesPreserved(t *testing.T) {
	for _, tc := range []struct {
		id                int64
		extra             string
		wantID, wantCount int64
	}{
		{10157775, "", 0, 0},
		{10157776, "", 0, 0},
		{10159652, "", 0, 0},
		{10159653, "", 0, 0},

		{10157775, "[lottery use need item]\n10007367 2\n", 10007367, 2},
		{10157775, "[sealing removal item]\n10007367 3\n", 10007367, 3},
		{10159653, "[lottery use need item]\n10007367 2\n", 10007367, 2},
		{10159653, "[sealing removal item]\n10007367 3\n", 10007367, 3},
		{900, "", 10095702, 4},
	} {
		doc, err := dnfpvf.Parse("stackable/test.stk", "[stackable type]\n`[upgradable legacy]` 1\n[need material]\n10095702 4\n"+tc.extra)
		if err != nil {
			t.Fatal(err)
		}
		id, count := resolveMagicBoxNeedMaterial(doc, tc.id)
		if id != tc.wantID || count != tc.wantCount {
			t.Fatalf("item=%d extra=%q got=%d/%d want=%d/%d", tc.id, tc.extra, id, count, tc.wantID, tc.wantCount)
		}
	}
}

func TestFateShopPurchasedJarsOpenWithoutSecondPayment(t *testing.T) {
	for _, id := range []int64{10157775, 10157776, 10159652, 10159653} {
		for _, full := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/full_%t", id, full), func(t *testing.T) {
				source := dungeonDropCatalogTestSource{
					"monster/monster.lst": "", "equipment/equipment.lst": "700 `weapon/reward.equ`\n",
					"equipment/weapon/reward.equ": "[equipment type]\n`[weapon]` 0\n[attach type]\n`[trade]`\n",
					"stackable/stackable.lst":     fmt.Sprintf("%d `jar.stk`\n10304529 `fate.stk`\n", id),
					"stackable/jar.stk":           "[stackable type]\n`[upgradable legacy]` 1\n[need material]\n10304529 500\n[/need material]\n[int data]\n700 100 1\n[/int data]\n",
					"stackable/fate.stk":          "[stackable type]\n`[material]` 0\n[stack limit]\n1000\n",
				}
				catalog, err := newPVFDungeonDropCatalog(source)
				if err != nil {
					t.Fatal(err)
				}
				slots := map[string]repo.ItemStack{"0:75": {ItemID: id, Count: 2}}
				if full {
					for n := 3; n <= 64; n++ {
						slots[fmt.Sprintf("0:%d", n)] = repo.ItemStack{ItemID: 700, Count: 1}
					}
				}
				f := newBoosterFateFixture(t, repo.AccountRecord{}, slots)
				f.service.pvfItemCatalog = catalog
				before, _, _ := f.group.Inventory.Load(context.Background(), "19")
				if err := f.service.handleCurrentLotteryItem(f.session, []byte{0, 0, 75, 0}); err != nil {
					t.Fatal(err)
				}
				packets := drainUpperPackets(t, f.conn)
				after, _, _ := f.group.Inventory.Load(context.Background(), "19")
				if len(packets) == 0 || packets[0].msgID != 27 {
					t.Fatal("missing lottery response")
				}
				if full {
					if packets[0].body[0] != 0 || !reflect.DeepEqual(before, after) {
						t.Fatal("full bag consumed jar")
					}
					return
				}
				if packets[0].body[0] != 1 || after.Slots["0:75"].Count != 1 {
					t.Fatal("purchased jar could not open without Fate in main bag")
				}
				n := int64(0)
				for _, stack := range after.Slots {
					if stack.ItemID == 700 {
						n += stack.Count
					}
				}
				if n != 1 {
					t.Fatalf("reward count=%d", n)
				}
				if err := f.service.handleCurrentLotteryOverflowConfirm(f.session, []byte{1, 27, 0}); err != nil {
					t.Fatal(err)
				}
				replay, _, _ := f.group.Inventory.Load(context.Background(), "19")
				if !reflect.DeepEqual(after, replay) {
					t.Fatal("replayed confirmation grants twice")
				}
			})
		}
	}
}

func TestFateShopCurrentPVFRewardPools(t *testing.T) {
	data, err := os.ReadFile("testdata/fate_shop_current_pvf.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PVFSHA256 string                       `json:"pvfSHA256"`
		Source    dungeonDropCatalogTestSource `json:"source"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.PVFSHA256 != "0bcf5c36966c93d7f515b0193eb506377fcea46ad7d3d59370216f5325b1f374" {
		t.Fatal("fixture provenance changed")
	}
	catalog, err := newPVFDungeonDropCatalog(fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{10157775, 10157776, 10159652} {
		jar, err := resolveCurrentBoosterDefinition(catalog, uint32(id), currentBoosterRequestRandom)
		if err != nil || jar.Random.MaterialCountPerUse != 0 || len(jar.Random.Groups) != 1 {
			t.Fatalf("jar %d: %+v %v", id, jar, err)
		}
		for _, row := range jar.Random.Groups[0].Entries {
			if _, err := resolveCurrentMagicBoxRewardItem(catalog, fixture.Source, row.ItemID); err != nil {
				t.Fatalf("jar %d reward %d: %v", id, row.ItemID, err)
			}
		}
		f := newBoosterFateFixture(t, repo.AccountRecord{}, map[string]repo.ItemStack{"0:75": {ItemID: id, Count: 1}})
		f.service.pvfItemCatalog = catalog
		if err := f.service.handleCurrentLotteryItem(f.session, []byte{0, 0, 75, 0}); err != nil {
			t.Fatal(err)
		}
		packets := drainUpperPackets(t, f.conn)
		if len(packets) < 2 || packets[0].msgID != 27 || packets[0].body[0] != 1 {
			t.Fatalf("current PVF jar %d failed", id)
		}
		after, _, _ := f.group.Inventory.Load(context.Background(), "19")
		n := int64(0)
		for _, stack := range after.Slots {
			if stack.ItemID == id {
				t.Fatal("jar retained")
			}
			n += stack.Count
		}
		if n != 1 {
			t.Fatalf("jar %d reward count=%d", id, n)
		}
	}
}
