package quest

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/profession"
	repo "longheng.io/server/internal/modules/dnf/repository"
	skill "longheng.io/server/internal/modules/dnf/skill"
)

const (
	CouponMainStory      = "main_story"
	CouponFirstAwakening = "first_awakening"
)

// ClearCoupon is resolved from the actual inventory item's PVF, never from a
// client-supplied task ID, level or profession.
type ClearCoupon struct {
	ItemID       int64
	Kind         string
	MinimumLevel int
	MaximumLevel int
	CutoffLevel  int
	Jobs         []int
}

type UseClearCouponInput struct {
	AccountID, CharacterID string
	Slot                   int16
	Coupon                 ClearCoupon
	Now                    time.Time
	Profiles               *profession.Profiles
	Skills                 *skill.Table
}

type UseClearCouponResult struct {
	BlockedQuestionIDs []int64
	DamagedQuestionIDs []int64
	Character          repo.CharacterRecord
	Skill              repo.SkillRecord
	Quests             repo.QuestRecord
	ChangedQuestIDs    []int64
	Remaining          int64
	CutoffLevel        int
	Changed            bool
	Awakened           bool
}

// UseClearCoupon revalidates ownership, source and eligibility under the same
// lock that commits quest completion, awakening skills and consumption.
func (o *Owner) UseClearCoupon(ctx context.Context, catalog *Catalog, input UseClearCouponInput) (UseClearCouponResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil || input.AccountID == "" || input.CharacterID == "" || input.Slot < 0 || input.Coupon.ItemID <= 0 || input.Now.IsZero() {
		return UseClearCouponResult{}, ErrCouponClearUnavailable
	}
	if input.Coupon.Kind != CouponMainStory && input.Coupon.Kind != CouponFirstAwakening {
		return UseClearCouponResult{}, ErrCouponClearUnavailable
	}
	var result UseClearCouponResult
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, input.CharacterID, func(tx repo.Group) error {
		if tx.Character == nil || tx.Inventory == nil || tx.Quest == nil {
			return ErrCouponClearUnavailable
		}
		c, found, err := tx.Character.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != input.CharacterID || c.AccountID != input.AccountID {
			return ErrCharacterNotFound
		}
		job, err := strconv.Atoi(c.Job)
		if err != nil {
			return ErrQuestNotAcceptable
		}
		grow, valid := repo.CharacterGrowTypeInDomain(c)
		if !valid {
			return ErrQuestNotAcceptable
		}
		eligibility := CharacterEligibility{Level: c.Level, Job: job, GrowType: grow}
		if !definitionCharacterJobValid(eligibility) || c.Level < input.Coupon.MinimumLevel || (input.Coupon.MaximumLevel > 0 && c.Level > input.Coupon.MaximumLevel) {
			return ErrCouponClearLevelInvalid
		}
		if len(input.Coupon.Jobs) > 0 {
			allowed := false
			for _, j := range input.Coupon.Jobs {
				allowed = allowed || j == job
			}
			if !allowed {
				return ErrQuestNotAcceptable
			}
		}
		inv, found, err := tx.Inventory.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || inv.CharacterID != input.CharacterID {
			return ErrCouponClearUnavailable
		}
		key := "0:" + strconv.Itoa(int(input.Slot))
		stack, found := inv.Slots[key]
		if !found || stack.ItemID != input.Coupon.ItemID || stack.Count <= 0 || stack.Count > math.MaxInt32 || (!stack.ExpireAt.IsZero() && !input.Now.Before(stack.ExpireAt)) {
			return ErrCouponClearUnavailable
		}
		quests, found, err := tx.Quest.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			quests = repo.QuestRecord{CharacterID: input.CharacterID}
		} else if quests.CharacterID != input.CharacterID {
			return ErrQuestPersistVerify
		}
		result = UseClearCouponResult{Character: repo.CloneCharacter(c), Quests: repo.CloneQuest(quests), Remaining: stack.Count}
		var fields []repo.QuestField
		if input.Coupon.Kind == CouponMainStory {
			cutoff := min(c.Level, CouponMainQuestClearMaxLevel)
			if input.Coupon.CutoffLevel > 0 {
				cutoff = min(cutoff, input.Coupon.CutoffLevel)
			}
			plan, err := catalog.PlanCouponMainQuestClear(quests, eligibility, cutoff, CouponClearScopeMainStorySweep, input.Now)
			if err != nil {
				return err
			}
			result.Quests = plan.Record
			result.BlockedQuestionIDs = append([]int64(nil), plan.BlockedQuestionIDs...)
			result.DamagedQuestionIDs = append([]int64(nil), plan.DamagedQuestionIDs...)
			result.ChangedQuestIDs = append(plan.ChangedQuestIDs, plan.ChangedLinkedSubIDs...)
			result.CutoffLevel = cutoff
			fields = plan.ChangedFields
		} else {
			// Match the native first-awakening gate: a normal class branch at Lv50+,
			// with no awakening yet. External classes do not use this ticket.
			if c.Level < 50 || job == 9 || job == 10 || grow&15 == 0 {
				return ErrQuestNotAcceptable
			}
			if grow>>4 >= 1 {
				return nil
			}
			if input.Profiles == nil || input.Skills == nil || tx.Skill == nil {
				return ErrFinishProfessionUnavailable
			}
			transition, err := input.Profiles.PlanTransition(byte(job), byte(grow), profession.Request{Kind: profession.KindAwakening, ChainType: 2, GrowNumber: 1, JobChangeQuest: 2})
			if err != nil {
				return err
			}
			terminal := false
			for _, def := range catalog.ordered {
				if def.JobChangeQuest != 2 || !definitionMatchesCharacter(def, eligibility) || def.LevelMin > c.Level {
					continue
				}
				if normalizeQuestTag(def.RewardType) == "awakening type" && len(def.RewardIntData) > 0 && def.RewardIntData[0] == 1 {
					terminal = true
				}
				state, field, known := mutableQuestState(&result.Quests, def.ID)
				if known && isCompletedQuestStatus(state.Status) {
					continue
				}
				if !known {
					field = repo.QuestFieldStates
				}
				state = couponMainQuestClearState(state, def, c.Level, input.Now)
				state.Extra["auto_complete_reason"] = "first_awakening_coupon_no_reward"
				if field == repo.QuestFieldProgress {
					result.Quests.Progress[def.ID] = state
				} else {
					if result.Quests.States == nil {
						result.Quests.States = make(map[int64]repo.QuestState)
					}
					result.Quests.States[def.ID] = state
				}
				fields = mergeQuestChangedField(fields, field)
				result.ChangedQuestIDs = append(result.ChangedQuestIDs, def.ID)
			}
			if !terminal {
				return fmt.Errorf("%w: first awakening terminal is missing", ErrQuestDefinitionMissing)
			}
			skills, found, err := tx.Skill.Load(ctx, input.CharacterID)
			if err != nil {
				return err
			}
			if !found || skills.CharacterID != input.CharacterID || skills.Points.SyncedLevel != c.Level {
				return ErrFinishProfessionUnavailable
			}
			skills, err = input.Profiles.ApplySkillTransition(input.Skills, byte(job), c.Level, transition, skills, skills.Points)
			if err != nil {
				return err
			}
			result.Character.Stats["grow_type"] = int64(transition.NewGrowType)
			result.Character.UpdatedAt = input.Now
			skills.UpdatedAt = input.Now
			result.Skill = skills
			result.Awakened = true
			if err := repo.SaveCharacterFields(ctx, tx.Character, result.Character, repo.CharacterFieldStats); err != nil {
				return err
			}
			if err := repo.SaveSkillFields(ctx, tx.Skill, skills, repo.SkillFieldSkills, repo.SkillFieldLayouts, repo.SkillFieldPoints); err != nil {
				return err
			}
		}
		if len(fields) == 0 && !result.Awakened {
			return nil
		}
		result.Quests.UpdatedAt = input.Now
		if len(fields) > 0 {
			if err := repo.SaveQuestFields(ctx, tx.Quest, result.Quests, fields...); err != nil {
				return err
			}
		}
		inv = repo.CloneInventory(inv)
		stack = inv.Slots[key]
		stack.Count--
		result.Remaining = stack.Count
		if stack.Count == 0 {
			delete(inv.Slots, key)
		} else {
			// The item projector rebuilds counts from Count; stale raw counts must not
			// survive a partial stack consumption.
			if len(stack.RawEntry) >= 10 {
				binary.LittleEndian.PutUint32(stack.RawEntry[6:10], uint32(stack.Count))
			}
			if stack.Extra != nil {
				stack.Extra["count"] = strconv.FormatInt(stack.Count, 10)
			}
			inv.Slots[key] = stack
		}
		inv.UpdatedAt = input.Now
		if err := repo.SaveInventoryFields(ctx, tx.Inventory, inv, repo.InventoryFieldSlots); err != nil {
			return err
		}
		result.Changed = true
		return nil
	})
	if err != nil {
		return UseClearCouponResult{}, err
	}
	return result, nil
}
