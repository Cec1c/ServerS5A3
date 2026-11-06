package experienceitem

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/progression"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// ExperienceConsumable is resolved from the item's own PVF expUp section.
// MaximumUseLevel limits when it may be used, not the player's playable cap.
type ExperienceConsumable struct {
	ItemID                        int64
	Amount                        uint32
	MinimumLevel, MaximumUseLevel int
	StackLimit                    int64
	PVFPath                       string
	Slots                         itemplacement.SlotPolicy
}
type ExperienceCommand struct {
	AccountID, CharacterID string
	Slot                   int16
	Definition             ExperienceConsumable
	Tables                 *progression.Tables
	PlayableCap            int
	Now                    time.Time
	Session                string
	Sequence               uint64
	Revalidate             func() bool
	LevelStats             func(r.CharacterRecord, int) (map[string]int64, error)
}
type ExperienceUseResult struct {
	Character r.CharacterRecord
	Points    r.SkillPointState
	Remaining int64
	Replayed  bool
}

func (o *Owner) UseExperience(ctx context.Context, cmd ExperienceCommand) (ExperienceUseResult, error) {
	d := cmd.Definition
	nonce, e := hex.DecodeString(cmd.Session)
	if o == nil || o.repositories.CharacterSettlement == nil || d.ItemID <= 0 || d.Amount == 0 || d.MinimumLevel < 1 || d.MaximumUseLevel < d.MinimumLevel || d.MaximumUseLevel > 255 || d.StackLimit <= 0 || d.PVFPath == "" || cmd.Tables == nil || cmd.PlayableCap < d.MaximumUseLevel || cmd.Now.IsZero() || cmd.Sequence == 0 || cmd.Sequence > math.MaxInt64 || e != nil || len(nonce) != 32 {
		return ExperienceUseResult{}, ErrBookInvalid
	}
	if cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Slot < 3 || cmd.Slot > 288 || d.Slots.ListType != r.MainInventoryListType {
		return ExperienceUseResult{}, ErrItemMismatch
	}
	var result ExperienceUseResult
	e = o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, cmd.CharacterID, func(g r.Group) error {
		if g.Character == nil || g.Inventory == nil || g.Skill == nil {
			return ErrOwnerUnavailable
		}
		if cmd.Revalidate != nil && !cmd.Revalidate() {
			return ErrCharacterOwnerMismatch
		}
		c, found, e := g.Character.Load(ctx, cmd.CharacterID)
		if e != nil {
			return e
		}
		if !found || c.CharacterID != cmd.CharacterID || c.AccountID != cmd.AccountID {
			return ErrCharacterOwnerMismatch
		}
		c = r.CloneCharacter(c)
		bag, found, e := g.Inventory.Load(ctx, c.CharacterID)
		if e != nil {
			return e
		}
		if !found || bag.CharacterID != c.CharacterID {
			return ErrSlotNotFound
		}
		bag = r.CloneInventory(bag)
		skill, found, e := g.Skill.Load(ctx, c.CharacterID)
		if e != nil {
			return e
		}
		if !found || skill.CharacterID != c.CharacterID {
			return ErrLedgerUnavailable
		}
		const prefix = "experience_use_v1_"
		same := true
		for i := 0; i < 4; i++ {
			if c.Stats[prefix+"nonce_"+strconv.Itoa(i)] != int64(binary.LittleEndian.Uint64(nonce[i*8:])) {
				same = false
			}
		}
		key := slotKey(r.MainInventoryListType, cmd.Slot)
		if same && c.Stats[prefix+"sequence"] >= int64(cmd.Sequence) {
			if c.Stats[prefix+"sequence"] != int64(cmd.Sequence) || c.Stats[prefix+"slot"] != int64(cmd.Slot) || c.Stats[prefix+"item"] != d.ItemID {
				return ErrItemMismatch
			}
			remaining := int64(0)
			if bag.Slots[key].ItemID == d.ItemID {
				remaining = bag.Slots[key].Count
			}
			result = ExperienceUseResult{Character: c, Points: skill.Points, Remaining: remaining, Replayed: true}
			return nil
		}
		if c.Level < d.MinimumLevel || c.Level > d.MaximumUseLevel {
			return ErrBookInvalid
		}
		if e = r.BindInventoryContainerState(ctx, g.Settings, &bag); e != nil {
			return e
		}
		if !d.Slots.ForInventory(&bag).Contains(r.MainInventoryListType, cmd.Slot) {
			return ErrSlotNotFound
		}
		stack, found := bag.Slots[key]
		if !found || stack.ItemID != d.ItemID || stack.Count <= 0 || stack.Count > d.StackLimit || itemlock.IsLockedAt(stack, cmd.Now) || !stack.ExpireAt.IsZero() && !cmd.Now.Before(stack.ExpireAt) {
			return ErrItemMismatch
		}
		for _, name := range []string{"expire_time", "expire_unix"} {
			if raw := strings.TrimSpace(stack.Extra[name]); raw != "" {
				deadline, e := strconv.ParseInt(raw, 10, 64)
				if e != nil || deadline < 0 || deadline > 0 && deadline <= cmd.Now.Unix() {
					return ErrItemMismatch
				}
			}
		}
		experience := c.Stats["exp"]
		if experience < 0 || experience > math.MaxUint32 {
			return ErrBookInvalid
		}
		plan, e := cmd.Tables.PlanExperienceAndSkillPoints(c.Level, uint32(experience), d.Amount, cmd.PlayableCap, skill.Points)
		if e != nil {
			return e
		}
		if plan.Experience.LevelsGained > 0 && cmd.LevelStats == nil {
			return ErrLedgerUnavailable
		}
		if e = progression.ApplyCharacterExperience(&c, &skill, plan.Experience.NewLevel, plan.Experience.NewExperience, plan.SkillPoints.New, cmd.Now); e != nil {
			return e
		}
		if plan.Experience.LevelsGained > 0 {
			values, e := cmd.LevelStats(c, c.Level)
			if e != nil {
				return e
			}
			for k, v := range values {
				c.Stats[k] = v
			}
		}
		stack.Count--
		remaining := stack.Count
		if stack.Count == 0 {
			delete(bag.Slots, key)
		} else {
			stack.RawEntry = nil
			stack.Extra = cloneStringMap(stack.Extra)
			for _, k := range []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"} {
				delete(stack.Extra, k)
			}
			bag.Slots[key] = stack
		}
		for i := 0; i < 4; i++ {
			c.Stats[prefix+"nonce_"+strconv.Itoa(i)] = int64(binary.LittleEndian.Uint64(nonce[i*8:]))
		}
		c.Stats[prefix+"sequence"], c.Stats[prefix+"slot"], c.Stats[prefix+"item"] = int64(cmd.Sequence), int64(cmd.Slot), d.ItemID
		bag.UpdatedAt = cmd.Now
		if e = r.SaveInventoryFields(ctx, g.Inventory, bag, r.InventoryFieldSlots); e != nil {
			return e
		}
		if e = r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldBase, r.CharacterFieldStats); e != nil {
			return e
		}
		if e = r.SaveSkillFields(ctx, g.Skill, skill, r.SkillFieldPoints); e != nil {
			return e
		}
		if cmd.Revalidate != nil && !cmd.Revalidate() {
			return fmt.Errorf("%w: binding retired before commit", ErrCharacterOwnerMismatch)
		}
		result = ExperienceUseResult{Character: c, Points: skill.Points, Remaining: remaining}
		return nil
	})
	if e != nil {
		return ExperienceUseResult{}, e
	}
	return result, nil
}
