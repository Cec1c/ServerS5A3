package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type npcShopRepairFixture struct {
	shop    *currentNPCShopCatalog
	items   *pvfDungeonDropCatalog
	towns   *town.Table
	archive *platformpvf.Archive
	scenes  map[uint32]currentTownPositionSnapshot
}

func mustNPCShopRepairFixture(t *testing.T) npcShopRepairFixture {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := npcShopRepairFixture{shop: shop, items: items, towns: towns, archive: archive, scenes: map[uint32]currentTownPositionSnapshot{}}
	for _, entry := range towns.Towns() {
		for _, area := range entry.Areas {
			placement, err := worldmap.LoadTownPlacementMap(archive, area.MapPath)
			if err != nil {
				continue // Some town areas are not placement maps.
			}
			for _, npc := range placement.NPCs {
				if npc.NPCID > 0 && npc.NPCID <= 65535 {
					if _, exists := f.scenes[uint32(npc.NPCID)]; !exists {
						f.scenes[uint32(npc.NPCID)] = currentTownPositionSnapshot{CharacterID: 19, TownID: byte(entry.ID), AreaID: byte(area.ID), PositionValid: true}
					}
				}
			}
		}
	}
	return f
}

func (f npcShopRepairFixture) service(t *testing.T, repos dnfrepo.Group, npc uint32) (*Service, *gameSession) {
	t.Helper()
	p, found := f.scenes[npc]
	if !found {
		t.Fatalf("real NPC %d has no town placement", npc)
	}
	s := currentNPCShopTestService(repos, f.shop, f.items)
	s.townCatalog, s.initialEquipmentArchive = f.towns, f.archive
	g := &gameSession{conn: &bufferConn{}, connID: "npc-shop-context-regression", selectedCharacterID: 19,
		townSceneReadyCharacterID: 19, townPositionSnapshot: p}
	s.gameSessions = map[uint16]*gameSession{19: g}
	return s, g
}

func TestNPCShopContextRepairRealHandlers(t *testing.T) {
	f := mustNPCShopRepairFixture(t)
	for _, tc := range []struct {
		name                   string
		shop, npc, item, count uint32
	}{
		{"aiken_magic_stone_18", 247, 486, 10161536, 18},
		{"aiken_permanent_avatar_coupon", 247, 486, 990031104, 2},
		{"seria", 15, 2, 0, 1},
		{"linus", 2, 1, 0, 1},
		{"klonter", 35, 26, 0, 1},
		{"naen", 108, 326, 0, 1},
		{"wuun", 110, 328, 0, 1},
		{"erika", 246, 460, 0, 1},
		{"jonathan_time_stone", 0, 476, 10158700, 1},
		{"jonathan_refined_stone", 0, 476, 10158964, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := tc.item
			if item == 0 {
				var materialFallback uint32
				var ids []int
				for id := range f.shop.shopItems[tc.shop] {
					ids = append(ids, int(id))
				}
				sort.Ints(ids)
				for _, id := range ids {
					p, err := resolveCurrentNPCShopActorPricing(f.shop, f.items, uint32(id), tc.npc)
					priced := !p.MaterialExchange && p.BuyGold > 0 && p.BuyGold < 1000000
					material := p.MaterialExchange && len(p.AdditionalMaterials) == 0 && p.NeedMaterialItem != 10002606
					if err == nil && p.Buyable && (priced || material) &&
						(p.Definition.Kind == dungeonDropItemEquipment || strings.Trim(p.Definition.StackableType, "[] ") == "waste" || strings.Trim(p.Definition.StackableType, "[] ") == "material") {
						if priced {
							item = uint32(id)
							break
						}
						if materialFallback == 0 {
							materialFallback = uint32(id)
						}
					}
				}
				if item == 0 {
					item = materialFallback
				}
				if item == 0 {
					t.Fatal("ordinary priced real shelf fixture missing")
				}
			}
			p, err := resolveCurrentNPCShopActorPricing(f.shop, f.items, item, tc.npc)
			if err != nil || !p.Buyable {
				t.Fatalf("pricing=%+v err=%v", p, err)
			}
			count, wireCount := tc.count, tc.count
			if p.Definition.Kind == dungeonDropItemEquipment {
				count, wireCount = 1, 0
			}
			for _, actor := range []uint32{1000, 0, tc.npc} {
				t.Run(fmt.Sprintf("actor_%d", actor), func(t *testing.T) {
					slots := map[string]dnfrepo.ItemStack{}
					var cost int64
					if p.MaterialExchange {
						cost = p.NeedMaterialCount * int64(count)
						slots["0:121"] = dnfrepo.ItemStack{ItemID: int64(p.NeedMaterialItem), Count: cost + 2}
						if len(p.AdditionalMaterials) != 0 {
							t.Fatal("fixture requires additional materials")
						}
					}
					repos := mustCurrentNPCShopRepositories(t, 10000000, slots)
					s, g := f.service(t, repos, tc.npc)
					if err := s.handleCurrentNPCShopBuy(g, currentNPCShopBuyTestBody(item, wireCount, tc.shop, actor)); err != nil {
						t.Fatal(err)
					}
					ack, _ := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
					if len(ack.Body) == 0 || ack.Body[0] != 1 {
						t.Fatalf("real shelf %d NPC %d item %d actor %d rejected: %x", tc.shop, tc.npc, item, actor, ack.Body)
					}
					character, _, _ := repos.Character.Load(context.Background(), "19")
					wantGold := int64(10000000) - p.BuyGold*int64(count)
					if p.MaterialExchange {
						wantGold = 10000000 - p.ExchangeGold*int64(count)
					}
					if character.Stats["gold"] != wantGold {
						t.Fatalf("gold=%d want=%d", character.Stats["gold"], wantGold)
					}
					bag, _, _ := repos.Inventory.Load(context.Background(), "19")
					delivered := int64(item)
					if item == 990031104 {
						delivered = 2681593
						assertCurrentNPCShopInventoryAmount(t, bag, int64(item), 0)
					}
					assertCurrentNPCShopInventoryAmount(t, bag, delivered, int64(count))
					if p.MaterialExchange {
						assertCurrentNPCShopInventoryAmount(t, bag, int64(p.NeedMaterialItem), 2)
					}
				})
			}
		})
	}
}

func TestNPCShopContextRepairRejectsUnprovenSceneAndShelf(t *testing.T) {
	f := mustNPCShopRepairFixture(t)
	for _, state := range []string{"unready", "invalid_position", "wrong_character", "unbound", "wrong_area", "wrong_shop", "wrong_product"} {
		t.Run(state, func(t *testing.T) {
			repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 10161530, Count: 100}})
			s, g := f.service(t, repos, 486)
			q := currentNPCShopBuyRequest{ItemID: 10161536, Count: 18, ShopContext: 247, ActorContext: 1000}
			switch state {
			case "unready":
				g.townSceneReadyCharacterID = 0
			case "invalid_position":
				g.townPositionSnapshot.PositionValid = false
			case "wrong_character":
				g.townPositionSnapshot.CharacterID = 20
			case "unbound":
				delete(s.gameSessions, 19)
			case "wrong_area":
				g.townPositionSnapshot = f.scenes[2]
			case "wrong_shop":
				q.ShopContext = 999999
			case "wrong_product":
				q.ItemID = 10158700
			}
			beforeCharacter, _, _ := repos.Character.Load(context.Background(), "19")
			beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
			if err := s.handleCurrentNPCShopBuy(g, currentNPCShopBuyTestBody(q.ItemID, q.Count, q.ShopContext, q.ActorContext)); err != nil {
				t.Fatal(err)
			}
			ack, tail := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
			if len(tail) != 0 || !bytes.Equal(ack.Body, []byte{0, 0x11}) {
				t.Fatalf("context failure must not be bag-full: %x tail=%d", ack.Body, len(tail))
			}
			privateAikenExchangeAssertAssetsEqual(t, repos, beforeCharacter, beforeBag)
		})
	}
}

func TestNPCShopContextRepairAikenTitleLifetimeLimit(t *testing.T) {
	f := mustNPCShopRepairFixture(t)
	repos := mustCurrentNPCShopRepositories(t, 1000, nil)
	s, g := f.service(t, repos, 486)
	body := currentNPCShopBuyTestBody(uint32(currentAikenSupportTitleOwnedID), 0, 247, 1000)
	if err := s.handleCurrentNPCShopBuy(g, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
	if len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("first title purchase rejected: %x", ack.Body)
	}
	character, _, _ := repos.Character.Load(context.Background(), "19")
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	if character.Stats["gold"] != 999 {
		t.Fatalf("title gold=%d want999", character.Stats["gold"])
	}
	assertCurrentNPCShopInventoryAmount(t, bag, currentAikenSupportTitleOwnedID, 1)
	// Discard the delivered item: the persisted character lifetime allowance
	// must still prevent a second purchase through the repaired context.
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	beforeCharacter, _, _ := repos.Character.Load(context.Background(), "19")
	beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
	g.conn.(*bufferConn).write.Reset()
	if err := s.handleCurrentNPCShopBuy(g, body); err != nil {
		t.Fatal(err)
	}
	ack, tail := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
	if len(tail) != 0 || !bytes.Equal(ack.Body, []byte{0, 0x85}) {
		t.Fatalf("repeat title ACK=%x tail=%d", ack.Body, len(tail))
	}
	privateAikenExchangeAssertAssetsEqual(t, repos, beforeCharacter, beforeBag)
}

func TestNPCShopRestrictedSaleRejectsZeroCredit(t *testing.T) {
	f := mustNPCShopRepairFixture(t)
	for _, actor := range []uint32{0, 1000, 326, 459, 476} {
		t.Run(fmt.Sprintf("actor_%d", actor), func(t *testing.T) {
			repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 10158700, Count: 2}})
			s, g := f.service(t, repos, 476)
			beforeCharacter, _, _ := repos.Character.Load(context.Background(), "19")
			beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
			if err := s.handleCurrentNPCShopSell(g, currentNPCShopSellTestBody(0, 121, 1, actor)); err != nil {
				t.Fatal(err)
			}
			ack, tail := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
			if actor != 459 && actor != 476 {
				if len(tail) != 0 || !bytes.Equal(ack.Body, []byte{0, 0x11}) {
					t.Fatalf("restricted sale must fail: %x tail=%d", ack.Body, len(tail))
				}
				privateAikenExchangeAssertAssetsEqual(t, repos, beforeCharacter, beforeBag)
				return
			}
			if len(ack.Body) == 0 || ack.Body[0] != 1 {
				t.Fatalf("allowed merchant sale failed: %x", ack.Body)
			}
			character, _, _ := repos.Character.Load(context.Background(), "19")
			bag, _, _ := repos.Inventory.Load(context.Background(), "19")
			if character.Stats["gold"] != 7666 {
				t.Fatalf("sale gold=%d want7666", character.Stats["gold"])
			}
			assertCurrentNPCShopInventoryAmount(t, bag, 10158700, 1)
		})
	}
}
