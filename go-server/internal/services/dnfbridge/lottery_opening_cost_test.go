package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFHeroLotteryJarsUseOpeningPrice(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("requires matching real PVF")
	}
	archive, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{8219, 8220} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			definition, err := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestRandom)
			if err != nil {
				t.Fatal(err)
			}
			if definition.LotteryGoldCost != 1000000 || definition.Random.MaterialItemID != 0 {
				t.Fatalf("opening cost=%d material=%d", definition.LotteryGoldCost, definition.Random.MaterialItemID)
			}
			for _, group := range definition.Random.Groups {
				for _, reward := range group.Entries {
					item, err := catalog.ResolveItem(uint32(reward.ItemID))
					if err != nil || item.Kind != dungeonDropItemEquipment || reward.Count != 1 {
						t.Fatalf("invalid real jar reward=%+v err=%v", reward, err)
					}
				}
			}
			repos := memory.NewMemoryGroup()
			ctx := context.Background()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 1000007, "fatigue": 120}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:75": {ItemID: int64(id), Count: 2}}}); err != nil {
				t.Fatal(err)
			}
			s := currentBoosterTestService(catalog, repos)
			result, err := s.commitCurrentBooster(ctx, &gameSession{selectedCharacterID: 19}, catalog, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75})
			if err != nil {
				t.Fatal(err)
			}
			if result.SourceRemaining != 1 || len(result.Rewards) != 1 || result.Rewards[0].Count != 1 {
				t.Fatalf("result=%+v", result)
			}
			character, _, err := repos.Character.Load(ctx, "19")
			if err != nil || character.Stats["gold"] != 7 || character.Stats["fatigue"] != 120 {
				t.Fatalf("opening price not charged atomically: stats=%v err=%v", character.Stats, err)
			}
		})
	}
}

func lotteryPriceFixture(t *testing.T, price string) *pvfDungeonDropCatalog {
	t.Helper()
	_, source := newMagicBoxTestCatalog(t)
	source["stackable/stackable.lst"] += "900 `cash/hero.stk`\n"
	source["stackable/cash/hero.stk"] = "[stackable type] `[upgradable legacy]` 1\n[stack limit] 100\n[need material] 3314 1\n[lottery use cost] " + price + "\n[int data] 2600014 100 1\n[/int data]\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func lotteryPriceAssets(t *testing.T, gold int64) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": gold, "fatigue": 120}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:75": {ItemID: 900, Count: 2}, "0:121": {ItemID: 3314, Count: 4},
	}}); err != nil {
		t.Fatal(err)
	}
	return repos
}

func TestLotteryOpeningPriceRejectsMalformedPrices(t *testing.T) {
	for _, price := range []string{"", "-1", "1.5", "`1000000`", "one", "100 200", "2147483648", "100\n[lottery use cost] 100"} {
		t.Run(price, func(t *testing.T) {
			catalog := lotteryPriceFixture(t, price)
			if _, err := resolveCurrentBoosterDefinition(catalog, 900, currentBoosterRequestRandom); err == nil {
				t.Fatal("malformed price accepted")
			}
			if _, err := resolveCurrentMagicBox(catalog, catalog.source, 900); err == nil {
				t.Fatal("magic box ignored malformed price")
			}
		})
	}
}

func TestLotteryOpeningPriceKeepsExplicitMaterials(t *testing.T) {
	for _, tc := range []struct {
		extra string
		want  int64
	}{{"", 0}, {"[lottery use need item] 10007367 2\n", 2}, {"[sealing removal item] 10007367 3\n", 3}} {
		catalog := lotteryPriceFixture(t, "0\n"+tc.extra)
		definition, err := resolveCurrentBoosterDefinition(catalog, 900, currentBoosterRequestRandom)
		if err != nil || definition.Random.MaterialCountPerUse != tc.want {
			t.Fatalf("definition=%+v err=%v", definition, err)
		}
	}
}

func TestLotteryOpeningPriceCannotUseInventoryOnlyMagicBoxOwner(t *testing.T) {
	for _, price := range []string{"1000000", "0"} {
		catalog := lotteryPriceFixture(t, price)
		_, err := resolveCurrentInventoryMagicBox(catalog, catalog.source, 900)
		if (err != nil) != (price == "1000000") {
			t.Fatalf("price=%s err=%v", price, err)
		}
	}
}

func TestLotteryOpeningPriceFailurePreservesAllAssets(t *testing.T) {
	for _, mode := range []string{"poor", "foreign-owner", "full-bag", "late-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			catalog := lotteryPriceFixture(t, "1000000")
			gold := int64(2000007)
			if mode == "poor" {
				gold = 999999
			}
			repos := lotteryPriceAssets(t, gold)
			if mode == "full-bag" {
				inv, _, _ := repos.Inventory.Load(ctx, "19")
				for slot := 3; slot <= 248; slot++ {
					if slot != 75 {
						inv.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 2002, Count: 1}
					}
				}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "late-failure" {
				repos.AccountAssets = lotteryFailLateAssets{repos.AccountAssets}
			}
			s := currentBoosterTestService(catalog, repos)
			if mode == "foreign-owner" {
				s.options.accountID = "another-account"
			}
			beforeChar, _, _ := repos.Character.Load(ctx, "19")
			beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
			beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			definition, err := resolveCurrentBoosterDefinition(catalog, 900, currentBoosterRequestRandom)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.commitCurrentBooster(ctx, &gameSession{selectedCharacterID: 19}, catalog, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75})
			if err == nil {
				t.Fatal("invalid/faulted open succeeded")
			}
			afterChar, _, _ := repos.Character.Load(ctx, "19")
			afterInv, _, _ := repos.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
				t.Fatal("failed opening changed assets")
			}
		})
	}
}

type lotteryFailLateAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
}

func (u lotteryFailLateAssets) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		if err := apply(a, c, i, e); err != nil {
			return err
		}
		return fmt.Errorf("injected failure after lottery asset saves")
	})
}

func TestLotteryOpeningPriceReplyRefreshAndDuplicateConfirm(t *testing.T) {
	for _, mode := range []string{"legacy-lottery", "official-lottery", "booster"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			catalog := lotteryPriceFixture(t, "1000000")
			repos := lotteryPriceAssets(t, 2000007)
			s := currentBoosterTestService(catalog, repos)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			if mode == "official-lottery" {
				s.options.currentClientVersion = "official-24.9"
			}
			if mode == "booster" {
				if err := s.handleCurrentBoosterItem(session, []byte{75, 0}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.handleCurrentLotteryItem(session, []byte{0, 0, 75, 0}); err != nil {
					t.Fatal(err)
				}
				if mode == "legacy-lottery" {
					before, _, _ := repos.Character.Load(ctx, "19")
					if before.Stats["gold"] != 2000007 {
						t.Fatal("animation charged gold")
					}
					conn.write.Reset()
					if err := s.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
						t.Fatal(err)
					}
				}
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Body[0] != 1 {
				t.Fatalf("open rejected: %x", ack.Body)
			}
			if mode == "official-lottery" && (ack.Header.MsgID != 27 || len(ack.Body) != 126) {
				t.Fatalf("native result header=%+v len=%d", ack.Header, len(ack.Body))
			}
			_, rest = splitGameServerUpperPacket(t, rest) // source slot
			list, rest := splitCurrentSceneItemListPacket(t, rest)
			wallet := currentGoldWalletItemListEntry(1000007)
			if len(rest) != 0 || !bytes.Contains(list.Body, wallet.data[:]) {
				t.Fatal("committed wallet missing from container refresh")
			}
			character, _, _ := repos.Character.Load(ctx, "19")
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			if character.Stats["gold"] != 1000007 || character.Stats["fatigue"] != 120 || inventory.Slots["0:75"].Count != 1 || inventory.Slots["0:121"].Count != 4 {
				t.Fatal("wrong fee, source or acquisition-material mutation")
			}
			if mode != "booster" {
				conn.write.Reset()
				if err := s.handleCurrentLotteryOverflowConfirm(session, []byte{1, 27, 0}); err != nil {
					t.Fatal(err)
				}
				afterChar, _, _ := repos.Character.Load(ctx, "19")
				afterInv, _, _ := repos.Inventory.Load(ctx, "19")
				if conn.write.Len() != 0 || !reflect.DeepEqual(character, afterChar) || !reflect.DeepEqual(inventory, afterInv) {
					t.Fatal("repeated confirmation charged or granted twice")
				}
			}
		})
	}
}
