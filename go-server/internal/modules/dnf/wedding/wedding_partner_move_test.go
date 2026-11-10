package wedding

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func partnerMoveFixture(t *testing.T) (Owner, PairRequest, PartnerMoveRules, PartnerMoveCommand) {
	t.Helper()
	o, req, _ := extensionFixture(t)
	ctx := context.Background()
	for _, id := range []string{"1", "2"} {
		c, _, _ := o.Repositories.Character.Load(ctx, id)
		c.Stats["town_id"], c.Stats["area_id"] = 38, 1
		if id == "2" {
			c.Stats["town_id"], c.Stats["area_id"] = 39, 0
		}
		c.Stats["pos_x"], c.Stats["pos_y"] = 400, 200
		if e := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldStats); e != nil {
			t.Fatal(e)
		}
	}
	if e := o.Repositories.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:65": {ItemID: PartnerMoveItemID, Count: 2, Extra: map[string]string{"identity": "keep"}}}}); e != nil {
		t.Fatal(e)
	}
	req.Revalidate = func() bool { return true }
	rule := PartnerMoveRules{ItemID: PartnerMoveItemID, StackLimit: 1000, CooldownMilliseconds: 1000, MinimumLevel: 1, Village: true, Seria: true}
	command := PartnerMoveCommand{Slot: 65, FromTown: 38, FromArea: 1, Destination: PartnerMoveLocation{Town: 39, Area: 0, X: 700, Y: 250, Direction: 5}, Slots: itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 120}, Admit: func(r.Group, r.CharacterRecord, PartnerMoveLocation) error { return nil }}
	return o, req, rule, command
}
func TestWeddingPartnerMoveAtomicLocationCooldownAndVoucherConsumption(t *testing.T) {
	o, req, rules, command := partnerMoveFixture(t)
	ctx := context.Background()
	result, e := o.MoveToPartner(ctx, req, rules, command)
	if e != nil {
		t.Fatal(e)
	}
	if result.Actor.Stats["town_id"] != 39 || result.Actor.Stats["area_id"] != 0 || result.Actor.Stats["pos_x"] != 700 || result.Actor.Stats[statPrefix+"partner_move_at_ms"] != req.Now.UnixMilli() {
		t.Fatal(result.Actor.Stats)
	}
	if stack := result.Inventories["1"].Slots["0:65"]; stack.Count != 1 || stack.ItemID != PartnerMoveItemID || stack.Extra["identity"] != "keep" || !reflect.DeepEqual(result.ChangedSlots["1"], []string{"0:65"}) {
		t.Fatalf("ordinary waste must consume exactly one and retain metadata: %+v", stack)
	}
	command.FromTown, command.FromArea = 39, 0
	if _, e = o.MoveToPartner(ctx, req, rules, command); e == nil {
		t.Fatal("cooldown bypass")
	}
	bag, _, _ := o.Repositories.Inventory.Load(ctx, "1")
	if bag.Slots["0:65"].Count != 1 {
		t.Fatal("rejected cooldown consumed another ticket")
	}
	req.Now = req.Now.Add(time.Second)
	if _, e = o.MoveToPartner(ctx, req, rules, command); e != nil {
		t.Fatal("PVF cooldown elapsed", e)
	}
	bag, _, _ = o.Repositories.Inventory.Load(ctx, "1")
	if _, exists := bag.Slots["0:65"]; exists {
		t.Fatal("final ticket must clear source slot")
	}
}
func TestWeddingPartnerMoveRejectsChangedSceneLockedVoucherAndAdmission(t *testing.T) {
	for _, scenario := range []string{"retired", "locked", "expired", "missing", "closed", "quest", "over-limit"} {
		t.Run(scenario, func(t *testing.T) {
			o, req, rules, command := partnerMoveFixture(t)
			ctx := context.Background()
			calls := 0
			if scenario == "retired" {
				req.Revalidate = func() bool { calls++; return calls == 1 }
			}
			if scenario == "quest" {
				command.Admit = func(r.Group, r.CharacterRecord, PartnerMoveLocation) error { return ErrQuest }
			}
			bag, _, _ := o.Repositories.Inventory.Load(ctx, "1")
			stack := bag.Slots["0:65"]
			switch scenario {
			case "locked":
				stack.Extra["equipment_lock_state"] = "locked"
			case "expired":
				stack.ExpireAt = req.Now
			case "missing":
				stack.ItemID = 100
			case "closed":
				command.Slot = 120
				delete(bag.Slots, "0:65")
			case "over-limit":
				stack.Count = rules.StackLimit + 1
			}
			bag.Slots["0:"+map[bool]string{true: "120", false: "65"}[scenario == "closed"]] = stack
			if e := o.Repositories.Inventory.Save(ctx, bag); e != nil {
				t.Fatal(e)
			}
			before, _, _ := o.Repositories.Character.Load(ctx, "1")
			beforeBag, _, _ := o.Repositories.Inventory.Load(ctx, "1")
			if _, e := o.MoveToPartner(ctx, req, rules, command); e == nil {
				t.Fatal("invalid teleport accepted")
			}
			after, _, _ := o.Repositories.Character.Load(ctx, "1")
			afterBag, _, _ := o.Repositories.Inventory.Load(ctx, "1")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeBag, afterBag) {
				t.Fatal("partial voucher/cooldown/location on rejection")
			}
		})
	}
}

func TestWeddingPartnerMoveMaximumStackConsumesOne(t *testing.T) {
	o, req, rules, command := partnerMoveFixture(t)
	ctx := context.Background()
	bag, _, _ := o.Repositories.Inventory.Load(ctx, "1")
	stack := bag.Slots["0:65"]
	stack.Count = rules.StackLimit
	bag.Slots["0:65"] = stack
	if err := o.Repositories.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	result, err := o.MoveToPartner(ctx, req, rules, command)
	if err != nil || result.Inventories["1"].Slots["0:65"].Count != rules.StackLimit-1 {
		t.Fatalf("max stack %+v %v", result.Inventories, err)
	}
}
