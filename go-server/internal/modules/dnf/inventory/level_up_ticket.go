package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrLevelUpTicket = errors.New("升级券的物品、等级、职业、场景或冷却条件不满足")

type LevelUpTicketOutcome struct {
	Levels int
	Weight int64
}
type LevelUpTicketDefinition struct {
	ItemID                                  int64
	PVFPath                                 string
	MinimumLevel, MaximumLevel, MaximumGain int
	Jobs                                    []string
	Places                                  []string
	CoolTimeMS                              int64
	DisallowHardcore                        bool
	Outcomes                                []LevelUpTicketOutcome
}
type LevelUpTicketRequest struct {
	AccountID, CharacterID string
	SlotIndex              int16
	LevelCap               int
	Definition             LevelUpTicketDefinition
	Revalidate             func() bool
	Roll                   func(int64) (int64, error)
}
type LevelUpTicketResult struct {
	ItemID, Remaining int64
	ChangedSlots      []int16
	Character         repo.CharacterRecord
	Skill             repo.SkillRecord
	Progression       progression.ExperienceSkillPointPlan
	QuestPlan         quest.AutoCompleteMainPlan
}

func DecodeLevelUpTicketRequest(body []byte) (int16, byte, error) {
	if len(body) != 3 {
		return 0, 0, ErrLevelUpTicket
	}
	slot, list := int16(binary.LittleEndian.Uint16(body)), body[2]
	if slot < 0 || list != listTypeMain {
		return slot, list, ErrLevelUpTicket
	}
	return slot, list, nil
}

// ApplyLevelUpTicket 在一次角色结算中保存扣券、等级/经验、SP/TP、冷却和任务。
// 请求没有操作nonce；未知提交结果不自动重试，成功后的网络刷新不再次提交。
func ApplyLevelUpTicket(ctx context.Context, g repo.Group, tables *progression.Tables, catalog *quest.Catalog, req LevelUpTicketRequest, now time.Time) (LevelUpTicketResult, error) {
	d := req.Definition
	if g.CharacterSettlement == nil || tables == nil || catalog == nil || req.Revalidate == nil || req.CharacterID == "" || req.AccountID == "" || req.SlotIndex < 0 || now.IsZero() ||
		req.LevelCap < 1 || req.LevelCap > math.MaxUint8 || d.ItemID <= 0 || d.ItemID > math.MaxUint32 || strings.TrimSpace(d.PVFPath) == "" || d.MaximumGain < 1 || d.MaximumGain > req.LevelCap ||
		d.MinimumLevel < 0 || d.MaximumLevel < d.MinimumLevel || d.MaximumLevel >= req.LevelCap || d.CoolTimeMS < 0 || d.CoolTimeMS > math.MaxUint32 || len(d.Jobs) == 0 {
		return LevelUpTicketResult{}, ErrLevelUpTicket
	}
	var weight int64
	for _, outcome := range d.Outcomes {
		if outcome.Levels < 1 || outcome.Levels > d.MaximumGain || outcome.Weight < 0 || outcome.Weight > math.MaxInt64-weight {
			return LevelUpTicketResult{}, ErrLevelUpTicket
		}
		weight += outcome.Weight
	}
	if len(d.Outcomes) > 0 && (weight == 0 || req.Roll == nil) {
		return LevelUpTicketResult{}, ErrLevelUpTicket
	}
	var result LevelUpTicketResult
	err := g.CharacterSettlement.WithinCharacterSettlement(ctx, req.CharacterID, func(tx repo.Group) error {
		if tx.Character == nil || tx.Skill == nil || tx.Inventory == nil || tx.Quest == nil || !req.Revalidate() {
			return ErrLevelUpTicket
		}
		c, found, err := tx.Character.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != req.CharacterID || c.AccountID != req.AccountID || c.Level < 1 || c.Level >= req.LevelCap || c.Level < d.MinimumLevel || c.Level > d.MaximumLevel || c.Stats["delete_flag"] != 0 || d.DisallowHardcore && c.Stats["is_hardcore_mode"] != 0 {
			return ErrLevelUpTicket
		}
		job, err := strconv.Atoi(c.Job)
		if err != nil || !jobmap.Valid(job) {
			return ErrLevelUpTicket
		}
		allowed := false
		for _, tag := range d.Jobs {
			if tag == "[all]" || jobmap.MatchesQuestTag(tag, job) {
				allowed = true
			}
		}
		grow, valid := repo.CharacterGrowTypeInDomain(c)
		if !allowed || !valid {
			return ErrLevelUpTicket
		}
		cooldownKey := "level_up_ticket_next_use_ms_" + strconv.FormatInt(d.ItemID, 10)
		if c.Stats[cooldownKey] < 0 || c.Stats[cooldownKey] > now.UnixMilli() {
			return ErrLevelUpTicket
		}
		bag, found, err := tx.Inventory.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found || bag.CharacterID != req.CharacterID {
			return ErrInventoryNotFound
		}
		key := slotKey(listTypeMain, req.SlotIndex)
		stack, found := bag.Slots[key]
		if !found || stack.ItemID != d.ItemID || !levelUpTicketStackUsable(stack, now) {
			return ErrLevelUpTicket
		}
		if p := strings.TrimSpace(stack.Extra["pvf_path"]); p != "" && !strings.EqualFold(strings.ReplaceAll(p, "\\", "/"), d.PVFPath) {
			return ErrLevelUpTicket
		}
		skill, found, err := tx.Skill.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found || skill.CharacterID != req.CharacterID {
			return ErrLevelUpTicket
		}
		exp, present := c.Stats["exp"]
		if !present || exp < 0 || exp > math.MaxUint32 {
			return progression.ErrExperienceOutOfRange
		}
		next, err := tables.ThresholdToNext(c.Level)
		if err != nil {
			return err
		}
		// 历史存档可能只有等级提升而累计经验仍低于本级门槛。
		// 升级券按现有等级确定目标，补足目标累计经验；SP/TP和升级事件
		// 仍只计算本次跨过的等级，不补发历史等级奖励。
		if uint32(exp) >= next {
			return progression.ErrExperienceOutOfRange
		}
		levels := d.MaximumGain
		if len(d.Outcomes) > 0 {
			roll, err := req.Roll(weight)
			if err != nil {
				return err
			}
			if roll < 0 || roll >= weight {
				return ErrLevelUpTicket
			}
			for _, outcome := range d.Outcomes {
				if roll < outcome.Weight {
					levels = outcome.Levels
					break
				}
				roll -= outcome.Weight
			}
		}
		target := c.Level + levels
		if target > req.LevelCap {
			return fmt.Errorf("%w：升级目标%d超过上限", ErrLevelUpTicket, target)
		}
		total, err := tables.ThresholdToNext(target - 1)
		if err != nil {
			return err
		}
		if total <= uint32(exp) {
			return ErrLevelUpTicket
		}
		plan, err := tables.PlanExperienceAndSkillPoints(c.Level, uint32(exp), total-uint32(exp), req.LevelCap, skill.Points)
		if err != nil {
			return err
		}
		if plan.Experience.NewLevel != target || plan.Experience.Saturated || plan.SkillPoints.SPGain > math.MaxUint16 || plan.SkillPoints.TPGain > math.MaxUint16 {
			return ErrLevelUpTicket
		}
		quests, found, err := tx.Quest.Load(ctx, req.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			quests = repo.QuestRecord{CharacterID: req.CharacterID}
		}
		if quests.CharacterID != req.CharacterID {
			return ErrLevelUpTicket
		}
		qp, err := catalog.PlanLevelUpTicketQuestClear(quests, quest.CharacterEligibility{Level: target - 1, Job: job, GrowType: int(grow)}, now)
		if err != nil {
			return err
		}
		c, skill, bag = repo.CloneCharacter(c), repo.CloneSkill(skill), repo.CloneInventory(bag)
		if err := progression.ApplyCharacterExperience(&c, &skill, target, plan.Experience.NewExperience, plan.SkillPoints.New, now); err != nil {
			return err
		}
		stack = cloneStack(stack)
		stack.Count--
		if stack.Count == 0 {
			delete(bag.Slots, key)
		} else {
			updateStackRawAmount(&stack)
			bag.Slots[key] = stack
		}
		bag.UpdatedAt = now
		if d.CoolTimeMS > 0 {
			c.Stats[cooldownKey] = now.UnixMilli() + d.CoolTimeMS
		}
		if err := repo.SaveInventoryFields(ctx, tx.Inventory, bag, repo.InventoryFieldSlots); err != nil {
			return err
		}
		if err := repo.SaveCharacterFields(ctx, tx.Character, c, repo.CharacterFieldBase, repo.CharacterFieldStats); err != nil {
			return err
		}
		if err := repo.SaveSkillFields(ctx, tx.Skill, skill, repo.SkillFieldPoints); err != nil {
			return err
		}
		if len(qp.ChangedFields) > 0 {
			if err := repo.SaveQuestFields(ctx, tx.Quest, qp.Record, qp.ChangedFields...); err != nil {
				return err
			}
		}
		if !req.Revalidate() {
			return ErrLevelUpTicket
		}
		result = LevelUpTicketResult{ItemID: d.ItemID, Remaining: stack.Count, ChangedSlots: []int16{req.SlotIndex}, Character: c, Skill: skill, Progression: plan, QuestPlan: qp}
		return nil
	})
	if err != nil {
		return LevelUpTicketResult{}, err
	}
	return result, nil
}

func levelUpTicketStackUsable(stack repo.ItemStack, now time.Time) bool {
	kind := strings.TrimSpace(stack.Extra["item_kind"])
	return stack.Count > 0 && stack.Count <= math.MaxInt32 && (stack.ExpireAt.IsZero() || now.Before(stack.ExpireAt)) && (kind == "" || strings.EqualFold(kind, "stackable")) && !itemlock.IsLockedAt(stack, now)
}
