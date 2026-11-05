package wedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func reviewMarriedRoomFixture(t *testing.T) (Owner, PairRequest) {
	t.Helper()
	o, now := ownerFixture(t)
	req := pairRequest(now)
	if _, err := o.Engage(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		c, _, err := o.Repositories.Character.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		state, err := Read(c)
		if err != nil {
			t.Fatal(err)
		}
		state.Phase, state.LovePoints = Married, 800
		write(&c, state)
		if err = r.SaveCharacterFields(context.Background(), o.Repositories.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	o.Rules.Furniture = map[int64]Furniture{
		100: {ItemID: 100, Points: 10, Gold: 11, Positions: map[uint16]bool{0: true, 1: true}},
		101: {ItemID: 101, Points: 20, Gold: 13, Positions: map[uint16]bool{0: true, 1: true}},
	}
	return o, req
}

func reviewSaveRoom(t *testing.T, o Owner, room Room) r.SettingsRecord {
	t.Helper()
	ctx := context.Background()
	scope, err := r.WeddingPairScope([]string{"1", "2"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(room)
	if err != nil {
		t.Fatal(err)
	}
	record := r.SettingsRecord{Scope: scope, Values: map[string]string{"room": string(encoded)}}
	if err = o.Repositories.Settings.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestWeddingReviewFullSharedInventoryDoesNotDebitEitherOwner(t *testing.T) {
	o, req := reviewMarriedRoomFixture(t)
	ctx := context.Background()
	for i := 0; i < RoomInventoryCapacity; i++ {
		result, err := o.BuyFurniture(ctx, req, r.ItemStack{ItemID: 100, Count: 1})
		if err != nil || result.PurchasedSlot != uint16(i) || len(result.Room.Bag) != i+1 {
			t.Fatalf("purchase %d: slot=%d err=%v", i, result.PurchasedSlot, err)
		}
	}
	a, _, _ := o.Repositories.Character.Load(ctx, "1")
	b, _, _ := o.Repositories.Character.Load(ctx, "2")
	scope, _ := r.WeddingPairScope([]string{"1", "2"})
	roomBefore, _, _ := o.Repositories.Settings.Load(ctx, scope)
	if _, err := o.BuyFurniture(ctx, req, r.ItemStack{ItemID: 100, Count: 1}); !errors.Is(err, ErrItem) {
		t.Fatalf("full shared inventory: %v", err)
	}
	afterA, _, _ := o.Repositories.Character.Load(ctx, "1")
	afterB, _, _ := o.Repositories.Character.Load(ctx, "2")
	roomAfter, _, _ := o.Repositories.Settings.Load(ctx, scope)
	if !reflect.DeepEqual(a, afterA) || !reflect.DeepEqual(b, afterB) || !reflect.DeepEqual(roomBefore, roomAfter) {
		t.Fatal("full room purchase changed balance or shared assets")
	}
	state, _ := Read(a)
	if state.LovePoints != 400 || state.RoomPoints != 0 {
		t.Fatalf("unplaced purchases earned room score or wrong cost: %+v", state)
	}
}

func TestWeddingReviewConcurrentPlacementConsumesOneOwnedFurniture(t *testing.T) {
	o, req := reviewMarriedRoomFixture(t)
	ctx := context.Background()
	if _, err := o.BuyFurniture(ctx, req, r.ItemStack{ItemID: 100, Count: 1}); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.MoveFurniture(ctx, req, RoomMove{FromBag: true, FromSlot: 0, ToSlot: 0, Count: 1, SourceItemID: 100, ExpectedDestination: 100}); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	a, _, _ := o.Repositories.Character.Load(ctx, "1")
	b, _, _ := o.Repositories.Character.Load(ctx, "2")
	room, err := o.ReadRoom(ctx, a, b)
	x, y, pairErr := ValidatePair(a, b)
	if successes.Load() != 1 || err != nil || pairErr != nil || len(room.Bag) != 0 || len(room.Placed) != 1 || x.RoomPoints != 10 || y.RoomPoints != 10 {
		t.Fatalf("competing placement: successes=%d room=%+v pair=%+v/%+v errors=%v/%v", successes.Load(), room, x, y, err, pairErr)
	}
}

func TestWeddingReviewPlacementRejectsInvalidSourceOrSwappedDestination(t *testing.T) {
	for _, target := range []string{"source", "destination"} {
		for _, invalid := range []string{"locked", "deadline", "expire_time", "expire_unix", "malformed"} {
			t.Run(target+"/"+invalid, func(t *testing.T) {
				o, req := reviewMarriedRoomFixture(t)
				room := Room{Version: 1, Bag: map[uint16]r.ItemStack{
					0: {ItemID: 100, Count: 1}, 1: {ItemID: 101, Count: 1},
				}, Placed: map[uint16]r.ItemStack{}}
				slot := uint16(0)
				if target == "destination" {
					slot = 1
				}
				stack := room.Bag[slot]
				stack.Extra = map[string]string{}
				switch invalid {
				case "locked":
					stack.Extra["equipment_lock_state"] = "locked"
				case "deadline":
					stack.ExpireAt = req.Now
				case "expire_time", "expire_unix":
					stack.Extra[invalid] = strconv.FormatInt(req.Now.Unix()-1, 10)
				case "malformed":
					stack.Extra["expire_time"] = "invalid deadline"
				}
				room.Bag[slot] = stack
				before := reviewSaveRoom(t, o, room)
				if _, err := o.MoveFurniture(context.Background(), req, RoomMove{FromBag: true, ToBag: true, FromSlot: 0, ToSlot: 1, Count: 1, SourceItemID: 100, ExpectedDestination: 101}); !errors.Is(err, ErrItem) {
					t.Fatalf("invalid %s %s accepted: %v", target, invalid, err)
				}
				after, _, _ := o.Repositories.Settings.Load(context.Background(), before.Scope)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected swap mutated shared metadata")
				}
			})
		}
	}
}

func TestWeddingReviewSwapKeepsEveryOwnedItemMetadata(t *testing.T) {
	o, req := reviewMarriedRoomFixture(t)
	room := Room{Version: 1, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}}
	for slot := uint16(0); slot < 2; slot++ {
		room.Bag[slot] = r.ItemStack{ItemID: 100 + int64(slot), Count: 1, Bind: slot == 0, RawEntry: []byte{byte(slot), 0xf4}, ExpireAt: req.Now.Add(time.Hour), Extra: map[string]string{"identity": fmt.Sprint(slot), "expire_time": strconv.FormatInt(req.Now.Add(2*time.Hour).Unix(), 10)}}
	}
	reviewSaveRoom(t, o, room)
	result, err := o.MoveFurniture(context.Background(), req, RoomMove{FromBag: true, ToBag: true, FromSlot: 0, ToSlot: 1, Count: 1, SourceItemID: 100, ExpectedDestination: 101})
	if err != nil || !reflect.DeepEqual(result.Room.Bag[0], room.Bag[1]) || !reflect.DeepEqual(result.Room.Bag[1], room.Bag[0]) {
		t.Fatalf("metadata changed during swap: %+v %v", result.Room, err)
	}
}
