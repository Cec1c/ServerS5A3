package skillcmd

import (
	"context"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// PK继续使用已存在的独立两页存档，保留历史道场域名，避免重新复制覆盖旧加点。
// 普通Skill仓储始终属于刷图；这里只替换当前请求的仓储视图。
func PVPRepositories(base repo.Group, accountID, characterID string, requested ...byte) repo.Group {
	out := DojoRepositories(base, accountID, characterID, requested...)
	out.CharacterProgression = pvpSkillProgression{base: base, accountID: accountID, characterID: characterID}
	return out
}

func PreparePVPSkills(ctx context.Context, base repo.Group, accountID, characterID string) (repo.SkillRecord, error) {
	return PrepareDojoSkills(ctx, base, accountID, characterID)
}

type pvpSkillProgression struct {
	base                   repo.Group
	accountID, characterID string
}

func (u pvpSkillProgression) WithinCharacterProgression(ctx context.Context, id string, apply func(repo.CharacterRepository, repo.SkillRepository) error) error {
	if id != u.characterID || apply == nil || u.base.CharacterSettlement == nil {
		return ErrOwnerUnavailable
	}
	return u.base.CharacterSettlement.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		before, found, err := tx.Character.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found || before.AccountID != u.accountID {
			return ErrCharacterOwner
		}
		view := dojoSkillStore{base: tx, accountID: u.accountID, characterID: id, writable: true}
		if err := apply(tx.Character, view); err != nil {
			return err
		}
		// 普通切页owner还会写角色镜像索引；PK页只保存到独立记录，不能改变刷图页。
		after, found, err := tx.Character.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		previous, existed := before.Stats[currentEXESkillTreeIndexStat]
		current, exists := after.Stats[currentEXESkillTreeIndexStat]
		if existed == exists && previous == current {
			return nil
		}
		after = repo.CloneCharacter(after)
		if existed {
			after.Stats[currentEXESkillTreeIndexStat] = previous
		} else {
			delete(after.Stats, currentEXESkillTreeIndexStat)
		}
		return repo.SaveCharacterFields(ctx, tx.Character, after, repo.CharacterFieldStats)
	})
}

// CopyPVPActivePageToOrdinary只在调用方角色结算事务中执行。
// 只替换普通当前页，另一页、PK两页、共用冷却以及扩展资格保持；不共享map。
func CopyPVPActivePageToOrdinary(ctx context.Context, tx repo.Group, accountID, characterID string) error {
	source, found, err := DojoRepositories(tx, accountID, characterID).Skill.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found {
		return ErrSkillTreeUnavailable
	}
	target, found, err := tx.Skill.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found {
		return ErrSkillTreeUnavailable
	}
	if source.CharacterID != characterID || target.CharacterID != characterID {
		return ErrCharacterOwner
	}
	page, found := repo.SkillTree(source, repo.ActiveSkillTree(source))
	if !found {
		return ErrSkillTree
	}
	target = repo.CloneSkill(target)
	totals := target.Points
	totals.TotalSP = max(totals.TotalSP, page.Points.TotalSP)
	totals.TotalTP = max(totals.TotalTP, page.Points.TotalTP)
	totals.SyncedLevel = max(totals.SyncedLevel, page.Points.SyncedLevel)
	points, _, err := dnfskill.SyncPointState(page.Points, totals)
	if err != nil {
		return err
	}
	target.Skills, target.Points = page.Skills, points
	if target.Layouts == nil {
		target.Layouts = make(map[int]repo.SkillLayout)
	}
	from := source.Layouts[repo.ActiveSkillTree(source)]
	layout := make(repo.SkillLayout, len(from))
	for slot, skill := range from {
		layout[slot] = skill
	}
	target.Layouts[repo.ActiveSkillTree(target)] = layout
	target.UpdatedAt = time.Now().UTC()
	return repo.SaveSkillFields(ctx, tx.Skill, target, repo.SkillFieldSkills, repo.SkillFieldPoints, repo.SkillFieldLayouts)
}
