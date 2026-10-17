package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"

	dnfchronicle "longheng.io/server/internal/modules/dnf/chronicle"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestChronicleRealPVFSixAurasAndFemalePriestTarget(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	catalog := items.currentChronicleCatalog()
	equipment, err := catalog.Equipment(100080578)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1254, 1255, 1256, 10090758, 10090759, 10090760} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			m, err := catalog.Material(id)
			if err != nil {
				t.Fatal(err)
			}
			want := 11
			if id > 1256 {
				want = 100
			}
			if m.Probabilities != [2]int{100, want} || m.FragmentID != 3311 || !m.Allows(14, 4, equipment, 8) {
				t.Fatalf("90真实规则%+v", m)
			}
			jobs := map[byte]int{}
			for _, check := range m.Checks {
				jobs[check.Job] += len(check.Skills)
			}
			for _, job := range []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 11, 12, 13, 14, 15} {
				if jobs[job] == 0 {
					t.Fatalf("90真实气息缺职业%d的有效技能表", job)
				}
			}
			s, session, _, g := chronicleFixture(t, chronicleFixtureSource(), id)
			s.pvfItemCatalog = items
			def, err := items.ResolveItem(uint32(id))
			if err != nil {
				t.Fatal(err)
			}
			cmd := s.currentChronicleRefineCommand(session, items, def, dnfchronicle.Request{MaterialSlot: 65, MaterialID: id, TargetSlot: 30, TargetID: 100080578, Option: 8})
			cmd.Random = func(int) int { return 100 }
			owner, _ := dnfchronicle.NewOwner(g)
			r, err := owner.Refine(context.Background(), cmd)
			if err != nil || !r.Succeeded {
				t.Fatalf("真实首次精炼%+v %v", r, err)
			}
			cmd.Request.Option = 9
			r, err = owner.Refine(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			if id > 1256 {
				if !r.Succeeded || r.Options != 2 {
					t.Fatal("神圣气息第二次未按真实100阈值成功")
				}
			} else {
				if r.Succeeded || len(r.Rewards) < 2 {
					t.Fatalf("普通气息未碎装返材：%+v", r)
				}
				inv, _, _ := g.Inventory.Load(context.Background(), "2")
				acc, _, _ := g.AccountInventory.Load(context.Background(), "account-1")
				if _, exists := inv.Slots["0:30"]; exists {
					t.Fatal("真实失败装备仍存在")
				}
				found := false
				for _, row := range r.Rewards {
					if row.ItemID == 3311 && row.Count == 13 {
						found = true
					}
					stack := inv.Slots[currentCeraShopInventorySlotKey(0, row.Slot)]
					if dnfrepo.IsAccountSharedInventorySlot(0, row.Slot) {
						stack = acc.Slots[currentCeraShopInventorySlotKey(0, row.Slot)]
					}
					if stack.ItemID != int64(row.ItemID) || stack.Count < int64(row.Count) {
						t.Fatal("真实奖励与仓储不符")
					}
				}
				if !found {
					t.Fatal("+12装备未返回13碎片")
				}
				t.Logf("实际PVF失败奖励：%+v", r.Rewards)
			}
		})
	}
}
