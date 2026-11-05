package wedding

import (
	"context"
	"os"
	"reflect"
	"sort"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestWeddingRoomCurrentPVFAllFurniturePlacesRemovesAndRejectsWrongPositions(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("requires paired PVF")
	}
	source, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(rules.Furniture))
	for id := range rules.Furniture {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	ctx := context.Background()
	tested := 0
	for _, id := range ids {
		f := rules.Furniture[id]
		o, req := reviewMarriedRoomFixture(t)
		o.Rules = rules
		item := r.ItemStack{ItemID: id, Count: 1, Extra: map[string]string{"identity": "owned-pvf-furniture"}}
		before := Room{Version: 1, Bag: map[uint16]r.ItemStack{0: item}, Placed: map[uint16]r.ItemStack{}}
		reviewSaveRoom(t, o, before)
		aBefore, _, _ := o.Repositories.Character.Load(ctx, "1")
		positions := make([]int, 0, len(f.Positions))
		for slot := range f.Positions {
			positions = append(positions, int(slot))
		}
		sort.Ints(positions)
		for _, position := range positions {
			if position >= RoomPlacementCapacity {
				t.Fatalf("PVF item=%d position=%d exceeds native room capacity", id, position)
			}
			placed, err := o.MoveFurniture(ctx, req, RoomMove{FromBag: true, FromSlot: 0, ToSlot: uint16(position), SourceItemID: id, ExpectedDestination: id, Count: 1})
			if err != nil {
				t.Fatalf("place item=%d slot=%d: %v", id, position, err)
			}
			x, y, err := ValidatePair(placed.Actor, placed.Partner)
			if err != nil || x.RoomPoints != f.Points || y.RoomPoints != f.Points || !reflect.DeepEqual(placed.Room.Placed[uint16(position)], item) {
				t.Fatalf("item=%d bonus/identity: %+v/%+v %v", id, x, y, err)
			}
			removed, err := o.MoveFurniture(ctx, req, RoomMove{FromSlot: uint16(position), ToBag: true, ToSlot: 0, SourceItemID: id, ExpectedDestination: id, Count: 1})
			if err != nil {
				t.Fatalf("remove item=%d slot=%d: %v", id, position, err)
			}
			x, y, err = ValidatePair(removed.Actor, removed.Partner)
			if err != nil || x.RoomPoints != 0 || y.RoomPoints != 0 || len(removed.Room.Placed) != 0 || !reflect.DeepEqual(removed.Room.Bag[0], item) {
				t.Fatalf("remove item=%d changed assets/bonus %v", id, err)
			}
			if removed.Actor.Stats["gold"] != aBefore.Stats["gold"] || removed.Actor.Stats["wedding_v1_love_points"] != aBefore.Stats["wedding_v1_love_points"] {
				t.Fatal("moving furniture charged twice")
			}
			tested++
		}
		for position := uint16(0); position < RoomPlacementCapacity; position++ {
			if f.Positions[position] {
				continue
			}
			if _, err := o.MoveFurniture(ctx, req, RoomMove{FromBag: true, ToSlot: position, SourceItemID: id, ExpectedDestination: id, Count: 1}); err == nil {
				t.Fatalf("invalid PVF position accepted item=%d slot=%d", id, position)
			}
			a, _, _ := o.Repositories.Character.Load(ctx, "1")
			b, _, _ := o.Repositories.Character.Load(ctx, "2")
			room, err := o.ReadRoom(ctx, a, b)
			if err != nil || !reflect.DeepEqual(room.Bag, before.Bag) || len(room.Placed) != 0 {
				t.Fatal("invalid position consumed furniture", id, err)
			}
			break
		}
	}
	if len(ids) == 0 || tested == 0 {
		t.Fatal("no furniture checked")
	}
	t.Logf("current-PVF templates=%d allowed-position moves=%d; each placed, removed and both spouses' score checked", len(ids), tested)
}
