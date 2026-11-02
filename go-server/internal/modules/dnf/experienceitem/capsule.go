package experienceitem

import (
	"context"
	"encoding/binary"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type CapsuleCommand struct {
	AccountID, CharacterID string
	SlotIndex              int16
	LevelCap               int
	Definition             Capsule
	Now                    time.Time
	Revalidate             func() bool
	// ProjectStack applies the existing item-lifetime policy to this locked
	// instance. It must never manufacture a different item or quantity.
	ProjectStack func(repo.ItemStack) (repo.ItemStack, error)
	LevelStats   func(repo.CharacterRecord, int) (map[string]int64, error)
}

type CapsuleResult struct {
	Character         repo.CharacterRecord
	Skill             repo.SkillRecord
	Progression       progression.ExperienceSkillPointPlan
	ItemID, Remaining int64
}

func (d Capsule) Gain(t *progression.Tables, level int) (uint32, error) {
	if err := d.Validate(); err != nil {
		return 0, err
	}
	if t == nil || level < d.MinimumLevel || level > d.MaximumLevel {
		return 0, ErrCapsule
	}
	switch d.Kind {
	case CapsuleFixed:
		return d.Amount, nil
	case CapsulePercent:
		next, err := t.ThresholdToNext(level)
		if err != nil {
			return 0, err
		}
		var previous uint32
		if level > 1 {
			previous, err = t.ThresholdToNext(level - 1)
			if err != nil {
				return 0, err
			}
		}
		if next <= previous {
			return 0, ErrCapsule
		}
		return uint32(uint64(next-previous) * uint64(d.Amount) / 100), nil
	case CapsuleTable:
		for _, b := range d.Bands {
			if level >= b.Minimum && level <= b.Maximum {
				return b.Amount, nil
			}
		}
	case CapsuleCrack:
		// The crack reward compatibility formula uses questParameter's
		// zero-based level row. The public table API is one-based.
		base, err := t.DungeonClearBaseExperience(level + 1)
		if err != nil {
			return 0, err
		}
		return uint32(uint64(base) * 56 / 100), nil
	}
	return 0, ErrCapsule
}

// UseCapsule commits the consumed instance, EXP, level, level-event receipts
// and SP/TP together. Ordinary experience capsules never auto-complete quests.
func UseCapsule(ctx context.Context, g repo.Group, tables *progression.Tables, cmd CapsuleCommand) (CapsuleResult, error) {
	d := cmd.Definition
	if err := d.Validate(); err != nil {
		return CapsuleResult{}, err
	}
	if g.CharacterSettlement == nil || tables == nil || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.SlotIndex < 0 || cmd.LevelCap < 2 || cmd.LevelCap > 255 || cmd.Now.IsZero() || cmd.Revalidate == nil || cmd.ProjectStack == nil || cmd.LevelStats == nil {
		return CapsuleResult{}, ErrCapsule
	}
	var result CapsuleResult
	err := g.CharacterSettlement.WithinCharacterSettlement(ctx, cmd.CharacterID, func(tx repo.Group) error {
		if tx.Character == nil || tx.Inventory == nil || tx.Skill == nil || !cmd.Revalidate() {
			return ErrCapsule
		}
		c, found, err := tx.Character.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != cmd.CharacterID || c.AccountID != cmd.AccountID || c.Level < d.MinimumLevel || c.Level > d.MaximumLevel || c.Level >= cmd.LevelCap || c.Stats["delete_flag"] != 0 || d.DisallowHardcore && c.Stats["is_hardcore_mode"] != 0 {
			return ErrCapsule
		}
		job, err := strconv.Atoi(c.Job)
		if err != nil || !jobmap.Valid(job) {
			return ErrCapsule
		}
		allowed := false
		for _, tag := range d.Jobs {
			allowed = allowed || tag == "[all]" || jobmap.MatchesQuestTag(tag, job)
		}
		for _, tag := range d.ExcludedJobs {
			if jobmap.MatchesQuestTag(tag, job) {
				allowed = false
			}
		}
		grow, valid := repo.CharacterGrowTypeInDomain(c)
		if !allowed || !valid || len(d.FirstGrowTypes) > 0 && !slices.Contains(d.FirstGrowTypes, int(grow)&15) {
			return ErrCapsule
		}
		coolKey := "experience_capsule_next_use_ms_" + strconv.FormatInt(d.ItemID, 10)
		if c.Stats[coolKey] < 0 || c.Stats[coolKey] > cmd.Now.UnixMilli() {
			return ErrCapsule
		}
		bag, found, err := tx.Inventory.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || bag.CharacterID != cmd.CharacterID {
			return ErrCapsule
		}
		key := "0:" + strconv.Itoa(int(cmd.SlotIndex))
		stack, found := bag.Slots[key]
		if !found || stack.ItemID != d.ItemID || stack.Count <= 0 || stack.Count > math.MaxInt32 || itemlock.IsLockedAt(stack, cmd.Now) {
			return ErrCapsule
		}
		if path := strings.TrimSpace(stack.Extra["pvf_path"]); path != "" && !strings.EqualFold(strings.ReplaceAll(path, "\\", "/"), d.PVFPath) {
			return ErrCapsule
		}
		projected, err := cmd.ProjectStack(stack)
		if err != nil {
			return err
		}
		if projected.ItemID != stack.ItemID || projected.Count != stack.Count || !projected.ExpireAt.IsZero() && !cmd.Now.Before(projected.ExpireAt) {
			return ErrCapsule
		}
		if kind := projected.Extra["item_kind"]; kind != "" && kind != "stackable" {
			return ErrCapsule
		}
		skill, found, err := tx.Skill.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || skill.CharacterID != cmd.CharacterID {
			return ErrCapsule
		}
		exp, present := c.Stats["exp"]
		if !present || exp < 0 || exp > math.MaxUint32 {
			return progression.ErrExperienceOutOfRange
		}
		next, err := tables.ThresholdToNext(c.Level)
		if err != nil {
			return err
		}
		if uint32(exp) >= next {
			return progression.ErrExperienceOutOfRange
		}
		gain, err := d.Gain(tables, c.Level)
		if err != nil {
			return err
		}
		if gain == 0 {
			return ErrCapsule
		}
		plan, err := tables.PlanExperienceAndSkillPoints(c.Level, uint32(exp), gain, cmd.LevelCap, skill.Points)
		if err != nil {
			return err
		}
		if plan.Experience.Saturated {
			return progression.ErrExperienceOutOfRange
		}
		c, skill, bag = repo.CloneCharacter(c), repo.CloneSkill(skill), repo.CloneInventory(bag)
		if plan.Experience.LevelsGained > 0 {
			stats, err := cmd.LevelStats(c, plan.Experience.NewLevel)
			if err != nil {
				return err
			}
			for key, value := range stats {
				c.Stats[key] = value
			}
		}
		if err := progression.ApplyCharacterExperience(&c, &skill, plan.Experience.NewLevel, plan.Experience.NewExperience, plan.SkillPoints.New, cmd.Now); err != nil {
			return err
		}
		projected.Count--
		if projected.Count == 0 {
			delete(bag.Slots, key)
		} else {
			projected.RawEntry = append([]byte(nil), projected.RawEntry...)
			// Keep the same count projection as inventory.updateStackRawAmount.
			if len(projected.RawEntry) == 0x77 {
				binary.LittleEndian.PutUint32(projected.RawEntry[6:10], uint32(projected.Count))
			}
			extra := make(map[string]string, len(projected.Extra))
			for k, v := range projected.Extra {
				extra[k] = v
			}
			for _, k := range []string{"amount_or_count", "amount", "count", "stack", "quantity"} {
				if _, ok := extra[k]; ok {
					extra[k] = strconv.FormatInt(projected.Count, 10)
				}
			}
			projected.Extra = extra
			bag.Slots[key] = projected
		}
		bag.UpdatedAt = cmd.Now
		if d.CoolTimeMS > 0 {
			c.Stats[coolKey] = cmd.Now.UnixMilli() + d.CoolTimeMS
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
		if !cmd.Revalidate() {
			return ErrCapsule
		}
		result = CapsuleResult{Character: c, Skill: skill, Progression: plan, ItemID: d.ItemID, Remaining: projected.Count}
		return nil
	})
	if err != nil {
		return CapsuleResult{}, err
	}
	return result, nil
}
