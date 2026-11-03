package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// Exercise the ground-pickup owner, not the already-correct shop/booster grant.
func TestDungeonPickupDuplicateGuildMedalKeepsMedalContainer(t *testing.T) {
	ctx := context.Background()
	catalog := guildMedalRoutingCatalog(t)
	definition, err := catalog.ResolveItem(uint32(guildMedalRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	group := memory.NewMemoryGroup()
	first := repo.ItemStack{ItemID: guildMedalRoutingItemID, Count: 1, Extra: map[string]string{"instance": "existing-medal"}}
	if err := group.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"38:0": first}}); err != nil {
		t.Fatal(err)
	}
	slot, _, err := grantCurrentDungeonPickupItem(ctx, group, "19", definition, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := group.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if slot != 1 || actual.Slots["38:1"].ItemID != guildMedalRoutingItemID || actual.Slots["38:1"].Count != 1 {
		t.Fatalf("second identical medal went to slot=%d inventory=%+v; want a separate 38:1 instance", slot, actual.Slots)
	}
	if !reflect.DeepEqual(actual.Slots["38:0"], first) || len(actual.Slots) != 2 {
		t.Fatalf("existing medal changed or reward duplicated: %+v", actual.Slots)
	}
}

func TestDungeonPickupFullGuildMedalPageDoesNotSpillToEquipment(t *testing.T) {
	ctx := context.Background()
	catalog := guildMedalRoutingCatalog(t)
	definition, err := catalog.ResolveItem(uint32(guildMedalRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	group := memory.NewMemoryGroup()
	before := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}
	for slot := 0; slot <= 48; slot++ {
		before.Slots[fmt.Sprintf("38:%d", slot)] = repo.ItemStack{ItemID: guildMedalRoutingItemID, Count: 1}
	}
	if err := group.Inventory.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	_, _, err = grantCurrentDungeonPickupItem(ctx, group, "19", definition, 1, time.Now().UTC())
	if !errors.Is(err, errDungeonPickupInventoryFull) {
		t.Fatalf("full medal page error=%v, want full-page rejection even with empty equipment page", err)
	}
	after, _, err := group.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed pickup changed inventory")
	}
}

func TestDungeonPickupGuildMedalCompletesContainerProjection(t *testing.T) {
	catalog := guildMedalRoutingCatalog(t)
	for _, tc := range []struct {
		name string
		id   uint32
		slot uint16
	}{
		{"first_medal", uint32(guildMedalRoutingItemID), 0},
		{"second_identical_medal", uint32(guildMedalRoutingItemID), 1},
		{"guardian_gem", uint32(guardianGemRoutingItemID), 49},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def, err := catalog.ResolveItem(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			verifyGuildPickupProjection(t, def, tc.slot)
		})
	}
}

func verifyGuildPickupProjection(t *testing.T, def dungeonDropItemDefinition, want uint16) {
	t.Helper()
	s, runtime, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
	if want == 1 {
		inv, _, _ := repos.Inventory.Load(context.Background(), "99")
		inv.Slots = map[string]repo.ItemStack{"38:0": {ItemID: int64(def.ItemID), Count: 1}}
		if err := repos.Inventory.Save(context.Background(), inv); err != nil {
			t.Fatal(err)
		}
	}
	key := seedCurrentDungeonPickupDrop(t, s, runtime, 3227, currentSceneActorObjectKey(99))
	drop := runtime.DropOwner.byObjectKey[key]
	drop.Item = def
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "guild-pickup", accountID: "account-1", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "99")
	stack := inv.Slots[fmt.Sprintf("38:%d", want)]
	if stack.ItemID != int64(def.ItemID) || stack.Count != 1 {
		t.Fatalf("dedicated page missing item: %+v", inv.Slots)
	}
	if len(conn.write.Bytes()) == 0 {
		t.Fatalf("slot %d left client waiting", want)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	update, rest := splitGameServerUpperPacket(t, rest)
	if ack.Header.MsgID != 39 || binary.LittleEndian.Uint16(ack.Body[16:18]) != want || update.Header.MsgID != 14 || update.Body[0] != 38 || binary.LittleEndian.Uint16(update.Body[3:5]) != want || len(rest) != 0 {
		t.Fatalf("wrong dedicated pickup projection ack=%x update=%x", ack.Body, update.Body)
	}
	if stack.RawEntry == nil || !bytes.Equal(stack.RawEntry, update.Body[3:]) {
		t.Fatal("wire row differs from committed row")
	}
	first := append([]byte(nil), conn.write.Bytes()...)
	conn.write.Reset()
	if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first[13:32], conn.write.Bytes()[13:32]) {
		t.Fatal("retry changed pickup body")
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "99")
	if !reflect.DeepEqual(inv, after) {
		t.Fatal("retry granted a second item")
	}
	for _, bookExemption := range []bool{false, true} {
		rows, exempt, _, err := currentDungeonCommittedDropReceiptsFiltered(runtime, after, bookExemption, false, 99)
		if err != nil || len(rows) != 0 || len(exempt) != 1 || exempt[0].AssetOwner != "character_guild_medal_inventory" {
			t.Fatalf("dedicated pickup blocked settlement or polluted main bag: rows=%v exempt=%v err=%v", rows, exempt, err)
		}
	}
	// A subsequent ordinary material still gets its normal ACK and list0 update.
	conn.write.Reset()
	otherKey := key + 1
	normal := *drop
	normal.ObjectKey = otherKey
	normal.SceneSlot++
	normal.Status = runtimeDungeonDropAvailable
	normal.DestinationSlot = 0
	normal.PickupResponseBody = nil
	normal.PickupItemUpdateBody = nil
	monsters, _ := s.dungeonMonsterCatalog()
	items, _ := monsters.DropCatalog()
	normal.Item, _ = items.ResolveItem(3227)
	if err := runtime.DropOwner.registerBatch([]*runtimeDungeonDrop{&normal}, normal.SceneSlot+1); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(otherKey)); err != nil {
		t.Fatal(err)
	}
	next, tail := splitGameServerUpperPacket(t, conn.write.Bytes())
	nextUpdate, tail := splitGameServerUpperPacket(t, tail)
	if next.Header.MsgID != 39 || nextUpdate.Body[0] != 0 || len(tail) != 0 {
		t.Fatal("ordinary pickup after medal failed")
	}
	// A missing dedicated row or a forged main-bag update is not a settlement proof.
	delete(after.Slots, fmt.Sprintf("38:%d", want))
	if _, _, _, err := currentDungeonCommittedDropReceiptsFiltered(&runtimeDungeonState{DropOwner: &runtimeDungeonDropOwner{byObjectKey: map[uint32]*runtimeDungeonDrop{key: drop}}}, after, false, false, 99); err == nil {
		t.Fatal("missing dedicated item accepted")
	}
}

func TestRealPVFDungeonPickupShiningMedal(t *testing.T) {
	a := real90DropArchive(t)
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for id, ref := range catalog.itemRefs {
		if ref.kind != dungeonDropItemEquipment || !strings.Contains(strings.ToLower(ref.path), "flag/") {
			continue
		}
		def, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(def.Name, "耀眼") {
			continue
		}
		found++
		t.Run(fmt.Sprint(id), func(t *testing.T) { verifyGuildPickupProjection(t, def, 0) })
	}
	if found == 0 {
		t.Fatal("runtime PVF has no shining medal")
	}
}
