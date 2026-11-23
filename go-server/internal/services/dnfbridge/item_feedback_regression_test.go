package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestXunlongGroundGoldCreditsBonusExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		slot    int16
		id      int64
		expired bool
		want    uint32
	}{
		{"worn", 22, 100341606, false, 130}, {"wrong_slot", 23, 100341606, false, 100},
		{"different_item", 22, 100341605, false, 100}, {"expired", 22, 100341606, true, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, run, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
			key := seedCurrentDungeonPickupDrop(t, s, run, 3227, 99)
			drop := run.DropOwner.byObjectKey[key]
			drop.Item = dungeonDropItemDefinition{}
			drop.Amount = 100
			entry := dnfrepo.EquipmentEntry{SlotIndex: tc.slot, ItemID: tc.id}
			if tc.expired {
				entry.ExpireAt = time.Unix(1, 0)
			}
			if err := repos.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{CharacterID: "99", Entries: map[string]dnfrepo.EquipmentEntry{"22": entry}}); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: run}}
			for attempt := 0; attempt < 2; attempt++ {
				conn.write.Reset()
				if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
					t.Fatal(err)
				}
				pickup, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				wallet, rest := splitGameServerUpperPacket(t, rest)
				if pickup.Header.MsgID != 39 || binary.LittleEndian.Uint32(pickup.Body[7:11]) != tc.want || wallet.Header.MsgID != 14 || len(rest) != 0 {
					t.Fatalf("unexpected pickup=%x wallet=%x", pickup.Body, wallet.Body)
				}
				char, _, err := repos.Character.Load(context.Background(), "99")
				if err != nil || char.Stats["gold"] != int64(tc.want) || drop.Amount != 100 {
					t.Fatalf("attempt=%d wallet=%d base=%d err=%v", attempt, char.Stats["gold"], drop.Amount, err)
				}
				// A replay after removing the support keeps the committed response, but
				// neither grants again nor recomputes from the now-empty equipment.
				if err := repos.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{CharacterID: "99"}); err != nil {
					t.Fatal(err)
				}
			}
			next := seedCurrentDungeonPickupDrop(t, s, run, 3227, 99)
			nextDrop := run.DropOwner.byObjectKey[next]
			nextDrop.Item = dungeonDropItemDefinition{}
			nextDrop.Amount = 100
			if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(next)); err != nil {
				t.Fatal(err)
			}
			char, _, _ := repos.Character.Load(context.Background(), "99")
			if char.Stats["gold"] != int64(tc.want)+100 {
				t.Fatalf("unequipped pickup wallet=%d", char.Stats["gold"])
			}
		})
	}
}

func TestXunlongGroundGoldRoundingAndOverflow(t *testing.T) {
	eq := dnfrepo.EquipmentRecord{CharacterID: "99", Entries: map[string]dnfrepo.EquipmentEntry{"22": {SlotIndex: 22, ItemID: 100341606}}}
	for _, n := range []uint32{1, 3, 4, 10, 101, 1000} {
		got, err := currentXunlongPickupGold(eq, n, time.Now())
		if err != nil || got != n*130/100 {
			t.Fatalf("amount=%d got=%d err=%v", n, got, err)
		}
	}
	if _, err := currentXunlongPickupGold(eq, math.MaxInt32, time.Now()); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestRealPVFGuildAuraTicketOpensOnce(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(490003364)
	if err != nil || definition.ActionType != "[open aura skin slot]" {
		t.Fatalf("guild ticket=%+v err=%v", definition, err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 490003364, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 65)
	for i := 0; i < 2; i++ {
		conn.write.Reset()
		if err := s.handleCurrentOpenAuraSkinSlot(session, body); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if ack.Header.MsgID != 863 || len(ack.Body) != 1 || ack.Body[0] != 1 {
			t.Fatalf("ack=%x", ack.Body)
		}
		char, _, _ := repos.Character.Load(ctx, "19")
		inv, _, _ := repos.Inventory.Load(ctx, "19")
		if char.Stats["aura_flag"] != 1 || inv.Slots["0:65"].Count != 1 {
			t.Fatalf("attempt=%d flag=%d count=%d", i, char.Stats["aura_flag"], inv.Slots["0:65"].Count)
		}
	}
	t.Logf("verified real PVF item %d %s action=%s; first consumes one, repeat preserves", definition.ItemID, definition.PVFPath, definition.ActionType)
}

func TestRealPVFGuildAuraTicketRightClick(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(490003364)
	if err != nil || definition.ActionType != "[open aura skin slot]" {
		t.Fatalf("guild ticket=%+v err=%v", definition, err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:65": {ItemID: 490003364, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	body := make([]byte, 19)
	binary.LittleEndian.PutUint16(body, 65)
	binary.LittleEndian.PutUint32(body[7:], 126)
	// A different inventory slot contains no ticket. It must not grant access.
	body[0] = 66
	if err := s.handleGameCommand(session, 1, 515, body); err != nil {
		t.Fatal(err)
	}
	failure, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if failure.Header.MsgID != 515 || failure.Body[0] != 0 {
		t.Fatalf("missing-ticket ack=%x", failure.Body)
	}
	body[0] = 65
	for i := 0; i < 2; i++ {
		conn.write.Reset()
		if err := s.handleGameCommand(session, 1, 515, body); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if ack.Header.MsgID != 515 || len(ack.Body) != 8 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[1:3]) != 65 || binary.LittleEndian.Uint32(ack.Body[4:8]) != 126 {
			t.Fatalf("ack=%x", ack.Body)
		}
		char, _, _ := repos.Character.Load(ctx, "19")
		inv, _, _ := repos.Inventory.Load(ctx, "19")
		if char.Stats["aura_flag"] != 1 || inv.Slots["0:65"].Count != 1 {
			t.Fatalf("attempt=%d flag=%d count=%d", i, char.Stats["aura_flag"], inv.Slots["0:65"].Count)
		}
	}
	t.Logf("verified real PVF item %d %s action=%s; first consumes one, repeat preserves", definition.ItemID, definition.PVFPath, definition.ActionType)
}

func TestDungeonPickupAbsoluteCountMarkerExcludesUnpairedNativeResult(t *testing.T) {
	normal, err := buildCurrentDungeonNormalPickupResultBody(403, 99, 121)
	if err != nil {
		t.Fatal(err)
	}
	paired, err := buildCurrentDungeonPickupResultForItem(403, 99, 121, dungeonDropItemDefinition{Kind: dungeonDropItemStackable})
	if err != nil {
		t.Fatal(err)
	}
	if normal[18] != 0 || paired[18] != 128 {
		t.Fatalf("native=%x paired=%x", normal, paired)
	}
	for i := 0; i < 18; i++ {
		if normal[i] != paired[i] {
			t.Fatalf("unrelated wire byte %d changed", i)
		}
	}
}

func TestDungeonPickupProofAcceptsOnlyLegacyOrPairedMarker(t *testing.T) {
	for _, marker := range []byte{0, 128, 1, 129, 255} {
		drop := &runtimeDungeonDrop{ObjectKey: 421, OwnerActorObjectKey: 99, DestinationSlot: 121, Status: runtimeDungeonDropConsumed, Item: dungeonDropItemDefinition{ItemID: 3227, Kind: dungeonDropItemStackable}}
		drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(421, 99, 121)
		drop.PickupResponseBody[18] = marker
		_, ok := currentDungeonCommittedPickupProof(drop)
		if ok != (marker == 0 || marker == 128) {
			t.Fatalf("marker=%d accepted=%t", marker, ok)
		}
		drop.PickupResponseBody[0]++
		if _, ok := currentDungeonCommittedPickupProof(drop); ok {
			t.Fatalf("corrupt object accepted marker=%d", marker)
		}
	}
}
