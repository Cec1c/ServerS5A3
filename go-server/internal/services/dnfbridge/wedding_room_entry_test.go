package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestWeddingRoomEntryRealPVFTopology(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("requires paired runtime PVF")
	}
	source, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(context.Background(), source, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var seriaMap, roomMap worldmap.Map
	for _, location := range [][2]int64{{38, 1}, {101, 0}} {
		area, found := towns.FindArea(location[0], location[1])
		if !found {
			t.Fatalf("missing area %v", location)
		}
		placement, err := worldmap.LoadTownPlacementMap(source, area.MapPath)
		if err != nil {
			t.Fatal(err)
		}
		if location[0] == 38 {
			seriaMap = placement
		} else {
			roomMap = placement
		}
	}
	doorFound, returnDoorFound, merchantFound := false, false, false
	for _, door := range seriaMap.TownMovableAreas {
		if door.Params == ([2]int64{101, 0}) && door.Width > 0 && door.Height > 0 {
			doorFound = true
		}
	}
	for _, door := range roomMap.TownMovableAreas {
		if door.Params == ([2]int64{-101, 0}) && door.Width > 0 && door.Height > 0 {
			returnDoorFound = true
		}
	}
	for _, npc := range roomMap.NPCs {
		merchantFound = merchantFound || npc.NPCID == 20011
	}
	if !doorFound || !returnDoorFound || !merchantFound {
		t.Fatalf("room topology: entry=%v return=%v merchant=%v", doorFound, returnDoorFound, merchantFound)
	}
	// 3709D58's married-only Town101 branch uses 36FF4B0, whose exact
	// native body is tail0 (not the ordinary Seria walk-out tail6).
	request := currentTownSetUserAreaRequest{TownID: 101, AreaID: 0, PositionX: 900, PositionY: 250, Direction: 5, OpaqueU16A: 38, OpaqueU16B: 1}
	if !request.LooksLikeTownMapStationSelection() || !request.LooksLikeTownTransportSelection() {
		t.Fatal("native marriage door rejected by existing cross-town admission")
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	var furnitureShop uint32
	for id, npc := range shop.shopNPCs {
		if npc == 20011 {
			if _, found := shop.shopItems[id][10002685]; found {
				furnitureShop = id
			}
		}
	}
	if furnitureShop == 0 {
		t.Fatal("real marriage-room merchant has no furniture shelf")
	}
	if err := validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: 10002685, Count: 1, ShopContext: furnitureShop, ActorContext: 20011}); err != nil {
		t.Fatal(err)
	}
	pricing, err := resolveCurrentNPCShopActorPricing(shop, catalog, 10002685, 20011)
	if err != nil || !pricing.Buyable || !pricing.MaterialExchange || pricing.NeedMaterialItem != 10002606 {
		t.Fatalf("room merchant pricing: %+v %v", pricing, err)
	}
	// Exercise the production Town36 owner and pair-instance selection. No
	// special wedding entry route is necessary for the native tail0 body.
	s, actor, repos := weddingLovePointFixture(t, 0)
	s.townCatalog, s.initialEquipmentArchive = towns, source
	s.pvfItemCatalog = catalog
	ctx := context.Background()
	c, _, _ := repos.Character.Load(ctx, "29")
	c.Stats["town_id"], c.Stats["area_id"], c.Stats["area_state"] = 38, 1, 3
	if err := r.SaveCharacterFields(ctx, repos.Character, c, r.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 29, Session: actor, ChannelID: actor.channel.ID, TownID: 38, AreaID: 1, Instance: 29})
	other := s.gameSessions[30]
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 30, Session: other, ChannelID: other.channel.ID, TownID: 38, AreaID: 1, Instance: 30})
	body := buildTownMoveRequest(101, 0, 900, 250, 5)
	binary.LittleEndian.PutUint16(body[7:9], 38)
	binary.LittleEndian.PutUint16(body[9:11], 1)
	if err := s.handleTownSetUserArea(actor, body); err != nil {
		t.Fatal(err)
	}
	presence, found := s.onlinePlayers.PlayerForCharacter(29)
	c, _, _ = repos.Character.Load(ctx, "29")
	if !found || presence.TownID != 101 || presence.AreaID != 0 || presence.Instance != weddingPairInstance(29, 30) || c.Stats["town_id"] != 101 || c.Stats["area_id"] != 0 {
		t.Fatalf("native marriage-room move not committed: presence=%+v stats=%v", presence, c.Stats)
	}
	// Simulate the next native town position update after scene creation,
	// which is required by all ordinary NPC-scene authorization checks.
	actor.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 101, AreaID: 0, PositionX: 900, PositionY: 250, PositionValid: true}
	identity, _ := s.boundGameSessionCharacterSnapshot(actor)
	if err := s.validateUpgradeNPCScene(identity, 20011); err != nil {
		t.Fatalf("real marriage-room NPC placement unreadable: %v", err)
	}
	body = buildTownMoveRequest(38, 1, 300, 200, 5)
	binary.LittleEndian.PutUint16(body[7:9], 101)
	if err := s.handleTownSetUserArea(actor, body); err != nil {
		t.Fatal(err)
	}
	presence, found = s.onlinePlayers.PlayerForCharacter(29)
	if !found || presence.TownID != 38 || presence.AreaID != 1 || presence.Instance != 29 {
		t.Fatalf("native return door not committed: %+v", presence)
	}
}
