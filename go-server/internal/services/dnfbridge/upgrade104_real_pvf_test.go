package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestUpgrade104PairedRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired runtime PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a, pvfItemCatalog: c}
	for _, id := range []int64{490001497, 490001533, 490001534, 490002394, 490004594, 490005584, 490006368, 490007876, 490007878, 490008465, 490009333, 490009714, 490009901, 490010213, 490702938, 10004688, 10004745, 10004687, 10004744, 10006268} {
		r, err := resolveCurrentUpgradeRateBuff(c, a, id)
		if err != nil || r.Mode == "" || r.BonusWeight != 10000 || r.DurationMillis <= 0 {
			t.Fatalf("buff %d: %+v err=%v", id, r, err)
		}
	}
	t.Run("town potions commit effect and debit together", func(t *testing.T) {
		for _, id := range []int64{2600025, 2749959, 10007343} {
			d, err := resolveTownPotion002(c, id)
			if err != nil || d == nil {
				t.Fatalf("town potion %d %+v %v", id, d, err)
			}
			g := memory.NewMemoryGroup()
			ctx := context.Background()
			if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "a", Level: 90, Stats: map[string]int64{"gold": 500}}); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:87": {ItemID: id, Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": d.PVFPath, "instance_value": "123"}}}}); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, 15)
			binary.LittleEndian.PutUint16(body, 87)
			binary.LittleEndian.PutUint32(body[3:], 123)
			binary.LittleEndian.PutUint32(body[7:], uint32(id))
			resolver := func(id int64) (alignedcmd.StackableUseProvenanceResolution, error) {
				return resolveCurrentStackableUseProvenance(c, id)
			}
			r, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUseStackable), Body: body, AccountID: "a", SelectedCharacterID: 77, Repositories: g, ItemUsageNow: time.Now().UTC(), StackableUseProvenanceResolver: resolver})
			if err != nil {
				t.Fatal(err)
			}
			iv, _, _ := g.Inventory.Load(ctx, "77")
			ch, _, _ := g.Character.Load(ctx, "77")
			if iv.Slots["0:87"].Count != 1 || ch.Stats["upgrade_rate_buff_reinforce_bonus"] != 10000 || ch.Stats["gold"] != 500 {
				t.Fatalf("town potion %d debit/effect mismatch inv=%+v stats=%+v result=%+v", id, iv, ch.Stats, r)
			}
		}
	})
	t.Run("all probability pets and titles resolve", func(t *testing.T) {
		for _, tc := range []struct {
			id             int64
			slot           int16
			rate, discount int
		}{{400990076, 26, 400, 4}, {400990169, 26, 400, 4}, {400990006, 26, 200, 0}, {400990007, 26, 300, 0}, {400330110, 13, 400, 0}, {26698, 13, 100, 0}, {26699, 13, 100, 0}, {100330566, 26, 0, 5}, {100330496, 13, 0, 3}, {400330028, 13, 0, 10}, {100330617, 13, 0, 5}, {26700, 13, 0, 5}} {
			r, e := s.alignedUpgradeEquippedBonusResolver()(tc.id)
			if e != nil || r.Slot != tc.slot || r.CompoundRate != tc.rate || r.GoldDiscount != tc.discount {
				t.Fatalf("equipped %d: %+v err=%v", tc.id, r, e)
			}
		}
	})
	t.Run("Aiken battle-pass furnace and account protection", func(t *testing.T) {
		shop, err := newCurrentNPCShopCatalog(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			id   uint32
			cost int64
		}{{490008445, 30}, {990031102, 30}, {990031103, 10}} {
			request := currentNPCShopBuyRequest{ItemID: tc.id, Count: 1, ShopContext: 247, ActorContext: 486}
			if err := validateCurrentNPCShopBuyShelf(shop, request); err != nil {
				t.Fatal(err)
			}
			price, err := resolveCurrentNPCShopActorPricing(shop, c, tc.id, 486)
			if err != nil || !price.MaterialExchange || price.NeedMaterialItem != 10161538 || price.NeedMaterialCount != tc.cost || price.ExchangeGold != 0 {
				t.Fatalf("shop %d: %+v %v", tc.id, price, err)
			}
			privateAikenExchangeAssertBuy(t, shop, c, tc.id, tc.cost, 1)
			privateAikenExchangeAssertOneShort(t, shop, c, tc.id, tc.cost, 1)
			privateAikenExchangeAssertCommitFailureRollsBack(t, shop, c, tc.id, tc.cost)
			def, e := c.ResolveItem(tc.id)
			if e != nil {
				t.Fatal(e)
			}
			if tc.id != 490008445 && (normalizeMagicBoxPVFType(def.AttachType) != "account" || currentAuctionTradeable(def) || currentDungeonDiscardTradeable(def)) {
				t.Fatalf("protection %d is transferable: %+v", tc.id, def)
			}
		}
		ft, err := loadCurrentForgingTable(a)
		if err != nil {
			t.Fatal(err)
		}
		f, err := currentForgingResolver(c, a, ft)(490008445, realForgingWeaponItemID)
		if err != nil || !f.PortableFurnace {
			t.Fatalf("battle-pass forge effect lost: %+v err=%v", f, err)
		}
	})
}
