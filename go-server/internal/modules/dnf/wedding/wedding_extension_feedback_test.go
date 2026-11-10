package wedding

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestWeddingExtensionFeedbackBusinessReasonsAreAtomic(t *testing.T) {
	for _, name := range []string{"plant-occupied", "harvest-missing", "harvest-immature", "harvest-expired", "clear-missing", "capsule-energy", "capsule-own-limit", "capsule-empty", "capsule-partner-limit"} {
		t.Run(name, func(t *testing.T) {
			o, req, rules := extensionFixture(t)
			ctx := context.Background()
			scope, _ := r.WeddingPairScope([]string{"1", "2"})
			tree := TreeState{Index: 1, Expires: req.Now.Unix() + 3600, Growth: 20, WaterDay: 1, WaterCount: 2, PlantSerial: 1}
			op, want, category := uint16(1050), ErrTreeNotMature, ErrState
			own, partner := CapsuleState{}, CapsuleState{}
			switch name {
			case "plant-occupied":
				op, want = 1048, ErrTreeOccupied
			case "harvest-missing":
				tree, want = TreeState{}, ErrTreeMissing
			case "harvest-expired":
				tree.Expires, want = req.Now.Unix(), ErrTreeExpired
			case "clear-missing":
				tree, op, want = TreeState{}, 1051, ErrTreeMissing
			case "capsule-energy":
				op, want, category = 1052, ErrCapsuleEnergy, ErrItem
			case "capsule-own-limit":
				op, want, category = 1052, ErrCapsuleOwnLimit, ErrItem
				own.Progress, own.Own = rules.Capsule.MaximumGauge, rules.Capsule.MaximumOwn
			case "capsule-empty":
				op, want, category = 1053, ErrCapsuleEmpty, ErrItem
			case "capsule-partner-limit":
				op, want, category = 1053, ErrCapsulePartnerLimit, ErrItem
				own.Own, partner.Taken = 1, rules.Capsule.MaximumTaken
			}
			room := Room{Version: 1, Tree: tree, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}}
			if err := saveExtensionTestRoom(ctx, o.Repositories.Settings, scope, room); err != nil {
				t.Fatal(err)
			}
			for id, state := range map[string]CapsuleState{"1": own, "2": partner} {
				c, _, _ := o.Repositories.Character.Load(ctx, id)
				if err := storeCapsule(&c, state); err != nil {
					t.Fatal(err)
				}
				if err := r.SaveCharacterFields(ctx, o.Repositories.Character, c, r.CharacterFieldWedding); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() []any {
				room, _, _ := o.Repositories.Settings.Load(ctx, scope)
				a, _, _ := o.Repositories.Character.Load(ctx, "1")
				b, _, _ := o.Repositories.Character.Load(ctx, "2")
				ai, _, _ := o.Repositories.Inventory.Load(ctx, "1")
				bi, _, _ := o.Repositories.Inventory.Load(ctx, "2")
				return []any{room, a, b, ai, bi}
			}
			before := snapshot()
			for attempt := 0; attempt < 2; attempt++ {
				var err error
				if op <= 1051 {
					cmd := extensionTreeCommand(op, 1)
					cmd.Slot, cmd.ItemID = 65, 10002766
					_, err = o.MutateTree(ctx, req, rules, cmd)
				} else {
					_, err = o.MutateCapsule(ctx, req, rules.Capsule, CapsuleCommand{Opcode: op, Receipt: extensionStamp(op, 1)})
				}
				if !errors.Is(err, want) || !errors.Is(err, category) {
					t.Fatalf("got %v, want %v/%v", err, want, category)
				}
				if !reflect.DeepEqual(before, snapshot()) {
					t.Fatal("failure changed either spouse, inventory, tree or receipt")
				}
			}
		})
	}
}

func TestWeddingTreeExpiredClearPreservesQuotaAndSerials(t *testing.T) {
	o, req, rules := extensionFixture(t)
	ctx := context.Background()
	scope, _ := r.WeddingPairScope([]string{"1", "2"})
	room := Room{Version: 1, Tree: TreeState{Index: 1, Expires: req.Now.Unix(), Growth: 20, WaterDay: 1, WaterCount: 2, PlantSerial: 7, HarvestSerial: 2}, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}}
	if err := saveExtensionTestRoom(ctx, o.Repositories.Settings, scope, room); err != nil {
		t.Fatal(err)
	}
	cmd := extensionTreeCommand(1051, 1)
	result, err := o.MutateTree(ctx, req, rules, cmd)
	if err != nil {
		t.Fatal("expired tree cannot be cleared", err)
	}
	want := TreeState{WaterDay: 1, WaterCount: 2, PlantSerial: 7, HarvestSerial: 2}
	if result.Room == nil || result.Room.Tree != want {
		t.Fatalf("clear erased water quota or serials: %+v", result.Room)
	}
	result, err = o.MutateTree(ctx, req, rules, cmd)
	if err != nil || !result.Replayed || result.Room.Tree != want {
		t.Fatal("replayed clear changed state", err)
	}
}
