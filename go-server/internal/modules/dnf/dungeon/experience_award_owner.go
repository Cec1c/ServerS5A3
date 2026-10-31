package dungeon

import (
	"context"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ExperienceAwardCommand 将本批角色经验、熟练度与满级冒险团经验放进同一事务。
// 逐只奖励和取整由调用方规划；账号增量仍逐项应用，避免合计溢出及胶囊上限变化。
type ExperienceAwardCommand struct {
	Plan              progression.CommitPlan
	MaximumLevelGains []uint32
	AdventureRuntime  adventuregroup.RuntimeConfig
	UpdatedAt         time.Time
}

func (o *Owner) CommitExperienceAward(ctx context.Context, cmd ExperienceAwardCommand) (progression.CommitResult, error) {
	if o == nil || o.settlements == nil {
		return progression.CommitResult{}, ErrOwnerUnavailable
	}
	ctx = contextOrBackground(ctx)
	var result progression.CommitResult
	err := o.settlements.WithinCharacterSettlement(ctx, cmd.Plan.CharacterID, func(tx dnfrepo.Group) error {
		// 保留结算仓储的锁顺序：账号访问先锁读角色归属，再读取账号；
		// 经验提交继续在同一事务锁读角色和技能，不另开事务。
		if tx.Account == nil {
			return ErrSettlementStateInvalid
		}
		account, found, err := tx.Account.Load(ctx, cmd.Plan.AccountID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(account.AccountID) != cmd.Plan.AccountID {
			return ErrSettlementStateInvalid
		}
		result, err = progression.Commit(ctx, experienceAwardProgression{tx}, cmd.Plan)
		if err != nil || result.Idempotent {
			return err
		}
		account = dnfrepo.CloneAccount(account)
		now := updatedAtOrNow(cmd.UpdatedAt)
		state, err := adventuregroup.ParseRuntimeState(account, cmd.AdventureRuntime, now)
		if err != nil {
			return err
		}
		for _, gain := range cmd.MaximumLevelGains {
			state.AddMaximumLevelExperience(cmd.AdventureRuntime, gain)
			if uint64(gain) > math.MaxUint64-account.HonorExp {
				account.HonorExp = math.MaxUint64
			} else {
				account.HonorExp += uint64(gain)
			}
		}
		encoded, err := state.Marshal()
		if err != nil {
			return err
		}
		if account.Metadata == nil {
			account.Metadata = make(map[string]string)
		}
		account.Metadata[adventuregroup.RuntimeStateMetadataKey] = encoded
		account.UpdatedAt = now
		return tx.Account.Save(ctx, account)
	})
	if err != nil {
		return progression.CommitResult{}, err
	}
	return result, nil
}

// 复用经验提交的全部版本、归属、SP/TP及升级事件校验，使用已打开的结算事务。
type experienceAwardProgression struct{ tx dnfrepo.Group }

func (u experienceAwardProgression) WithinCharacterProgression(ctx context.Context, id string, apply func(dnfrepo.CharacterRepository, dnfrepo.SkillRepository) error) error {
	if u.tx.Character == nil || u.tx.Skill == nil {
		return ErrSettlementStateInvalid
	}
	return apply(u.tx.Character, u.tx.Skill)
}
