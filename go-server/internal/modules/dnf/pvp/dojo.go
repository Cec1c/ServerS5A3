package pvp

import (
	"context"
	"fmt"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

const DojoSkillModeKey = "pvp_dojo_skill_mode"

// Native sub_10938F0 permits the tutorial skill tree only for mode zero.
// Modes one/two are the player's later keep/restore selection (C2S1266).
func DojoSkillMode(character repo.CharacterRecord) byte {
	mode := character.Stats[DojoSkillModeKey]
	if mode < 0 || mode > 2 {
		return 0
	}
	return byte(mode)
}

// ApplyDojoSkillChoice把玩家的复制/关闭选择与复制结果放在同一角色事务。
// 模式2不改任何技能；同一完成选择的重放不能反复覆盖后来调整的刷图页。
func ApplyDojoSkillChoice(ctx context.Context, repos repo.Group, accountID, characterID string, mode byte, now time.Time) error {
	if mode < 1 || mode > 2 || repos.CharacterSettlement == nil {
		return fmt.Errorf("道场技能选择或事务不可用")
	}
	return repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != accountID {
			return fmt.Errorf("道场技能选择角色归属不符")
		}
		// 已关闭或已复制的选择均已完成，不允许延迟重放再次复制。
		if DojoSkillMode(character) != 0 {
			return nil
		}
		if mode == 1 {
			if err := skillcmd.CopyPVPActivePageToOrdinary(ctx, tx, accountID, characterID); err != nil {
				return err
			}
		}
		character = repo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		character.Stats[DojoSkillModeKey] = int64(mode)
		character.UpdatedAt = now
		return repo.SaveCharacterFields(ctx, tx.Character, character, repo.CharacterFieldStats)
	})
}

func SaveDojoSkillMode(ctx context.Context, repos repo.Group, accountID, characterID string, mode byte, now time.Time) error {
	if mode < 1 || mode > 2 || repos.CharacterSettlement == nil {
		return fmt.Errorf("invalid dojo skill mode or missing settlement")
	}
	return repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != accountID {
			return fmt.Errorf("dojo skill mode character ownership mismatch")
		}
		character = repo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		character.Stats[DojoSkillModeKey] = int64(mode)
		character.UpdatedAt = now
		return repo.SaveCharacterFields(ctx, tx.Character, character, repo.CharacterFieldStats)
	})
}
