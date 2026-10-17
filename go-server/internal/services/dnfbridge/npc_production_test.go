package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func npcProductionFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group, dungeonDropCatalogTestSource) {
	t.Helper()
	source := equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestGoldDoc)
	source["town/town.lst"] = "7 `production.twn`"
	source["town/production.twn"] = "[area]\n3 `Town/production.map` `[normal]`\n[/area]"
	source["map/Town/production.map"] = "[npc]\n29 100 200 0\n[/npc]"
	source["npc/npc.lst"] = "29 `roger.npc`"
	source["npc/roger.npc"] = "[role]\n`[product item]` 20 `[item shop]` 49\n[/role]"
	source["itemshop/itemshop.lst"] = "20 `ItemProduct1.shp`"
	source["itemshop/ItemProduct1.shp"] = "[NPC]\n29\n[type]\n`[etc shop]`\n[sell info]\n[tab]\n`Materials`\n[item list]\n1000\n[/item list]\n[/tab]\n[/sell info]"
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 1000}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 2000, Count: 20}}}); err != nil {
		t.Fatal(err)
	}
	s := equipmentRecipeCompoundTestService(t, source, repos)
	towns, err := dnftown.Load(ctx, source, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = towns
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, characterGeneration: 1, townSceneReadyCharacterID: 19, townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 19, TownID: 7, AreaID: 3, PositionValid: true}}
	s.gameSessions = map[uint16]*gameSession{19: session}
	return s, session, repos, source
}

func TestNPCProductionItemIDDoesNotRequireOrConsumeDrawing(t *testing.T) {
	s, session, repos, _ := npcProductionFixture(t)
	request, err := parseCurrentEquipmentRecipeCompoundRequest(equipmentRecipeCompoundTestItemIDRequest(1000, 3))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.commitCurrentEquipmentRecipeCompound(context.Background(), session, request)
	if err != nil {
		t.Fatal(err)
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "19")
	character, _, _ := repos.Character.Load(context.Background(), "19")
	if result.RecipeItemID != 1000 || result.SourceSlot != -1 || equipmentRecipeCompoundTestTotal(inv, 2000) != 14 || equipmentRecipeCompoundTestTotal(inv, 3000) != 3 || equipmentRecipeCompoundTestTotal(inv, 1000) != 0 || character.Stats["gold"] != 850 {
		t.Fatalf("NPC production result=%+v inventory=%+v gold=%d", result, inv, character.Stats["gold"])
	}
}

func TestNPCProductionRejectionsDoNotMutateAssets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Service, *gameSession, dnfrepo.Group, dungeonDropCatalogTestSource)
	}{
		{"unbound session", func(s *Service, _ *gameSession, _ dnfrepo.Group, _ dungeonDropCatalogTestSource) {
			s.gameSessions = nil
		}},
		{"unready scene", func(_ *Service, g *gameSession, _ dnfrepo.Group, _ dungeonDropCatalogTestSource) {
			g.townSceneReadyCharacterID = 0
		}},
		{"stale position", func(_ *Service, g *gameSession, _ dnfrepo.Group, _ dungeonDropCatalogTestSource) {
			g.townPositionSnapshot.CharacterID++
		}},
		{"wrong area", func(_ *Service, g *gameSession, _ dnfrepo.Group, _ dungeonDropCatalogTestSource) {
			g.townPositionSnapshot.AreaID++
		}},
		{"NPC absent", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["map/Town/production.map"] = "[npc]\n30 100 200 0\n[/npc]"
		}},
		{"hidden NPC", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["npc/roger.npc"] += "\n[hide at town]\n7"
		}},
		{"ordinary shop only", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["npc/roger.npc"] = "[role]\n`[item shop]` 20\n[/role]"
		}},
		{"unlisted menu", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["npc/roger.npc"] = "[role]\n`[product item]` 21\n[/role]"
		}},
		{"wrong shop owner", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["itemshop/ItemProduct1.shp"] = strings.Replace(p["itemshop/ItemProduct1.shp"], "29", "30", 1)
		}},
		{"recipe absent", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["itemshop/ItemProduct1.shp"] = strings.ReplaceAll(p["itemshop/ItemProduct1.shp"], "1000", "4000")
		}},
		{"malformed trailing item", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["itemshop/ItemProduct1.shp"] = strings.ReplaceAll(p["itemshop/ItemProduct1.shp"], "1000", "1000 ?")
		}},
		{"duplicate menu mapping", func(_ *Service, _ *gameSession, _ dnfrepo.Group, p dungeonDropCatalogTestSource) {
			p["itemshop/itemshop.lst"] += "\n20 `another.shp`"
		}},
		{"insufficient materials", func(_ *Service, _ *gameSession, r dnfrepo.Group, _ dungeonDropCatalogTestSource) {
			_ = r.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}})
		}},
		{"locked material", func(_ *Service, _ *gameSession, r dnfrepo.Group, _ dungeonDropCatalogTestSource) {
			_ = r.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 2000, Count: 20, Extra: map[string]string{"equipment_lock_state": "1"}}}})
		}},
		{"insufficient gold", func(_ *Service, _ *gameSession, r dnfrepo.Group, _ dungeonDropCatalogTestSource) {
			c, _, _ := r.Character.Load(context.Background(), "19")
			c.Stats["gold"] = 0
			_ = r.Character.Save(context.Background(), c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g, r, p := npcProductionFixture(t)
			tc.change(s, g, r, p)
			before, _, _ := r.Inventory.Load(context.Background(), "19")
			character, _, _ := r.Character.Load(context.Background(), "19")
			request, _ := parseCurrentEquipmentRecipeCompoundRequest(equipmentRecipeCompoundTestItemIDRequest(1000, 3))
			if _, err := s.commitCurrentEquipmentRecipeCompound(context.Background(), g, request); err == nil {
				t.Fatal("accepted invalid NPC production")
			}
			after, _, _ := r.Inventory.Load(context.Background(), "19")
			c, _, _ := r.Character.Load(context.Background(), "19")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(character, c) {
				t.Fatal("rejected production changed assets")
			}
		})
	}
}

func TestNPCProductionDispatchKeepsOwnedDrawingAndPublishesCommittedState(t *testing.T) {
	s, g, r, _ := npcProductionFixture(t)
	inv, _, _ := r.Inventory.Load(context.Background(), "19")
	inv.Slots["0:10"] = dnfrepo.ItemStack{ItemID: 1000, Count: 5}
	if err := r.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	equipmentRecipeCompoundTestDispatch(t, s, g, 1, equipmentRecipeCompoundTestItemIDRequest(1000, 3))
	ack, rest := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1, 0, 0}) {
		t.Fatalf("ack=%x", ack.Body)
	}
	equipmentRecipeCompoundTestRefresh(t, rest, map[uint32]uint64{1000: 5, 2000: 14, 3000: 3})
}

type npcProductionFailInventory struct{ dnfrepo.InventoryRepository }

func (r npcProductionFailInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("NPC production inventory save failed")
}

type npcProductionFailUOW struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (u npcProductionFailUOW) WithinCharacterSettlement(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		tx.Inventory = npcProductionFailInventory{tx.Inventory}
		return fn(tx)
	})
}

func TestNPCProductionSaveFailureRollsBackGold(t *testing.T) {
	s, g, repos, _ := npcProductionFixture(t)
	before, _, _ := repos.Inventory.Load(context.Background(), "19")
	failed := repos
	failed.CharacterSettlement = npcProductionFailUOW{repos.CharacterSettlement}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return failed, true }
	request, _ := parseCurrentEquipmentRecipeCompoundRequest(equipmentRecipeCompoundTestItemIDRequest(1000, 3))
	if _, err := s.commitCurrentEquipmentRecipeCompound(context.Background(), g, request); err == nil {
		t.Fatal("save failure accepted")
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "19")
	character, _, _ := repos.Character.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) || character.Stats["gold"] != 1000 {
		t.Fatal("save failure partially committed")
	}
}

type npcProductionBeforeTransaction struct {
	dnfrepo.CharacterSettlementUnitOfWork
	before func()
}

func (u npcProductionBeforeTransaction) WithinCharacterSettlement(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
	u.before()
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, fn)
}

func TestNPCProductionRejectsCharacterGenerationChangedBeforeTransaction(t *testing.T) {
	s, g, repos, _ := npcProductionFixture(t)
	before, _, _ := repos.Inventory.Load(context.Background(), "19")
	repos.CharacterSettlement = npcProductionBeforeTransaction{repos.CharacterSettlement, func() { g.characterGeneration++ }}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	request, _ := parseCurrentEquipmentRecipeCompoundRequest(equipmentRecipeCompoundTestItemIDRequest(1000, 3))
	if _, err := s.commitCurrentEquipmentRecipeCompound(context.Background(), g, request); err == nil {
		t.Fatal("stale request accepted")
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "19")
	character, _, _ := repos.Character.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) || character.Stats["gold"] != 1000 {
		t.Fatal("stale request changed assets")
	}
}

func TestNPCProductionACKFailureDoesNotUndoCommit(t *testing.T) {
	s, g, r, _ := npcProductionFixture(t)
	g.conn = &failNthDungeonWriteConn{failAt: 1, err: errors.New("ACK write failed")}
	if err := s.handleCurrentEquipmentRecipeCompound(g, equipmentRecipeCompoundTestItemIDRequest(1000, 3)); err == nil {
		t.Fatal("write failure swallowed")
	}
	inv, _, _ := r.Inventory.Load(context.Background(), "19")
	character, _, _ := r.Character.Load(context.Background(), "19")
	if equipmentRecipeCompoundTestTotal(inv, 2000) != 14 || equipmentRecipeCompoundTestTotal(inv, 3000) != 3 || character.Stats["gold"] != 850 {
		t.Fatal("ACK failure undid committed production")
	}
}

func TestRealPVFNPCRogerProductionMenuTransactions(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("requires current real PVF")
	}
	a, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	towns, err := dnftown.Load(ctx, a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var townID, areaID int64
	found := false
	for _, town := range towns.Towns() {
		for _, area := range town.Areas {
			m, err := worldmap.LoadTownPlacementMap(a, area.MapPath)
			if err != nil {
				continue
			}
			for _, npc := range m.NPCs {
				if npc.NPCID == 29 {
					townID, areaID, found = town.ID, area.ID, true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("real town with NPC29 absent")
	}
	recipes, err := newPVFEquipmentRecipeCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{5057, 5058, 5059, 5060, 5061, 5062, 5063, 5064, 5065, 5066, 5067, 5068, 5069, 5070, 5071, 5072, 5073, 5074, 5075, 5076, 5077, 5078, 5079, 5080, 5081, 5082, 5083, 5084, 5085, 5101, 5102}
	for _, id := range ids {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			entry, ok, err := recipes.Recipe(id)
			if err != nil || !ok {
				t.Fatalf("recipe missing %d: %v", id, err)
			}
			if id == 5072 && (entry.GoldCost != 1400 || len(entry.Materials) != 1 || entry.Materials[0] != (equipmentRecipeItemStack{ItemID: 3027, Count: 1}) || len(entry.Outputs) != 1 || entry.Outputs[0] != (equipmentRecipeItemStack{ItemID: 3150, Count: 1})) {
				t.Fatalf("steel recipe changed %+v", entry)
			}
			r := dnfrepomemory.NewMemoryGroup()
			inv := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}
			account := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}
			for i, material := range entry.Materials {
				stack := dnfrepo.ItemStack{ItemID: material.ItemID, Count: material.Count * 2}
				if slot, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
					account.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)] = stack
				} else {
					inv.Slots["0:"+strconv.Itoa(121+i)] = stack
				}
			}
			if err := r.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if err := r.AccountInventory.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			if err := r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 1000000}}); err != nil {
				t.Fatal(err)
			}
			catalog, err := newPVFDungeonDropCatalog(a)
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{pvfItemCatalog: catalog, initialEquipmentArchive: a, townCatalog: towns, options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return r, true }}
			g := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, characterGeneration: 1, townSceneReadyCharacterID: 19, townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 19, TownID: byte(townID), AreaID: byte(areaID), PositionValid: true}}
			s.gameSessions = map[uint16]*gameSession{19: g}
			request, _ := parseCurrentEquipmentRecipeCompoundRequest(equipmentRecipeCompoundTestItemIDRequest(uint32(id), 2))
			result, err := s.commitCurrentEquipmentRecipeCompound(ctx, g, request)
			if err != nil {
				t.Fatal(err)
			}
			after, _, _ := r.Inventory.Load(ctx, "19")
			acc, _, _ := r.AccountInventory.Load(ctx, "account-1")
			character, _, _ := r.Character.Load(ctx, "19")
			if result.SourceSlot != -1 || character.Stats["gold"] != 1000000-entry.GoldCost*2 {
				t.Fatalf("bad result %+v gold=%d", result, character.Stats["gold"])
			}
			for _, material := range entry.Materials {
				var remaining int64
				if slot, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
					remaining = acc.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)].Count
				} else {
					remaining = equipmentRecipeCompoundTestTotal(after, material.ItemID)
				}
				if remaining != 0 {
					t.Fatalf("material %d remaining=%d", material.ItemID, remaining)
				}
			}
			for _, output := range entry.Outputs {
				var got int64
				if slot, shared := dnfrepo.AccountWarehouseSlotForItem(output.ItemID); shared {
					got = acc.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)].Count
				} else {
					got = equipmentRecipeCompoundTestTotal(after, output.ItemID)
				}
				if got != output.Count*2 {
					t.Fatalf("output %d count=%d", output.ItemID, got)
				}
			}
			if _, err := s.commitCurrentEquipmentRecipeCompound(ctx, g, request); err == nil {
				t.Fatal("repeat without materials succeeded")
			}
			unchanged, _, _ := r.Inventory.Load(ctx, "19")
			char2, _, _ := r.Character.Load(ctx, "19")
			if !reflect.DeepEqual(after, unchanged) || !reflect.DeepEqual(character, char2) {
				t.Fatal("failed repeat mutated assets")
			}
		})
	}
}
