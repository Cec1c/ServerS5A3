package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/npcshop"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func levelGirl008Archive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("paired008 PVF required")
	}
	a, e := platformpvf.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	return a
}

func TestLevelGirl008RealPlacementAndCoinShop(t *testing.T) {
	a := levelGirl008Archive(t)
	ctx := context.Background()
	for _, p := range []string{"map/cataclysm/town/elvengard/(f)new_seria_room.map", "map/cataclysm/town/elvengard/(r)new_seria_room.map"} {
		m, e := worldmap.LoadTownPlacementMap(a, p)
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for _, n := range m.NPCs {
			if n.NPCID == 20471 {
				count++
				if n.Position.X != 620 || n.Position.Y != 310 {
					t.Fatal(n)
				}
			}
		}
		if count != 1 {
			t.Fatalf("%s: girl count%d", p, count)
		}
	}
	shop, e := newCurrentNPCShopCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	for id, cost := range map[uint32]int64{490009269: 60, 490009270: 120, 490009271: 40, 490009274: 60, 490009275: 20} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			pricing, e := resolveCurrentNPCShopActorPricing(shop, items, id, 20471)
			if e != nil {
				t.Fatal(e)
			}
			if pricing.NeedMaterialItem != 490009268 || pricing.NeedMaterialCount != cost || pricing.ExchangeGold != 0 || pricing.PurchaseLimitPeriod != "accumulate" || pricing.PurchaseLimitScope != "account" || pricing.PurchaseLimitValue != 1 {
				t.Fatal(pricing)
			}
			for _, actor := range []uint32{0, 20471} {
				g := mustCurrentNPCShopRepositories(t, 1000, map[string]r.ItemStack{"0:121": {ItemID: 490009268, Count: cost, Extra: map[string]string{"item_kind": "stackable"}}})
				s := currentNPCShopTestService(g, shop, items)
				q := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
				req := currentNPCShopBuyRequest{ItemID: id, Count: 1, ActorContext: actor, ShopContext: 400000029}
				before, _, _ := g.Inventory.Load(ctx, "19")
				bad := req
				bad.ActorContext = 486
				if e := validateCurrentNPCShopBuyShelf(shop, bad); e == nil {
					t.Fatal("wrong NPC accepted")
				}
				result, e := s.commitCurrentNPCShopBuy(ctx, q, shop, items, req)
				if e != nil {
					t.Fatal(e)
				}
				_ = result
				after, _, _ := g.Inventory.Load(ctx, "19")
				ch, _, _ := g.Character.Load(ctx, "19")
				account, _, _ := g.Account.Load(ctx, "account-1")
				if ch.Stats["gold"] != 1000 {
					t.Fatal("extra gold cost")
				}
				coins, goods := int64(0), int64(0)
				for _, v := range after.Slots {
					if v.ItemID == 490009268 {
						coins += v.Count
					}
					if v.ItemID == int64(id) {
						goods += v.Count
						if !v.ExpireAt.IsZero() && !v.ExpireAt.After(s.gameplayNow()) {
							t.Fatal("new reward expired")
						}
					}
				}
				if coins != 0 || goods != 1 || reflect.DeepEqual(before, after) {
					t.Fatalf("coins%d goods%d", coins, goods)
				}
				policy := currentNPCShopPurchaseLimit(pricing, 1)
				if npcshop.PurchaseCount(account, ch, *policy, time.Now().AddDate(5, 0, 0)) != 1 {
					t.Fatal("lifetime reset")
				}
				if _, e = s.commitCurrentNPCShopBuy(ctx, q, shop, items, req); !errors.Is(e, npcshop.ErrPurchaseLimit) {
					t.Fatalf("repeat=%v", e)
				}
				replay, _, _ := g.Inventory.Load(ctx, "19")
				if !reflect.DeepEqual(after, replay) {
					t.Fatal("replay changed assets")
				}
			}
		})
	}
}

func TestDeathIce008ActualInstanceTransfer(t *testing.T) {
	a := levelGirl008Archive(t)
	items, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	s, q, conn, g := npcEquipmentFixture(t, items, 101040485, 0, 0)
	ctx := context.Background()
	bag, _, _ := g.Inventory.Load(ctx, "19")
	target := bag.Slots["0:9"]
	raw, e := hex.DecodeString("0c0065c105068c6a2f241930000065c105060000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")
	if e != nil || len(raw) != 119 {
		t.Fatalf("raw%d %v", len(raw), e)
	}
	target.RawEntry = raw
	target.Bind = false
	target.ExpireAt = time.Time{}
	target.Extra = map[string]string{"item_kind": "equipment", "pvf_path": "equipment/character/swordman/weapon/beamsword/101040485.equ", "current_exe_equipment_type": "12", "equipped_slot": "12", "equipment_slot": "12", "current_exe_runtime_move": "1", "current_exe_inventory_count": "1", "durability": "48", "max_durability": "48", "ext_data0": "25", "packed_flag_byte": "25", "reinforce": "25", "upgrade_level": "25", "quality_seed": "607087244"}
	bag.Slots["0:9"] = target
	bag.Slots["0:122"] = r.ItemStack{ItemID: 10158964, Count: 15, Extra: map[string]string{"item_kind": "stackable"}}
	if e = g.Inventory.Save(ctx, bag); e != nil {
		t.Fatal(e)
	}
	account, _, _ := g.Account.Load(ctx, "a")
	account.Metadata[r.AccountEpicPieceMetadataKey(10300811)] = "100"
	if e = g.Account.Save(ctx, account); e != nil {
		t.Fatal(e)
	}
	if e = s.handleCurrentEquipmentTransfer(q, npcEquipmentBody557(2)); e != nil {
		t.Fatal(e)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 557 || ack.Body[0] != 1 {
		t.Fatal("actual instance rejected", ack)
	}
	mail, _, _ := g.Mailbox.Load(ctx, "42")
	account, _, _ = g.Account.Load(ctx, "a")
	if len(mail.Mails) != 1 || account.Metadata[r.AccountEpicPieceMetadataKey(10300811)] != "0" {
		t.Fatal("piece/mail mismatch")
	}
	for _, m := range mail.Mails {
		if m.Attachments[0].ItemID != 101040485 || !bytes.Equal(m.Attachments[0].RawEntry, raw) || !reflect.DeepEqual(m.Attachments[0].Extra, target.Extra) {
			t.Fatal("changed real item properties")
		}
	}
}
