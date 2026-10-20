package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentAuctionRealPVFHasTradeableAndBlockedStackables(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to validate auction eligibility")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	allowed, denied := 0, 0
	for itemID, reference := range catalog.itemRefs {
		if reference.kind != dungeonDropItemStackable {
			continue
		}
		definition, resolveErr := catalog.ResolveItem(itemID)
		if resolveErr != nil {
			continue
		}
		eligible := currentDungeonDiscardTradeable(definition) && (!definition.AuctionRuleFound || definition.AuctionAllowed)
		if eligible {
			allowed++
		} else {
			denied++
		}
		if allowed != 0 && denied != 0 {
			break
		}
	}
	if allowed == 0 || denied == 0 {
		t.Fatalf("stackable auction rules allowed=%d denied=%d", allowed, denied)
	}

	// Live op182 evidence came from the listing dialog for item 24006
	// (古老的莲花戒指). Keep that exact white equipment category covered so a
	// future catalog/parser change cannot put the dialog back into its waiting
	// state after the request decoder has been fixed.
	ring, err := catalog.ResolveItem(24006)
	if err != nil {
		t.Fatal(err)
	}
	if ring.Kind != dungeonDropItemEquipment || ring.Rarity != 0 ||
		!currentDungeonDiscardTradeable(ring) || (ring.AuctionRuleFound && !ring.AuctionAllowed) {
		t.Fatalf("item 24006 definition=%+v", ring)
	}
	plain := make([]byte, currentItemListEntryWireSize)
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(ring, dnfrepo.ItemStack{
		ItemID: 24006, Count: 1, RawEntry: plain,
	}); !eligible {
		t.Fatalf("tradeable white item 24006 must be auction eligible")
	}

	// The live current-client request also selects item 490702515 from slot
	// 110. It is an enchant bead/waste stackable and must follow the same
	// tradeable consumable/material route instead of being mistaken for an
	// equipment row that requires a seal byte.
	bead, err := catalog.ResolveItem(490702515)
	if err != nil {
		t.Fatal(err)
	}
	if bead.Kind != dungeonDropItemStackable || !currentDungeonDiscardTradeable(bead) ||
		(bead.AuctionRuleFound && !bead.AuctionAllowed) {
		t.Fatalf("item 490702515 definition=%+v", bead)
	}
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(bead, dnfrepo.ItemStack{
		ItemID: 490702515, Count: 1, RawEntry: plain,
	}); !eligible {
		t.Fatalf("tradeable enchant bead item 490702515 must be auction eligible")
	}

	// Active material-tab capture: item 3051 (斯卡迪女王的印章), two
	// units at 1,500 gold each. It is a normal tradeable material and must not
	// be filtered by the equipment/seal rules.
	material, err := catalog.ResolveItem(3051)
	if err != nil {
		t.Fatal(err)
	}
	if material.Kind != dungeonDropItemStackable || !currentDungeonDiscardTradeable(material) ||
		(material.AuctionRuleFound && !material.AuctionAllowed) {
		t.Fatalf("item 3051 definition=%+v", material)
	}
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(material, dnfrepo.ItemStack{
		ItemID: 3051, Count: 2, RawEntry: plain,
	}); !eligible {
		t.Fatalf("tradeable material item 3051 must be auction eligible")
	}

	// The reproduced listing dialog uses item 10081. It has no live auction
	// history in a fresh database, so its positive PVF price is the native
	// price-control fallback instead of an all-zero response.
	robe, err := catalog.ResolveItem(10081)
	if err != nil {
		t.Fatal(err)
	}
	if robe.Kind != dungeonDropItemEquipment || robe.Rarity != 1 || !robe.PriceFound || robe.Price <= 0 {
		t.Fatalf("item 10081 definition=%+v", robe)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, context.Background(), repositories, "1", "dnf:1", "Seller", 100000, map[string]dnfrepo.ItemStack{
		"0:10":  {ItemID: 24006, Count: 1, RawEntry: append([]byte(nil), plain...)},
		"0:110": {ItemID: 490702515, Count: 1, RawEntry: append([]byte(nil), plain...)},
		"0:164": {ItemID: 3051, Count: 2, RawEntry: append([]byte(nil), plain...)},
	})
	connection := &bufferConn{}
	service := &Service{
		options:            options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	request := make([]byte, 8)
	request[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(request[1:5], 10081)
	if err := service.sendCurrentAuctionAveragePriceLocked(&gameSession{conn: connection}, request); err != nil {
		t.Fatal(err)
	}
	response, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(response.Body) != 58 || response.Body[0] != 1 ||
		binary.LittleEndian.Uint32(response.Body[2:6]) != uint32(robe.Price) ||
		binary.LittleEndian.Uint32(response.Body[6:10]) != 0 {
		t.Fatalf("item 10081 empty-market op182 body=%x trailing=%d", response.Body, len(trailing))
	}

	// A tradeable bead with no PVF shop price still needs a positive native
	// control bound or the dialog never leaves "collecting auction price".
	connection.write.Reset()
	request = make([]byte, 8)
	request[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(request[1:5], 490702515)
	if err := service.sendCurrentAuctionAveragePriceLocked(&gameSession{conn: connection}, request); err != nil {
		t.Fatal(err)
	}
	response, trailing = splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(response.Body) != 58 || response.Body[0] != 1 ||
		binary.LittleEndian.Uint32(response.Body[2:6]) != 1 ||
		binary.LittleEndian.Uint32(response.Body[6:10]) != 0 {
		t.Fatalf("item 490702515 no-history op182 body=%x trailing=%d", response.Body, len(trailing))
	}

	// Replay both exact live registration shapes end to end. The current source
	// must accept the white ring and the tradeable enchant bead, move each row
	// into escrow and return the native {success,pay} ACK.
	seedAuctionSequenceForBridge(t, service, 1000)
	session := &gameSession{conn: connection, selectedCharacterID: 1, accountID: "dnf:1"}
	for _, registration := range [][]byte{
		buildCurrentAuctionRegisterRequestForTest(10, 24006, 1, -1, 6000, 6000, 24),
		buildCurrentAuctionRegisterRequestForTest(110, 490702515, 1, -1, 11111, 11111, 24),
		buildCurrentAuctionRegisterRequestForTest(164, 3051, 2, -1, 1500, 1500, 24),
	} {
		connection.write.Reset()
		if err := service.registerCurrentAuctionItemLocked(session, registration); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{1, 0}) {
			t.Fatalf("live registration ACK=%x", ack.Body)
		}
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "1")
	if err != nil || !found || inventory.Slots["0:10"].ItemID != 0 || inventory.Slots["0:110"].ItemID != 0 ||
		inventory.Slots["0:164"].ItemID != 0 {
		t.Fatalf("registered live rows remain in seller inventory found=%t err=%v slots=%+v", found, err, inventory.Slots)
	}
}
