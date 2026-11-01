package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
