package quest

import (
	"encoding/binary"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
)

// Native QuestClearCouponWindow submits op34 with the inventory slot in the
// final u16. This authority is distinct from ordinary quest submission and
// may waive objectives/materials only for an owned, eligible physical coupon.
func consumeSingleQuestCoupon(inv *repo.InventoryRecord, c repo.CharacterRecord, state repo.QuestState, d Definition, in UseClearCouponInput) (FinishConsumedItem, error) {
	fail := func() (FinishConsumedItem, error) { return FinishConsumedItem{}, ErrCouponClearUnavailable }
	if inv == nil || in.Coupon.Kind != CouponSingleQuest || in.CharacterID != c.CharacterID || in.AccountID != c.AccountID || in.Slot < 0 || in.Now.IsZero() || in.Coupon.ItemID <= 0 || !isActiveQuestStatus(state.Status) || state.Extra["reward_state"] == finishRewardGranted {
		return fail()
	}
	job, err := strconv.Atoi(c.Job)
	if c.Stats["delete_flag"] != 0 || in.Coupon.DisallowHardcore && c.Stats["is_hardcore_mode"] != 0 {
		return fail()
	}
	grow, valid := repo.CharacterGrowTypeInDomain(c)
	if err != nil || !valid || c.Level < in.Coupon.MinimumLevel || in.Coupon.MaximumLevel > 0 && c.Level > in.Coupon.MaximumLevel || d.LevelMin > c.Level || !definitionMatchesCharacter(d, CharacterEligibility{Level: c.Level, Job: job, GrowType: grow}) {
		return fail()
	}
	if normalizeQuestTag(d.Grade) != "epic" || d.DisableQuestClear || d.JobChangeQuest != 0 || normalizeQuestTag(d.Type) == "question" || d.ID == 649 || d.ID == 650 || d.IsEvent {
		return fail()
	}
	if len(in.Coupon.Jobs) > 0 {
		allowed := false
		for _, j := range in.Coupon.Jobs {
			allowed = allowed || j == job
		}
		if !allowed {
			return fail()
		}
	}
	switch normalizeQuestTag(d.RewardType) {
	case "", "item":
	default:
		return fail()
	}
	key := "0:" + strconv.Itoa(int(in.Slot))
	stack, found := inv.Slots[key]
	if !found || stack.ItemID != in.Coupon.ItemID || stack.Count <= 0 || stack.Count > math.MaxInt32 {
		return fail()
	}
	if in.NormalizeSource != nil {
		stack, err = in.NormalizeSource(stack)
		if err != nil {
			return fail()
		}
	}
	if !stack.ExpireAt.IsZero() && !in.Now.Before(stack.ExpireAt) {
		return fail()
	}
	stack.Count--
	if stack.Count == 0 {
		delete(inv.Slots, key)
	} else {
		if len(stack.RawEntry) >= 10 {
			binary.LittleEndian.PutUint32(stack.RawEntry[6:10], uint32(stack.Count))
		}
		if stack.Extra != nil {
			stack.Extra["count"] = strconv.FormatInt(stack.Count, 10)
		}
		inv.Slots[key] = stack
	}
	return FinishConsumedItem{SlotKey: key, SlotIndex: uint16(in.Slot), ItemID: stack.ItemID, Delta: 1, PostCount: stack.Count}, nil
}
