package wedding

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func marriedLovePointFixture(t *testing.T, balance int64) (Owner, PairRequest, LovePointUseRequest) {
	t.Helper()
	o, now := ownerFixture(t)
	ctx := context.Background()
	req := pairRequest(now)
	if _, err := o.Engage(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		state, _ := Read(c)
		state.Phase, state.LovePoints = Married, balance
		write(&c, state)
		if err := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	o.Rules.DailyLovePoint = 1
	o.Rules.LovePointItems = map[int64]LovePointCoupon{
		10002901:  {ItemID: 10002901, Points: 1, StackLimit: 999, MinimumLevel: 1, Village: true, SeriaRoom: true},
		490702176: {ItemID: 490702176, Points: 520, StackLimit: 999, MinimumLevel: 1, Village: true, SeriaRoom: true},
	}
	if err := o.Repositories.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:65": {ItemID: 10002901, Count: 3, RawEntry: []byte{1, 2, 3}, Extra: map[string]string{"grant_identity": "kept", "raw_entry_hex": "stale"}}}}); err != nil {
		t.Fatal(err)
	}
	return o, req, LovePointUseRequest{Slot: 65, ExpectedItemID: 10002901, Session: strings.Repeat("ab", 32), Sequence: 70000, Slots: itemplacement.SlotPolicy{ListType: r.MainInventoryListType, Start: 65, End: 120, AllowQuickSlots: true}}
}

func TestLovePointCouponAtomicConsumptionAndReplay(t *testing.T) {
	o, req, use := marriedLovePointFixture(t, 0)
	ctx := context.Background()
	first, err := o.ConsumeLovePointCoupon(ctx, req, use)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := Read(first.Actor)
	partner, _ := Read(first.Partner)
	if state.LovePoints != 1 || partner.LovePoints != 0 || first.Remaining != 2 || first.Replayed || !reflect.DeepEqual(first.ChangedSlots["1"], []string{"0:65"}) {
		t.Fatalf("first %+v %+v %+v", first, state, partner)
	}
	stack := first.Inventories["1"].Slots["0:65"]
	if stack.Count != 2 || stack.Extra["grant_identity"] != "kept" || stack.RawEntry != nil || stack.Extra["raw_entry_hex"] != "" {
		t.Fatalf("remaining identity %+v", stack)
	}
	replay, err := o.ConsumeLovePointCoupon(ctx, req, use)
	if err != nil || !replay.Replayed || replay.Remaining != 2 {
		t.Fatalf("replay %+v %v", replay, err)
	}
	state, _ = Read(replay.Actor)
	if state.LovePoints != 1 {
		t.Fatal("replay granted another point")
	}
	use.Sequence++
	second, err := o.ConsumeLovePointCoupon(ctx, req, use)
	if err != nil || second.Remaining != 1 {
		t.Fatalf("second %+v %v", second, err)
	}
	use.Sequence--
	if _, err = o.ConsumeLovePointCoupon(ctx, req, use); !errors.Is(err, ErrReplay) {
		t.Fatalf("old sequence: %v", err)
	}
	use.Sequence++
	use.Slot = 66
	if _, err = o.ConsumeLovePointCoupon(ctx, req, use); !errors.Is(err, ErrReplay) {
		t.Fatalf("same sequence different source: %v", err)
	}
}

func TestLovePointCouponLargeGiftFitsExactlyAndNeverTruncates(t *testing.T) {
	for _, balance := range []int64{479, 480} {
		t.Run(strconv.FormatInt(balance, 10), func(t *testing.T) {
			o, req, use := marriedLovePointFixture(t, balance)
			ctx := context.Background()
			use.ExpectedItemID = 490702176
			inventory, _, _ := o.Repositories.Inventory.Load(ctx, "1")
			inventory.Slots["0:65"] = r.ItemStack{ItemID: 490702176, Count: 1}
			_ = o.Repositories.Inventory.Save(ctx, inventory)
			before, _, _ := o.Repositories.Character.Load(ctx, "1")
			got, err := o.ConsumeLovePointCoupon(ctx, req, use)
			if balance == 480 {
				if !errors.Is(err, ErrItem) {
					t.Fatalf("overflow accepted %+v %v", got, err)
				}
				after, _, _ := o.Repositories.Character.Load(ctx, "1")
				remaining, _, _ := o.Repositories.Inventory.Load(ctx, "1")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inventory, remaining) {
					t.Fatal("overflow charged or partially granted")
				}
				return
			}
			state, _ := Read(got.Actor)
			if err != nil || state.LovePoints != 999 || got.Remaining != 0 {
				t.Fatalf("exact fit %+v %v", got, err)
			}
			if _, present := got.Inventories["1"].Slots["0:65"]; present {
				t.Fatal("empty stack persisted")
			}
			if replay, err := o.ConsumeLovePointCoupon(ctx, req, use); err != nil || !replay.Replayed {
				t.Fatalf("last-item replay %+v %v", replay, err)
			}
		})
	}
}

func TestLovePointCouponRejectsInvalidSourceAndRollsBack(t *testing.T) {
	for _, name := range []string{"locked", "expired", "extra_expired", "bad_expiry", "other_item", "excessive_stack", "closed_slot", "wrong_owner", "generation_changed", "not_married"} {
		t.Run(name, func(t *testing.T) {
			o, req, use := marriedLovePointFixture(t, 0)
			ctx := context.Background()
			inventory, _, _ := o.Repositories.Inventory.Load(ctx, "1")
			stack := inventory.Slots["0:65"]
			switch name {
			case "locked":
				stack.Extra["equipment_lock_state"] = "locked"
			case "expired":
				stack.ExpireAt = req.Now
			case "extra_expired":
				stack.Extra["expire_unix"] = strconv.FormatInt(req.Now.Unix()-1, 10)
			case "bad_expiry":
				stack.Extra["expire_time"] = "broken"
			case "other_item":
				stack.ItemID = 10002611
			case "excessive_stack":
				stack.Count = 1000
			case "closed_slot":
				use.Slot = 97
				inventory.Slots["0:97"] = stack
				delete(inventory.Slots, "0:65")
			case "wrong_owner":
				req.Actor.AccountID = "foreign"
			case "generation_changed":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls == 1 }
			case "not_married":
				for _, id := range []string{"1", "2"} {
					c, _, _ := o.Repositories.Character.Load(ctx, id)
					state, _ := Read(c)
					state.Phase = Engaged
					write(&c, state)
					_ = r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding)
				}
			}
			if name != "closed_slot" {
				inventory.Slots["0:65"] = stack
			}
			_ = o.Repositories.Inventory.Save(ctx, inventory)
			before, _, _ := o.Repositories.Character.Load(ctx, "1")
			if _, err := o.ConsumeLovePointCoupon(ctx, req, use); err == nil {
				t.Fatal("invalid source accepted")
			}
			after, _, _ := o.Repositories.Character.Load(ctx, "1")
			remaining, _, _ := o.Repositories.Inventory.Load(ctx, "1")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inventory, remaining) {
				t.Fatal("failed use changed durable assets")
			}
		})
	}
}

func TestLovePointCouponConcurrentReplayChargesOnce(t *testing.T) {
	o, req, use := marriedLovePointFixture(t, 0)
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := o.ConsumeLovePointCoupon(context.Background(), req, use)
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ := o.Repositories.Character.Load(context.Background(), "1")
	state, _ := Read(c)
	inv, _, _ := o.Repositories.Inventory.Load(context.Background(), "1")
	if state.LovePoints != 1 || inv.Slots["0:65"].Count != 2 {
		t.Fatalf("concurrent replay balance=%d inventory=%+v", state.LovePoints, inv)
	}
}

func TestDailyLovePointsArePerCharacterAndOncePerServiceDay(t *testing.T) {
	o, req, _ := marriedLovePointFixture(t, 998)
	ctx := context.Background()
	first, err := o.DailyLovePoints(ctx, req, 20758)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := Read(first.Actor)
	other, _ := Read(first.Partner)
	if state.LovePoints != 999 || other.LovePoints != 998 {
		t.Fatalf("daily balance %d/%d", state.LovePoints, other.LovePoints)
	}
	if _, err = o.DailyLovePoints(ctx, req, 20758); err != nil {
		t.Fatal(err)
	}
	if _, err = o.DailyLovePoints(ctx, req, 20759); err != nil {
		t.Fatal(err)
	}
	c, _, _ := o.Repositories.Character.Load(ctx, "1")
	state, _ = Read(c)
	if state.LovePoints != 999 || state.LastLoveDay != 20759 {
		t.Fatalf("daily cap %+v", state)
	}
	// The partner's own login still earns its own daily point.
	req.Actor, req.Partner = req.Partner, req.Actor
	partner, err := o.DailyLovePoints(ctx, req, 20759)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = Read(partner.Actor)
	if state.LovePoints != 999 {
		t.Fatalf("partner daily %+v", state)
	}
}

type lovePointTestSource map[string]string

func (s lovePointTestSource) ReadText(path string) (string, error) {
	if text, ok := s[path]; ok {
		return text, nil
	}
	return "", fmt.Errorf("missing %s", path)
}

func TestLovePointRulesRequireRealCouponDefinitions(t *testing.T) {
	base := lovePointTestSource{
		"stackable/stackable.lst": "10002901 `coupon.stk`\n490702176 `large.stk`\n",
		"stackable/coupon.stk":    "[minimum level]\n1\n[stack limit]\n999\n[stackable type]\n`[waste]` 0\n[action type]\n`[love point coupon]` 1\n[action usable place]\n`[village]` `[seria room]`\n",
	}
	base["stackable/large.stk"] = base["stackable/coupon.stk"]
	for _, row := range []struct {
		name, system, alter string
		valid               bool
	}{
		{"valid", "10002901 1 490702176 520", "", true},
		{"odd", "10002901 1 490702176", "", false},
		{"duplicate", "10002901 1 10002901 520", "", false},
		{"over_max", "10002901 1000", "", false},
		{"missing", "999 1", "", false},
		{"wrong_action", "10002901 1", "[action type]\n`[upgrade wedding ring]` 1\n", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			source := lovePointTestSource{}
			for path, text := range base {
				source[path] = text
			}
			if row.alter != "" {
				source["stackable/coupon.stk"] = row.alter
			}
			doc, err := pvf.Parse(SystemPath, "[love point item]\n"+row.system+"\n")
			if err != nil {
				t.Fatal(err)
			}
			got, err := loadLovePointItems(source, doc, 999)
			if row.valid {
				if err != nil || got[10002901].Points != 1 || got[490702176].Points != 520 || !got[10002901].Village || !got[10002901].SeriaRoom {
					t.Fatalf("rules %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("invalid coupon table accepted")
			}
		})
	}
}
