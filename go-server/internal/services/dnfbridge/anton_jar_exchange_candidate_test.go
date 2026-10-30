package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestAntonJarRealPVFExchangeThenOpen(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching current PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	shop := &currentNPCShopCatalog{source: a, buyableItems: map[uint32]struct{}{10094738: {}}, equipmentSellRatePermille: 1}
	pricing, err := resolveCurrentNPCShopPricing(shop, catalog, 10094738)
	if err != nil || !pricing.MaterialExchange || pricing.NeedMaterialItem != 10095702 || pricing.NeedMaterialCount != 4 {
		t.Fatalf("exchange price=%+v err=%v", pricing, err)
	}
	jar, err := resolveCurrentBoosterDefinition(catalog, 10094738, currentBoosterRequestRandom)
	if err != nil || jar.Random.MaterialCountPerUse != 0 {
		t.Fatalf("open definition=%+v err=%v", jar, err)
	}
	pool := map[uint32]bool{}
	for _, entry := range jar.Random.Groups[0].Entries {
		pool[uint32(entry.ItemID)] = true
	}
	if len(pool) != 47 {
		t.Fatalf("reward pool count %d", len(pool))
	}
	for _, initialFragments := range []int64{4, 8} {
		t.Run(fmt.Sprintf("fragments_%d", initialFragments), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]repo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 1000}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:121": {ItemID: 10095702, Count: initialFragments}}}); err != nil {
				t.Fatal(err)
			}
			s := currentBoosterTestService(catalog, repos)
			s.options.currentClientVersion = "official-24.9"
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			purchase, err := s.commitCurrentNPCShopMaterialExchange(ctx, session, pricing, currentNPCShopBuyRequest{ItemID: 10094738, Count: 1})
			if err != nil {
				t.Fatalf("exchange: %v", err)
			}
			afterBuy, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if afterBuy.Slots["0:121"].Count != initialFragments-4 {
				t.Fatalf("exchange did not charge exactly four fragments")
			}
			body := make([]byte, 4)
			binary.LittleEndian.PutUint16(body[2:], uint16(purchase.Slot))
			if err := s.handleCurrentLotteryItem(session, body); err != nil {
				t.Fatal(err)
			}
			packets := drainUpperPackets(t, conn)
			if len(packets) < 2 || packets[0].msgID != 27 || len(packets[0].body) != 126 || packets[0].body[0] != 1 {
				t.Fatalf("opening did not return real success result")
			}
			after, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if after.Slots["0:121"].Count != initialFragments-4 {
				t.Fatal("opening charged fragments a second time")
			}
			var rewards int64
			for _, stack := range after.Slots {
				if stack.ItemID == 10094738 {
					t.Fatal("jar not consumed")
				}
				if pool[uint32(stack.ItemID)] {
					rewards += stack.Count
				} else if stack.ItemID != 10095702 {
					t.Fatalf("unexpected item %d", stack.ItemID)
				}
			}
			if rewards != 1 {
				t.Fatalf("rewards=%d, want exactly one", rewards)
			}
			if err := s.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
				t.Fatal(err)
			}
			replayed, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil || !reflect.DeepEqual(after, replayed) {
				t.Fatal("duplicate confirmation changed inventory")
			}
		})
	}
}

func TestAntonJarExplicitOpenMaterialsAndOtherJarRemainEnforced(t *testing.T) {
	for _, tc := range []struct {
		id                int64
		extra             string
		wantID, wantCount int64
	}{
		{10094738, "", 0, 0},
		{10094738, "[lottery use need item]\n10007367 2\n", 10007367, 2},
		{10094738, "[sealing removal item]\n10007367 3\n", 10007367, 3},
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

func TestAntonJarFullEquipmentBagDoesNotConsumeJar(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching current PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	slots := map[string]repo.ItemStack{"0:75": {ItemID: 10094738, Count: 1}}
	// Equipment can also fall back to quick slots 3..8.
	for i := 3; i <= 64; i++ {
		slots[fmt.Sprintf("0:%d", i)] = repo.ItemStack{ItemID: 100050667, Count: 1}
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	s := currentBoosterTestService(catalog, repos)
	s.options.currentClientVersion = "official-24.9"
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	before, _, _ := repos.Inventory.Load(ctx, "19")
	if err := s.handleCurrentLotteryItem(session, []byte{0, 0, 75, 0}); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	after, _, _ := repos.Inventory.Load(ctx, "19")
	if len(packets) != 1 || packets[0].msgID != 27 || packets[0].body[0] != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("full bag must fail without consuming jar: packets=%d unchanged=%t", len(packets), reflect.DeepEqual(before, after))
	}
}
