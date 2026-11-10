package wedding

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestRoomAssetsPurchasePlaceRemoveAndStaleDestination(t *testing.T) {
	o, now := ownerFixture(t)
	ctx := context.Background()
	req := pairRequest(now)
	_, err := o.Engage(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		state, _ := Read(c)
		state.Phase = Married
		state.LovePoints = 200
		write(&c, state)
		if err = r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	o.Rules.Furniture = map[int64]Furniture{100: {ItemID: 100, Points: 100, Positions: map[uint16]bool{0: true}}, 101: {ItemID: 101, Points: 40, Positions: map[uint16]bool{1: true}}}
	item := r.ItemStack{ItemID: 100, Count: 1, Extra: map[string]string{"grant_identity": "kept"}}
	bought, err := o.BuyFurniture(ctx, req, item)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := Read(bought.Actor)
	if state.LovePoints != 100 || state.RoomPoints != 0 || len(bought.Room.Bag) != 1 {
		t.Fatalf("purchase %+v %+v", state, bought.Room)
	}
	placed, err := o.MoveFurniture(ctx, req, RoomMove{FromBag: true, FromSlot: bought.PurchasedSlot, ToSlot: 0, Count: 1, SourceItemID: 100, ExpectedDestination: 100})
	if err != nil {
		t.Fatal(err)
	}
	x, y, err := ValidatePair(placed.Actor, placed.Partner)
	if err != nil || x.RoomPoints != 100 || y.RoomPoints != 100 || !reflect.DeepEqual(placed.Room.Placed[0], item) {
		t.Fatalf("placement %+v %v", placed, err)
	}
	// A replay cannot move an already removed bag row, nor replace a different
	// destination item or place furniture at a location absent from PVF.
	if _, err = o.MoveFurniture(ctx, req, RoomMove{FromBag: true, FromSlot: bought.PurchasedSlot, ToSlot: 0, Count: 1, SourceItemID: 100, ExpectedDestination: 100}); err == nil {
		t.Fatal("replayed placement accepted")
	}
	if _, err = o.MoveFurniture(ctx, req, RoomMove{FromSlot: 0, ToSlot: 1, Count: 1, SourceItemID: 100}); err == nil {
		t.Fatal("wrong fixture position accepted")
	}
	removed, err := o.MoveFurniture(ctx, req, RoomMove{FromSlot: 0, ToBag: true, ToSlot: 2, Count: 1, SourceItemID: 100, ExpectedDestination: 100})
	if err != nil {
		t.Fatal(err)
	}
	state, _ = Read(removed.Actor)
	if state.RoomPoints != 0 || len(removed.Room.Placed) != 0 || !reflect.DeepEqual(removed.Room.Bag[2], item) {
		t.Fatalf("remove %+v %+v", state, removed.Room)
	}
	room, err := o.ReadRoom(ctx, removed.Actor, removed.Partner)
	if err != nil || !reflect.DeepEqual(room, *removed.Room) {
		t.Fatalf("reload %+v %v", room, err)
	}
}

func TestRoomRemovalIntoOccupiedBagCannotReportSuccessWithoutRemoving(t *testing.T) {
	o, req := reviewMarriedRoomFixture(t)
	ctx := context.Background()
	first, err := o.BuyFurniture(ctx, req, r.ItemStack{ItemID: 100, Count: 1, Extra: map[string]string{"identity": "installed"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := o.BuyFurniture(ctx, req, r.ItemStack{ItemID: 100, Count: 1, Extra: map[string]string{"identity": "bag"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.MoveFurniture(ctx, req, RoomMove{FromBag: true, FromSlot: first.PurchasedSlot, ToSlot: 0, SourceItemID: 100, ExpectedDestination: 100, Count: 1}); err != nil {
		t.Fatal(err)
	}
	scope, _ := r.WeddingPairScope([]string{"1", "2"})
	before, _, err := o.Repositories.Settings.Load(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.MoveFurniture(ctx, req, RoomMove{FromSlot: 0, ToBag: true, ToSlot: second.PurchasedSlot, SourceItemID: 100, ExpectedDestination: 100, Count: 1}); !errors.Is(err, ErrItem) {
		t.Fatalf("occupied one-item bag removal: %v", err)
	}
	after, _, err := o.Repositories.Settings.Load(context.Background(), before.Scope)
	if err != nil || !reflect.DeepEqual(before.Values, after.Values) {
		t.Fatal("rejected removal changed owned item identities", err)
	}
}

func TestRoomFullAndInsufficientPointsDoNotCharge(t *testing.T) {
	o, now := ownerFixture(t)
	ctx := context.Background()
	req := pairRequest(now)
	_, _ = o.Engage(ctx, req)
	o.Rules.Furniture = map[int64]Furniture{100: {ItemID: 100, Points: 100, Gold: 500, Positions: map[uint16]bool{0: true}}}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		state, _ := Read(c)
		state.Phase = Married
		write(&c, state)
		_ = r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding)
	}
	before, _, _ := o.Repositories.Character.Load(ctx, "1")
	if _, err := o.BuyFurniture(ctx, req, r.ItemStack{ItemID: 100, Count: 1}); !errors.Is(err, ErrItem) {
		t.Fatalf("insufficient points %v", err)
	}
	after, _, _ := o.Repositories.Character.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed purchase debited assets")
	}
	scope, _ := r.WeddingPairScope([]string{"1", "2"})
	if _, found, _ := o.Repositories.Settings.Load(ctx, scope); found {
		t.Fatal("failed purchase created shared room")
	}
}

func TestWeddingRoomTransactionRejectsOtherPairScope(t *testing.T) {
	o, _ := ownerFixture(t)
	u := o.Repositories.CharacterTrade.(r.WeddingAssetUnitOfWork)
	scope, _ := r.WeddingPairScope([]string{"1", "3"})
	called := false
	err := u.WithinWeddingAssets(context.Background(), []string{"1", "2"}, scope, func(r.CharacterRepository, r.InventoryRepository, r.SettingsRepository) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatal("cross-pair room accepted")
	}
}
