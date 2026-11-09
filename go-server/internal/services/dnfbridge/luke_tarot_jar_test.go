package dnfbridge

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const lukeTarotJarItemID = 10159653

func lukeTarotJarFixture(t *testing.T) dungeonDropCatalogTestSource {
	t.Helper()
	data, err := os.ReadFile("testdata/luke_tarot_jar_current_pvf.json")
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
	if fixture.PVFSHA256 != "d3507920cdb8038b900ab9897fdc181ff97840c775a06d4c2108b656cc952c56" {
		t.Fatal("fixture provenance changed")
	}
	return fixture.Source
}

func TestLukeTarotJarEveryRewardAndSourceDelta(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(lukeTarotJarFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentBoosterDefinition(catalog, lukeTarotJarItemID, currentBoosterRequestRandom)
	if err != nil || len(definition.Random.Groups) != 1 {
		t.Fatalf("jar definition=%+v err=%v", definition, err)
	}
	group := definition.Random.Groups[0]
	if group.DrawCount != 1 || len(group.Entries) != 22 || definition.LotteryGoldCost != 0 {
		t.Fatalf("jar group=%+v gold=%d", group, definition.LotteryGoldCost)
	}
	for index, entry := range group.Entries {
		if entry.ItemID != int64(10159697+index) || entry.Weight != 10000 || entry.Count != 1 {
			t.Fatalf("row %d=%+v", index, entry)
		}
		// Both ends of every interval exercise the actual op27 router and grant.
		for _, roll := range []int64{int64(index) * 10000, int64(index+1)*10000 - 1} {
			t.Run(fmt.Sprintf("%d/roll_%d", entry.ItemID, roll), func(t *testing.T) {
				f := newBoosterFateFixture(t, repo.AccountRecord{}, map[string]repo.ItemStack{"0:75": {ItemID: lukeTarotJarItemID, Count: 2}})
				f.service.pvfItemCatalog = catalog
				beforeAccount, _, _ := f.group.Account.Load(context.Background(), "account-1")
				beforeCharacter, _, _ := f.group.Character.Load(context.Background(), "19")
				func() {
					previous := cryptorand.Reader
					defer func() { cryptorand.Reader = previous }()
					cryptorand.Reader = &monsterTarotFixedRandReader{value: uint64(roll)}
					if err := f.service.handleGameUpper(f.session, monsterTarotRouteOp27Frame(t, 0, 75, 1)); err != nil {
						t.Fatal(err)
					}
				}()
				packets := drainUpperPackets(t, f.conn)
				if len(packets) < 2 || packets[0].msgID != 27 || len(packets[0].body) != 126 || packets[0].body[0] != 1 {
					t.Fatalf("op27 rejected purchased jar: packets=%+v material=%d/%d", packets, definition.Random.MaterialItemID, definition.Random.MaterialCountPerUse)
				}
				ack := packets[0].body
				if binary.LittleEndian.Uint16(ack[1:3]) != 75 || binary.LittleEndian.Uint32(ack[5:9]) != uint32(entry.ItemID) || binary.LittleEndian.Uint32(ack[9:13]) != 1 {
					t.Fatalf("wrong source/reward ACK: %x", ack)
				}
				after, _, _ := f.group.Inventory.Load(context.Background(), "19")
				if after.Slots["0:75"].Count != 1 || monsterTarotInventoryCount(after, entry.ItemID) != 1 {
					t.Fatalf("source or reward delta=%+v", after.Slots)
				}
				ackSlot := binary.LittleEndian.Uint16(ack[3:5])
				if landed := after.Slots[fmt.Sprintf("0:%d", ackSlot)]; landed.ItemID != entry.ItemID || landed.Count != 1 {
					t.Fatalf("ACK did not name committed reward slot: %d %+v", ackSlot, landed)
				}
				afterAccount, _, _ := f.group.Account.Load(context.Background(), "account-1")
				afterCharacter, _, _ := f.group.Character.Load(context.Background(), "19")
				if !reflect.DeepEqual(beforeAccount, afterAccount) || !reflect.DeepEqual(beforeCharacter, afterCharacter) {
					t.Fatal("opening charged wallet or changed character")
				}
				if err := f.service.handleCurrentLotteryOverflowConfirm(f.session, []byte{1, 27, 0}); err != nil {
					t.Fatal(err)
				}
				replayed, _, _ := f.group.Inventory.Load(context.Background(), "19")
				if !reflect.DeepEqual(after, replayed) {
					t.Fatal("duplicate confirm granted twice")
				}
			})
		}
	}
}

func TestLukeTarotJarFullConsumableBagPreservesAssets(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(lukeTarotJarFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	slots := map[string]repo.ItemStack{"0:75": {ItemID: lukeTarotJarItemID, Count: 2}}
	for slot := 3; slot <= 120; slot++ {
		if slot > 8 && slot < 65 || slot == 75 {
			continue
		}
		slots[fmt.Sprintf("0:%d", slot)] = repo.ItemStack{ItemID: lukeTarotJarItemID, Count: 1}
	}
	f := newBoosterFateFixture(t, repo.AccountRecord{}, slots)
	f.service.pvfItemCatalog = catalog
	ctx := context.Background()
	before, _, _ := f.group.Inventory.Load(ctx, "19")
	account, _, _ := f.group.Account.Load(ctx, "account-1")
	character, _, _ := f.group.Character.Load(ctx, "19")
	definition, err := f.service.prepareCurrentBooster(ctx, f.session, catalog, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75})
	if err != nil || definition.Random.MaterialCountPerUse != 0 {
		t.Fatalf("full-bag case must reach placement, not a material rejection: %v %+v", err, definition.Random)
	}
	if err := f.service.handleCurrentLotteryItem(f.session, []byte{0, 0, 75, 0}); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, f.conn)
	after, _, _ := f.group.Inventory.Load(ctx, "19")
	afterAccount, _, _ := f.group.Account.Load(ctx, "account-1")
	afterCharacter, _, _ := f.group.Character.Load(ctx, "19")
	if len(packets) != 1 || packets[0].msgID != 27 || packets[0].body[0] != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, afterAccount) || !reflect.DeepEqual(character, afterCharacter) {
		t.Fatal("full bag consumed jar, wallet or character assets")
	}
}

func TestRealPVFLukeTarotJarExchangeThenOpen(t *testing.T) {
	catalog := loadMonsterTarotRealPVFCatalog(t)
	for path, want := range lukeTarotJarFixture(t) {
		if path == "stackable/stackable.lst" || path == "monster/monster.lst" || path == "equipment/equipment.lst" {
			continue
		}
		doc, err := parseDungeonCardPVFDocument(catalog.source, path)
		if err != nil || doc == nil {
			t.Fatalf("missing PVF document %s: %v", path, err)
		}
		actual, err := catalog.source.ReadText(path)
		if err != nil || actual != want {
			t.Fatalf("snapshot differs from actual PVF: %s err=%v", path, err)
		}
	}
	shop := &currentNPCShopCatalog{source: catalog.source, buyableItems: map[uint32]struct{}{lukeTarotJarItemID: {}}}
	pricing, err := resolveCurrentNPCShopPricing(shop, catalog, lukeTarotJarItemID)
	if err != nil || !pricing.MaterialExchange || pricing.NeedMaterialItem != raidcurrency.FateItemID || pricing.NeedMaterialCount != 1000 {
		t.Fatalf("purchase price=%+v err=%v", pricing, err)
	}
	for _, balance := range []uint32{999, 1000} {
		t.Run(fmt.Sprint(balance), func(t *testing.T) {
			account := repo.AccountRecord{AccountID: "account-1"}
			if _, _, err := raidcurrency.Credit(&account, balance, boosterFateTestNow()); err != nil {
				t.Fatal(err)
			}
			f := newBoosterFateFixture(t, account, map[string]repo.ItemStack{})
			f.service.pvfItemCatalog = catalog
			ctx := context.Background()
			character, _, err := f.group.Character.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			character.Stats = map[string]int64{"gold": 7, "fatigue": 120}
			if err := f.group.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			before, _, _ := f.group.Inventory.Load(ctx, "19")
			purchase, err := f.service.commitCurrentNPCShopMaterialExchange(ctx, f.session, pricing, currentNPCShopBuyRequest{ItemID: lukeTarotJarItemID, Count: 1})
			if balance == 999 {
				after, _, _ := f.group.Inventory.Load(ctx, "19")
				afterAccount, _, _ := f.group.Account.Load(ctx, "account-1")
				if !errors.Is(err, errCurrentNPCShopMaterialInsufficient) || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, afterAccount) {
					t.Fatal("insufficient purchase changed assets")
				}
				return
			}
			if err != nil || purchase.CostItemNewCount != 0 {
				t.Fatalf("purchase=%+v err=%v", purchase, err)
			}
			afterBuy, _, _ := f.group.Account.Load(ctx, "account-1")
			body := make([]byte, 4)
			binary.LittleEndian.PutUint16(body[2:], uint16(purchase.Slot))
			if err := f.service.handleCurrentLotteryItem(f.session, body); err != nil {
				t.Fatal(err)
			}
			packets := drainUpperPackets(t, f.conn)
			after, _, _ := f.group.Inventory.Load(ctx, "19")
			afterOpen, _, _ := f.group.Account.Load(ctx, "account-1")
			if len(packets) < 2 || packets[0].msgID != 27 || packets[0].body[0] != 1 || !reflect.DeepEqual(afterBuy, afterOpen) || monsterTarotInventoryCount(after, lukeTarotJarItemID) != 0 {
				t.Fatalf("purchased jar failed to open at zero Fate balance: packets=%+v inventory=%+v", packets, after.Slots)
			}
			var cards int64
			for id := int64(10159697); id <= 10159718; id++ {
				cards += monsterTarotInventoryCount(after, id)
			}
			if cards != 1 {
				t.Fatalf("granted cards=%d want 1", cards)
			}
		})
	}
}
