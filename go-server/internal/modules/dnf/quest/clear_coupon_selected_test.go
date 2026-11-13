package quest

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
	"time"
)

func TestNamedQuestCouponUnlockIsAtomicScopedAndIdempotent(t *testing.T) {
	for _, scenario := range []string{"support", "magic", "earring", "under_level", "foreign", "question", "unknown_quest", "inventory_failure", "quest_failure", "already_done", "repair_missing_bit", "expired", "renewed_absolute"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
			index, level := int64(0), 60
			if scenario == "magic" {
				index, level = 1, 65
			}
			if scenario == "earring" {
				index, level = 2, 90
			}
			first := Definition{ID: 674, Grade: "[side]", Type: "[hunt monster]", LevelMin: level, LevelMax: 99, Job: "[all]", RewardType: "[item]"}
			last := Definition{ID: 649, Grade: "[side]", Type: "[seeking]", LevelMin: level, LevelMax: 99, Job: "[all]", PreRequiredGroups: [][]int64{{674}}, RewardType: "[slot expansion]", RewardIntData: []int64{index}}
			unrelated := last
			unrelated.ID = 650
			if scenario == "question" {
				first.Type = "[question]"
			}
			catalog := &Catalog{ordered: []Definition{last, unrelated, first}, byID: map[int64]Definition{674: first, 649: last, 650: unrelated}}
			c := repo.CharacterRecord{CharacterID: "1", AccountID: "a", Job: "0", Level: level, Stats: map[string]int64{"grow_type": 1, "gold": 100, "exp": 123, "ex_equip_slot_stat": 8}}
			inv := repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 900, Count: 2}}}
			q := repo.QuestRecord{CharacterID: "1", States: map[int64]repo.QuestState{650: {Status: "active", ProgressValue: 3}}}
			in := UseClearCouponInput{AccountID: "a", CharacterID: "1", Slot: 87, Now: now, Coupon: ClearCoupon{ItemID: 900, Kind: CouponNamedQuests, QuestIDs: []int64{674, 649}}}
			if scenario == "under_level" {
				c.Level = level - 1
			}
			if scenario == "foreign" {
				in.AccountID = "b"
			}
			if scenario == "unknown_quest" {
				in.Coupon.QuestIDs = append(in.Coupon.QuestIDs, 999)
			}
			if scenario == "expired" || scenario == "renewed_absolute" {
				v := inv.Slots["0:87"]
				v.ExpireAt = now.Add(-time.Hour)
				inv.Slots["0:87"] = v
			}
			if scenario == "renewed_absolute" {
				in.NormalizeSource = func(v repo.ItemStack) (repo.ItemStack, error) { v.ExpireAt = now.AddDate(2, 0, 0); return v, nil }
			}
			bit, _ := ExEquipSlotBitForPVFIndex(uint32(index))
			if scenario == "already_done" || scenario == "repair_missing_bit" {
				q.States[674] = repo.QuestState{Status: "completed"}
				q.States[649] = repo.QuestState{Status: "completed"}
				if scenario == "already_done" {
					c.Stats["ex_equip_slot_stat"] |= int64(bit)
				}
			}
			for _, err := range []error{g.Character.Save(ctx, c), g.Inventory.Save(ctx, inv), g.Quest.Save(ctx, q)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			injected := g
			if scenario == "inventory_failure" || scenario == "quest_failure" {
				injected.CharacterSettlement = couponFailUOW{g.CharacterSettlement, scenario}
			}
			owner, _ := NewOwner(injected)
			r, err := owner.UseClearCoupon(ctx, catalog, in)
			success := scenario == "support" || scenario == "magic" || scenario == "earring" || scenario == "repair_missing_bit" || scenario == "renewed_absolute"
			ac, _, _ := g.Character.Load(ctx, "1")
			ai, _, _ := g.Inventory.Load(ctx, "1")
			aq, _, _ := g.Quest.Load(ctx, "1")
			if !success {
				if r.Changed || !reflect.DeepEqual(c, ac) || !reflect.DeepEqual(inv, ai) || !reflect.DeepEqual(q, aq) {
					t.Fatalf("rejected/noop changed state: %s %v", scenario, err)
				}
				return
			}
			if err != nil || !r.Changed || !r.SlotsExpanded || ac.Stats["ex_equip_slot_stat"] != 8|int64(bit) || ac.Stats["gold"] != 100 || ac.Stats["exp"] != 123 || ai.Slots["0:87"].Count != 1 || aq.States[649].Status != "completed" || !reflect.DeepEqual(aq.States[650], q.States[650]) {
				t.Fatalf("bad committed coupon: %+v %v", r, err)
			}
			again, err := owner.UseClearCoupon(ctx, catalog, in)
			if err != nil || again.Changed {
				t.Fatalf("repeat: %+v %v", again, err)
			}
		})
	}
}

func TestAchievementCouponTitleOnlyAndAllocationFailureRollsBack(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "full_inventory"}[fail], func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			now := time.Now()
			d := Definition{ID: 1, Grade: "[achievement]", Type: "[hunt monster]", LevelMin: 10, Job: "[all]", RewardType: "[title]", RewardIntData: []int64{26607, 1}}
			special := d
			special.ID = 2
			special.Name = "梅尔文的高科技"
			special.RewardType = "[item]"
			q := repo.QuestRecord{CharacterID: "1"}
			inv := repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 900, Count: 1}}}
			g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 1}})
			g.Inventory.Save(ctx, inv)
			g.Quest.Save(ctx, q)
			owner, _ := NewOwner(g)
			calls := 0
			in := UseClearCouponInput{AccountID: "a", CharacterID: "1", Slot: 87, Now: now, Coupon: ClearCoupon{ItemID: 900, Kind: CouponAchievement}, AllocateItem: func(v *repo.InventoryRecord, r FinishItemGrantRequest) ([]FinishCommittedItem, error) {
				calls++
				if fail {
					return nil, errors.New("full")
				}
				if r.ItemID != 26607 || r.Count != 1 {
					t.Fatal("wrong title")
				}
				v.Slots["0:9"] = repo.ItemStack{ItemID: r.ItemID, Count: 1}
				return nil, nil
			}}
			r, err := owner.UseClearCoupon(ctx, &Catalog{ordered: []Definition{d, special}, byID: map[int64]Definition{1: d, 2: special}}, in)
			ai, _, _ := g.Inventory.Load(ctx, "1")
			aq, _, _ := g.Quest.Load(ctx, "1")
			if fail {
				if err == nil || r.Changed || !reflect.DeepEqual(ai, inv) || !reflect.DeepEqual(aq, q) {
					t.Fatal("failed allocation was not atomic")
				}
				return
			}
			if err != nil || !r.Changed || calls != 1 || ai.Slots["0:9"].ItemID != 26607 || ai.Slots["0:87"].Count != 0 || aq.States[1].Extra["reward_state"] != "granted" || aq.States[2].Status != "" {
				t.Fatalf("title result %+v %v", r, err)
			}
		})
	}
}
