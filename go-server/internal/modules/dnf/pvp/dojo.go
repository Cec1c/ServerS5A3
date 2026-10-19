package pvp

import (
	"context"
	"fmt"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
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
