package inventory

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/profession"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skill"
)

var (
	ErrProfessionChange     = errors.New("转职变更条件不满足")
	ErrProfessionChangeGold = errors.New("转职变更金币不足")
)

const ProfessionChangeCountKey = "growup_change_count"

type ProfessionChangeConfig struct {
	MinimumLevel, MaximumLevel int
	GoldCosts                  []int64
}

func (c ProfessionChangeConfig) GoldCost(count int64) (int64, error) {
	if c.MinimumLevel < 1 || c.MaximumLevel < c.MinimumLevel || c.MaximumLevel > 90 || len(c.GoldCosts) == 0 || len(c.GoldCosts) > 128 || count < 0 || count > math.MaxInt32 {
		return 0, ErrProfessionChange
	}
	for i, value := range c.GoldCosts {
		if value < 0 || value > math.MaxInt32 || i > 0 && value < c.GoldCosts[i-1] {
			return 0, ErrProfessionChange
		}
	}
	if c.GoldCosts[0] != 0 {
		return 0, ErrProfessionChange
	}
	if count >= int64(len(c.GoldCosts)) {
		count = int64(len(c.GoldCosts) - 1)
	}
	return c.GoldCosts[count], nil
}

type ProfessionChangeBook struct {
	ItemID                                                               int64
	PVFPath                                                              string
	MinimumLevel, MaximumLevel                                           int
	Jobs                                                                 []string
	AllowedBranches                                                      []byte
	Places                                                               []string
	PreserveAwakening, RequireAwakening, ClearMain, BlockActiveAwakening bool
	ReturnBranch                                                         byte
	ReturnGroup, RecordReturnGroup                                       int64
	CoolTimeMS                                                           int64
	SlotPolicy                                                           itemplacement.SlotPolicy
	ProjectSource                                                        func(repo.ItemStack, time.Time) (repo.ItemStack, error)
	GrantBonus                                                           func(*repo.InventoryRecord, time.Time) ([]int16, error)
}

type ProfessionChangeRequest struct {
	AccountID, CharacterID string
	Target                 byte
	Slot                   int16
	Book                   *ProfessionChangeBook
	Config                 ProfessionChangeConfig
	Profiles               *profession.Profiles
	Skills                 *skill.Table
	Quests                 *quest.Catalog
	PointBaseline          func(repo.CharacterRecord) (repo.SkillPointState, error)
	Revalidate             func() bool
}

type ProfessionChangeResult struct {
	Character            repo.CharacterRecord
	Skill                repo.SkillRecord
	Quest                repo.QuestRecord
	Transition           profession.Transition
	ChangedSlots         []int16
	PreviousCount, Count int64
	GoldCost             int64
}

// ApplyProfessionChange 在同一角色结算中保存扣书/金币、职业、次数、双技能页及任务。
// 请求没有业务nonce；失败不自动重提事务，网络重试同一目标不会再次消费。
func ApplyProfessionChange(ctx context.Context, g repo.Group, req ProfessionChangeRequest, now time.Time) (ProfessionChangeResult, error) {
	if g.CharacterSettlement == nil || req.AccountID == "" || req.CharacterID == "" || req.Profiles == nil || req.Skills == nil || req.Quests == nil || req.PointBaseline == nil || req.Revalidate == nil || now.IsZero() || req.Target > 15 {
		return ProfessionChangeResult{}, ErrProfessionChange
	}
	var result ProfessionChangeResult
	err := g.CharacterSettlement.WithinCharacterSettlement(ctx, req.CharacterID, func(tx repo.Group) error {
		if tx.Character == nil || tx.Skill == nil || tx.Quest == nil || !req.Revalidate() {
			return ErrProfessionChange
		}
		c, found, err := tx.Character.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != req.CharacterID || c.AccountID != req.AccountID || c.Stats["delete_flag"] != 0 || c.Level < 1 || c.Level > 90 {
			return ErrProfessionChange
		}
		job, err := strconv.Atoi(c.Job)
		grow, valid := repo.CharacterGrowTypeInDomain(c)
		if err != nil || !jobmap.Valid(job) || !valid {
			return ErrProfessionChange
		}
		c = repo.CloneCharacter(c)
		if c.Stats == nil {
			return ErrProfessionChange
		}
		count := c.Stats[ProfessionChangeCountKey]
		if count < 0 || count > math.MaxInt32 || c.Stats["gold"] < 0 || c.Stats["gold"] > math.MaxInt32 {
			return ErrProfessionChange
		}
		result.PreviousCount, result.Count = count, count
		preserve := false
		var bag repo.InventoryRecord
		var stack repo.ItemStack
		var sourceKey string
		if req.Book == nil {
			cost, err := req.Config.GoldCost(count)
			if err != nil || c.Level < req.Config.MinimumLevel || c.Level > req.Config.MaximumLevel {
				return ErrProfessionChange
			}
			if c.Stats["gold"] < cost {
				return ErrProfessionChangeGold
			}
			result.GoldCost = cost
			c.Stats["gold"] -= cost
			if count < math.MaxInt32 {
				count++
			}
			c.Stats[ProfessionChangeCountKey] = count
			result.Count = count
		} else {
			d := req.Book
			if c.Stats["is_hardcore_mode"] != 0 {
				return ErrProfessionChange
			}
			if tx.Inventory == nil || req.Slot < 0 || req.Target == 0 || d.ItemID <= 0 || d.ItemID > math.MaxUint32 || d.PVFPath == "" || d.ProjectSource == nil || c.Level < d.MinimumLevel || c.Level > d.MaximumLevel || d.MinimumLevel < 1 || d.MaximumLevel < d.MinimumLevel || d.MaximumLevel > 90 || d.CoolTimeMS < 0 || d.CoolTimeMS > math.MaxUint32 {
				return ErrProfessionChange
			}
			allowed := false
			for _, tag := range d.Jobs {
				allowed = allowed || tag == "[all]" || jobmap.MatchesQuestTag(tag, job)
			}
			if !allowed {
				return ErrProfessionChange
			}
			first, stage := profession.Decode(byte(grow))
			if d.RequireAwakening && stage == 0 {
				return ErrProfessionChange
			}
			if len(d.AllowedBranches) > 0 {
				allowedCurrent, allowedTarget := false, false
				for _, branch := range d.AllowedBranches {
					allowedCurrent = allowedCurrent || branch == first
					allowedTarget = allowedTarget || branch == req.Target
				}
				if !allowedCurrent || !allowedTarget {
					return ErrProfessionChange
				}
			}
			if d.ReturnBranch != 0 && (req.Target != d.ReturnBranch || c.Stats["profession_return_group"] != d.ReturnGroup || c.Stats["profession_return_grow"] != int64(stage<<4|req.Target) || c.Stats["profession_return_from"] != int64(grow)) {
				return ErrProfessionChange
			}
			coolKey := "profession_book_next_use_ms_" + strconv.FormatInt(d.ItemID, 10)
			if c.Stats[coolKey] < 0 || c.Stats[coolKey] > now.UnixMilli() {
				return ErrProfessionChange
			}
			bag, found, err = tx.Inventory.Load(ctx, req.CharacterID)
			if err != nil {
				return err
			}
			if !found || bag.CharacterID != req.CharacterID {
				return ErrProfessionChange
			}
			bag = repo.CloneInventory(bag)
			if !d.SlotPolicy.ForInventory(&bag).Contains(0, req.Slot) {
				return ErrProfessionChange
			}
			sourceKey = slotKey(0, req.Slot)
			stack, found = bag.Slots[sourceKey]
			// 先交共用PVF投影处理既有历史期限政策，再检查有效期；锁和数量仍先校验。
			preview := stack
			preview.ExpireAt = time.Time{}
			if !found || stack.ItemID != d.ItemID || !levelUpTicketStackUsable(preview, now) {
				return ErrProfessionChange
			}
			if p := strings.TrimSpace(stack.Extra["pvf_path"]); p != "" && !strings.EqualFold(strings.ReplaceAll(p, "\\", "/"), d.PVFPath) {
				return ErrProfessionChange
			}
			stack, err = d.ProjectSource(stack, now)
			if err != nil || stack.ItemID != d.ItemID || !levelUpTicketStackUsable(stack, now) {
				return ErrProfessionChange
			}
			preserve = d.PreserveAwakening
			if d.CoolTimeMS > 0 {
				c.Stats[coolKey] = now.UnixMilli() + d.CoolTimeMS
			}
		}
		transition, err := req.Profiles.PlanBranchChange(req.Skills, byte(job), byte(grow), req.Target, preserve)
		if err != nil {
			return err
		}
		skills, found, err := tx.Skill.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found || skills.CharacterID != req.CharacterID {
			return ErrProfessionChange
		}
		points, err := req.PointBaseline(c)
		if err != nil {
			return err
		}
		if points.SyncedLevel != c.Level || points.TotalSP < 0 || points.TotalTP < 0 || points.TotalSP > math.MaxUint16 || points.TotalTP > math.MaxUint16 {
			return ErrProfessionChange
		}
		points.RemainingSP, points.RemainingTP = points.TotalSP, points.TotalTP
		skills, err = req.Profiles.ApplySkillTransition(req.Skills, byte(job), c.Level, transition, skills, points)
		if err != nil {
			return err
		}
		q, found, err := tx.Quest.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			q = repo.QuestRecord{CharacterID: req.CharacterID}
		}
		if q.CharacterID != req.CharacterID {
			return ErrProfessionChange
		}
		if req.Book != nil && req.Book.BlockActiveAwakening && req.Quests.HasActiveAwakeningQuest(q) {
			return ErrProfessionChange
		}
		clearMain := req.Book != nil && req.Book.ClearMain && transition.AwakeningStage > 0
		q, err = req.Quests.PlanBranchChangeQuests(q, job, int(transition.NewGrowType), c.Level, clearMain, now)
		if err != nil {
			return err
		}
		// 一次后续转职使此前回归凭据失效；同一事务内记录活动转换书的原职业。
		delete(c.Stats, "profession_return_group")
		delete(c.Stats, "profession_return_grow")
		delete(c.Stats, "profession_return_from")
		if req.Book != nil && req.Book.RecordReturnGroup > 0 {
			c.Stats["profession_return_group"] = req.Book.RecordReturnGroup
			c.Stats["profession_return_grow"] = int64(grow)
			c.Stats["profession_return_from"] = int64(transition.NewGrowType)
		}
		c.Stats["grow_type"] = int64(transition.NewGrowType)
		c.UpdatedAt, skills.UpdatedAt = now, now
		if req.Book != nil {
			stack = cloneStack(stack)
			stack.Count--
			if stack.Count == 0 {
				delete(bag.Slots, sourceKey)
			} else {
				updateStackRawAmount(&stack)
				bag.Slots[sourceKey] = stack
			}
			result.ChangedSlots = []int16{req.Slot}
			if req.Book.GrantBonus != nil {
				slots, err := req.Book.GrantBonus(&bag, now)
				if err != nil {
					return err
				}
				result.ChangedSlots = append(result.ChangedSlots, slots...)
			}
			bag.UpdatedAt = now
			if err := repo.SaveInventoryFields(ctx, tx.Inventory, bag, repo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if err := repo.SaveCharacterFields(ctx, tx.Character, c, repo.CharacterFieldStats); err != nil {
			return err
		}
		if err := repo.SaveSkillFields(ctx, tx.Skill, skills, repo.SkillFieldSkills, repo.SkillFieldLayouts, repo.SkillFieldCooldowns, repo.SkillFieldPoints, repo.SkillFieldTrees); err != nil {
			return err
		}
		if err := repo.SaveQuestFields(ctx, tx.Quest, q, repo.QuestFieldStates, repo.QuestFieldProgress); err != nil {
			return err
		}
		if !req.Revalidate() {
			return ErrProfessionChange
		}
		result.Character, result.Skill, result.Quest, result.Transition = c, skills, q, transition
		return nil
	})
	if err != nil {
		return ProfessionChangeResult{}, err
	}
	return result, nil
}
