package quest

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

func TestSingleQuestCouponNativeSlotGrantsOnlyExperienceAtomically(t *testing.T) {
	for _, scenario := range []string{"success", "wrong_item", "under_level", "expired", "not_active", "other_grade", "question", "class_change", "quest_failure", "inventory_failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			g := finishOwnerSeed(t, ctx)
			catalog := finishOwnerCatalog()
			d := catalog.byID[3145]
			d.Grade = "[epic]"
			d.Job = "[all]"
			in := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
			in.ExpectedCompletionKey = ""
			coupon := UseClearCouponInput{AccountID: "acc", CharacterID: "19", Slot: 87, Now: in.CommittedAt, Coupon: ClearCoupon{ItemID: 900, Kind: CouponSingleQuest, MinimumLevel: 1, MaximumLevel: 90}}
			inv, _, _ := g.Inventory.Load(ctx, "19")
			if inv.Slots == nil {
				inv.Slots = map[string]repo.ItemStack{}
			}
			inv.Slots["0:87"] = repo.ItemStack{ItemID: 900, Count: 2}
			q, _, _ := g.Quest.Load(ctx, "19")
			q.States[3145] = repo.QuestState{Status: "active", ProgressValue: 99}
			switch scenario {
			case "wrong_item":
				coupon.Coupon.ItemID = 901
			case "under_level":
				coupon.Coupon.MinimumLevel = 2
			case "expired":
				v := inv.Slots["0:87"]
				v.ExpireAt = in.CommittedAt
				inv.Slots["0:87"] = v
			case "not_active":
				q.States[3145] = repo.QuestState{Status: "completed"}
			case "other_grade":
				d.Grade = "[side]"
			case "question":
				d.Type = "[question]"
			case "class_change":
				d.JobChangeQuest = 1
			}
			catalog.byID[d.ID] = d
			catalog.ordered = []Definition{d}
			g.Inventory.Save(ctx, inv)
			g.Quest.Save(ctx, q)
			before, _, _ := g.Character.Load(ctx, "19")
			sk, _, _ := g.Skill.Load(ctx, "19")
			injected := g
			if scenario == "inventory_failure" || scenario == "quest_failure" {
				injected.CharacterSettlement = couponFailUOW{g.CharacterSettlement, scenario}
			}
			in.SingleQuestCoupon = &coupon
			owner, _ := NewOwner(injected)
			r, err := owner.ApplyFinishSettlement(ctx, catalog, in)
			after, _, _ := g.Character.Load(ctx, "19")
			ai, _, _ := g.Inventory.Load(ctx, "19")
			aq, _, _ := g.Quest.Load(ctx, "19")
			ask, _, _ := g.Skill.Load(ctx, "19")
			if scenario != "success" {
				if err == nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inv, ai) || !reflect.DeepEqual(q, aq) || !reflect.DeepEqual(sk, ask) {
					t.Fatalf("rejection not atomic: %s %v", scenario, err)
				}
				return
			}
			if err != nil || !r.AtomicCommitted || r.ExperienceDelta == 0 || len(r.Items) != 0 || len(r.Currency) != 0 || len(r.ConsumedItems) != 1 || r.ConsumedItems[0].SlotIndex != 87 || ai.Slots["0:87"].Count != 1 || len(ai.Slots) != 1 || after.Stats["gold"] != before.Stats["gold"] || aq.States[3145].Status != "completed" {
				t.Fatalf("invalid single coupon result: %+v %v", r, err)
			}
			_, err = owner.ApplyFinishSettlement(ctx, catalog, in)
			again, _, _ := g.Inventory.Load(ctx, "19")
			ac, _, _ := g.Character.Load(ctx, "19")
			if err == nil || !reflect.DeepEqual(ai, again) || !reflect.DeepEqual(after, ac) {
				t.Fatal("duplicate consumed or granted again")
			}
		})
	}
}
