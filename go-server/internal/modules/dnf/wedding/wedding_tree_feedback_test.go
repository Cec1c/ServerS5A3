package wedding

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestWeddingTreeWaterRejectionReasonsPreserveAllAssets(t *testing.T) {
	for _, name := range []string{"missing", "expired", "mature", "quota", "wrong-item"} {
		t.Run(name, func(t *testing.T) {
			o, req, rules := extensionFixture(t)
			ctx := context.Background()
			scope, _ := r.WeddingPairScope([]string{"1", "2"})
			tree := TreeState{Index: 1, Expires: req.Now.Unix() + 3600, Growth: 20, WaterDay: 1, WaterCount: 1, PlantSerial: 1}
			cmd := extensionTreeCommand(1049, 1)
			cmd.Slot, cmd.ItemID = 66, 10002765
			want, category := ErrTreeWaterQuota, ErrState
			switch name {
			case "missing":
				tree = TreeState{}
				want, category = ErrTreeMissing, ErrItem
			case "expired":
				tree.Expires = req.Now.Unix()
				want, category = ErrTreeExpired, ErrItem
			case "mature":
				tree.Growth = 30
				want, category = ErrTreeMature, ErrItem
			case "quota":
				tree.WaterCount = 2
			case "wrong-item":
				cmd.ItemID = 10002766
				want, category = ErrItem, ErrItem
			}
			room := Room{Version: 1, Tree: tree, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}}
			if err := saveExtensionTestRoom(ctx, o.Repositories.Settings, scope, room); err != nil {
				t.Fatal(err)
			}
			beforeRoom, _, _ := o.Repositories.Settings.Load(ctx, scope)
			beforeChar, _, _ := o.Repositories.Character.Load(ctx, "1")
			beforeBag, _, _ := o.Repositories.Inventory.Load(ctx, "1")
			for attempt := 0; attempt < 2; attempt++ {
				_, err := o.MutateTree(ctx, req, rules, cmd)
				if !errors.Is(err, want) || !errors.Is(err, category) {
					t.Fatalf("reason=%v want=%v category=%v", err, want, category)
				}
				afterRoom, _, _ := o.Repositories.Settings.Load(ctx, scope)
				afterChar, _, _ := o.Repositories.Character.Load(ctx, "1")
				afterBag, _, _ := o.Repositories.Inventory.Load(ctx, "1")
				if !reflect.DeepEqual(beforeRoom, afterRoom) || !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeBag, afterBag) {
					t.Fatal("rejected watering changed tree, receipt, or assets")
				}
			}
		})
	}
}
