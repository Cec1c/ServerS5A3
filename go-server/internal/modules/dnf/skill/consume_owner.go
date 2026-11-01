package skill

import (
	"context"
	"encoding/binary"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"time"
)

var ErrColorlessSkillUnavailable = errors.New("无色技能扣除条件无效")
var ErrColorlessSkillInsufficient = errors.New("无色小晶块不足")

type ColorlessUseCommand struct {
	AccountID, CharacterID, EventID string
	Definition                      Skill
	DungeonID                       int64
	At                              time.Time
	Current                         func() bool
}
type ColorlessUseResult struct {
	Accepted, Replayed bool
	Spent, Remaining   int64
	Slot               int16
}

// 扣晶块、记实际个数及战令使用次数共同提交；不按客户端宣称的等级或数量扣款。
func ConsumeColorlessSkill(ctx context.Context, g repo.Group, cmd ColorlessUseCommand) (ColorlessUseResult, error) {
	var out ColorlessUseResult
	u, ok := g.AccountAssets.(repo.AccountCharacterSkillAssetUnitOfWork)
	def := cmd.Definition
	if !ok || !def.Active || def.IsTPSkill() || !def.UsesColorlessCube || def.ColorlessCubeCount <= 0 || cmd.EventID == "" || cmd.DungeonID <= 0 || cmd.At.IsZero() {
		return out, ErrColorlessSkillUnavailable
	}
	slot, _ := repo.AccountWarehouseSlotForItem(repo.ColorlessCubeItemID)
	out.Slot = slot
	err := u.WithinAccountCharacterSkillAssets(ctx, cmd.AccountID, cmd.CharacterID, func(cubes repo.AccountInventoryRepository, characters repo.CharacterRepository, skills repo.SkillRepository) error {
		if cmd.Current != nil && !cmd.Current() {
			return ErrColorlessSkillUnavailable
		}
		account, found, err := cubes.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return ErrColorlessSkillInsufficient
		}
		ch, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != cmd.AccountID || ch.CharacterID != cmd.CharacterID || ch.Stats["delete_flag"] != 0 || ch.Level < def.RequiredLevel {
			return ErrColorlessSkillUnavailable
		}
		job, err := strconv.Atoi(ch.Job)
		if err != nil || job != int(def.Job) {
			return ErrColorlessSkillUnavailable
		}
		record, found, err := skills.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || record.CharacterID != cmd.CharacterID {
			return ErrColorlessSkillUnavailable
		}
		learned := record.Skills[int64(def.ID)]
		if !learned.Enabled || learned.Level <= 0 {
			return ErrColorlessSkillUnavailable
		}
		key := repo.AccountSharedInventorySlotKey(slot)
		stack := account.Slots[key]
		if stack.ItemID == repo.ColorlessCubeItemID {
			out.Remaining = stack.Count
		}
		event, err := repo.NewColorlessActivity(ch, cmd.EventID, def.ColorlessCubeCount, "skill", cmd.At, cmd.DungeonID)
		if err != nil {
			return err
		}
		created, err := repo.RecordCharacterActivity(ctx, characters, event)
		if err != nil {
			return err
		}
		if !created {
			out.Accepted = true
			out.Replayed = true
			return nil
		}
		if stack.ItemID != repo.ColorlessCubeItemID || stack.Count < def.ColorlessCubeCount {
			return ErrColorlessSkillInsufficient
		}
		if cmd.Current != nil && !cmd.Current() {
			return ErrColorlessSkillUnavailable
		}
		stack.Count -= def.ColorlessCubeCount
		out.Remaining = stack.Count
		if stack.Count == 0 {
			delete(account.Slots, key)
		} else {
			stack.RawEntry = append([]byte(nil), stack.RawEntry...)
			if len(stack.RawEntry) >= 10 {
				binary.LittleEndian.PutUint32(stack.RawEntry[6:10], uint32(stack.Count))
			}
			if stack.Extra != nil {
				stack.Extra["count"] = strconv.FormatInt(stack.Count, 10)
			}
			account.Slots[key] = stack
		}
		account.UpdatedAt = cmd.At
		if err := cubes.Save(ctx, account); err != nil {
			return err
		}
		ch = repo.CloneCharacter(ch)
		repo.AddBattlePassCounter(&ch, 15, 3037, 1, cmd.At)
		repo.AddReturneeCounter(&ch, repo.ReturneeCubeSkillCountKey, cmd.At)
		ch.UpdatedAt = cmd.At
		if err := repo.SaveCharacterFields(ctx, characters, ch, repo.CharacterFieldStats); err != nil {
			return err
		}
		out.Spent = def.ColorlessCubeCount
		out.Accepted = true
		return nil
	})
	if err != nil {
		out.Accepted = false
		out.Spent = 0
	}
	return out, err
}
