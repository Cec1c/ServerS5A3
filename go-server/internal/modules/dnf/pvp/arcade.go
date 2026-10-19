package pvp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// sub_1093C00 checks complete Windbell quest chains, not event IDs.
func ArcadeTrainingCompleted(quests repo.QuestRecord) bool {
	for _, chain := range [][2]int64{{2670, 2674}, {2675, 2678}} {
		complete := true
		for id := chain[0]; id <= chain[1]; id++ {
			state, exists := quests.States[id]
			if !exists {
				state = quests.Progress[id]
			}
			complete = complete && strings.EqualFold(strings.TrimSpace(state.Status), "completed")
		}
		if complete {
			return true
		}
	}
	return false
}

func SaveFatigueAcceleration(ctx context.Context, repos repo.Group, accountID, characterID string, enabled bool, now time.Time) error {
	if repos.CharacterSettlement == nil {
		return fmt.Errorf("fatigue preference transaction unavailable")
	}
	return repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != accountID {
			return fmt.Errorf("fatigue preference character ownership mismatch")
		}
		character = repo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		character.Stats["fatigue_acceleration"] = 0
		if enabled {
			character.Stats["fatigue_acceleration"] = 1
		}
		character.UpdatedAt = now
		return repo.SaveCharacterFields(ctx, tx.Character, character, repo.CharacterFieldStats)
	})
}

type ArcadeReward struct {
	Experience uint32
	Gold       uint32
}

type ArcadeRewardSnapshot struct {
	Character repo.CharacterRecord
	Skill     repo.SkillRecord
	Reward    ArcadeReward
}

type ArcadeRewardPlanner func(repo.CharacterRecord, repo.SkillRecord) (ArcadeRewardSnapshot, error)

// ApplyArcadeReward commits the progression, wallet and receipt together.
// The bridge supplies a PVF-backed plan and publishes only after this returns.
func ApplyArcadeReward(ctx context.Context, repos repo.Group, accountID, characterID, receipt string, plan ArcadeRewardPlanner, now time.Time) (ArcadeRewardSnapshot, error) {
	var result ArcadeRewardSnapshot
	if repos.CharacterSettlement == nil || receipt == "" || plan == nil {
		return result, fmt.Errorf("arcade reward transaction unavailable")
	}
	digest := sha256.Sum256([]byte(receipt))
	err := repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != accountID {
			return fmt.Errorf("arcade reward character ownership mismatch")
		}
		skill, found, err := tx.Skill.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("arcade reward skill ledger missing")
		}
		replay := true
		for i := 0; i < 4; i++ {
			value, exists := character.Stats[fmt.Sprintf("pvp_arcade_receipt_%d", i)]
			replay = replay && exists && value == int64(binary.LittleEndian.Uint64(digest[i*8:]))
		}
		if replay {
			result = ArcadeRewardSnapshot{Character: character, Skill: skill, Reward: ArcadeReward{
				Experience: uint32(character.Stats["pvp_arcade_reward_exp"]), Gold: uint32(character.Stats["pvp_arcade_reward_gold"]),
			}}
			return nil
		}
		result, err = plan(repo.CloneCharacter(character), repo.CloneSkill(skill))
		if err != nil {
			return err
		}
		if result.Character.CharacterID != characterID || result.Skill.CharacterID != characterID || result.Character.AccountID != accountID || result.Character.Stats == nil {
			return fmt.Errorf("invalid arcade reward plan")
		}
		for i := 0; i < 4; i++ {
			result.Character.Stats[fmt.Sprintf("pvp_arcade_receipt_%d", i)] = int64(binary.LittleEndian.Uint64(digest[i*8:]))
		}
		result.Character.Stats["pvp_arcade_reward_exp"] = int64(result.Reward.Experience)
		result.Character.Stats["pvp_arcade_reward_gold"] = int64(result.Reward.Gold)
		result.Character.UpdatedAt, result.Skill.UpdatedAt = now, now
		if err := repo.SaveCharacterFields(ctx, tx.Character, result.Character, repo.CharacterFieldBase, repo.CharacterFieldStats); err != nil {
			return err
		}
		return repo.SaveSkillFields(ctx, tx.Skill, result.Skill, repo.SkillFieldPoints)
	})
	return result, err
}
