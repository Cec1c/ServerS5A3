package skillcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 道场两页沿用已有技能规则，另存角色 settings；不改写刷图两页、扩展资格或冷却。
func DojoSkillScope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":pvp_dojo_skills_v1"
}

type dojoSkillStore struct {
	base                   repo.Group
	accountID, characterID string
	writable               bool
	requestedTree          *byte
}

func (r dojoSkillStore) Load(ctx context.Context, id string) (repo.SkillRecord, bool, error) {
	if id != r.characterID || r.base.Character == nil || r.base.Skill == nil || r.base.Settings == nil {
		return repo.SkillRecord{}, false, ErrOwnerUnavailable
	}
	c, found, err := r.base.Character.Load(ctx, id)
	if err != nil {
		return repo.SkillRecord{}, false, err
	}
	if !found || c.AccountID != r.accountID {
		return repo.SkillRecord{}, false, ErrCharacterOwner
	}
	ordinary, found, err := r.base.Skill.Load(ctx, id)
	if err != nil || !found {
		return repo.SkillRecord{}, false, err
	}
	saved, exists, err := r.base.Settings.Load(ctx, DojoSkillScope(id))
	if err != nil {
		return repo.SkillRecord{}, false, err
	}
	var out repo.SkillRecord
	if exists {
		if saved.Scope != DojoSkillScope(id) || saved.Values["profile"] == "" {
			return out, false, fmt.Errorf("道场技能记录缺少加点数据")
		}
		if err := json.Unmarshal([]byte(saved.Values["profile"]), &out); err != nil {
			return out, false, fmt.Errorf("道场技能记录无法解析: %w", err)
		}
		if out.CharacterID != id || !out.Trees.Expanded || out.Trees.Active > 1 {
			return out, false, fmt.Errorf("道场技能归属或页码无效")
		}
	} else {
		out = repo.CloneSkill(ordinary)
		// 原生道场管理器进入时选中第零页；从已存在的真实加点复制，不凭空授予技能点。
		zero, _ := repo.SkillTree(ordinary, 0)
		one, _ := repo.SkillTree(ordinary, 1)
		if !ordinary.Trees.Expanded && len(one.Skills) == 0 {
			one = zero
		}
		out.Skills, out.Points = zero.Skills, zero.Points
		out.Trees = repo.SkillTreeState{Expanded: true, Other: one}
		out.Layouts = map[int]repo.SkillLayout{0: ordinary.Layouts[0], 1: ordinary.Layouts[1]}
		if !ordinary.Trees.Expanded {
			out.Layouts[1] = ordinary.Layouts[0]
		}
		out = repo.CloneSkill(out)
	}
	if r.requestedTree != nil {
		if *r.requestedTree > 1 {
			return repo.SkillRecord{}, false, ErrSkillTree
		}
		if out.Trees.Active != *r.requestedTree {
			other := out.Trees.Other
			out.Trees.Other = repo.SkillProfile{Skills: out.Skills, Points: out.Points}
			out.Skills, out.Points = other.Skills, other.Points
			out.Trees.Active = *r.requestedTree
		}
	}
	out.Cooldowns = repo.CloneSkill(ordinary).Cooldowns
	return out, true, nil
}

func (r dojoSkillStore) Save(ctx context.Context, record repo.SkillRecord) error {
	if !r.writable || record.CharacterID != r.characterID || r.base.Settings == nil {
		return ErrOwnerUnavailable
	}
	if !record.Trees.Expanded || record.Trees.Active > 1 {
		return ErrSkillTree
	}
	record = repo.CloneSkill(record)
	record.Cooldowns = nil
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return r.base.Settings.Save(ctx, repo.SettingsRecord{Scope: DojoSkillScope(record.CharacterID), Values: map[string]string{"profile": string(raw)}, UpdatedAt: record.UpdatedAt})
}

func (r dojoSkillStore) SaveFields(ctx context.Context, record repo.SkillRecord, fields ...repo.SkillField) error {
	current, found, err := r.Load(ctx, record.CharacterID)
	if err != nil {
		return err
	}
	if !found {
		return ErrSkillTreeUnavailable
	}
	for _, f := range repo.SkillFields.Normalize(fields) {
		switch f {
		case repo.SkillFieldSkills:
			current.Skills = record.Skills
		case repo.SkillFieldPoints:
			current.Points = record.Points
		case repo.SkillFieldLayouts:
			current.Layouts = record.Layouts
		case repo.SkillFieldTrees:
			current.Trees = record.Trees
		default:
			return fmt.Errorf("道场技能不得写入字段 %s", f)
		}
	}
	current.UpdatedAt = record.UpdatedAt
	return r.Save(ctx, current)
}

type dojoSkillTransactions struct {
	base                   repo.Group
	accountID, characterID string
	requestedTree          *byte
}

func (u dojoSkillTransactions) WithinCharacterSkill(ctx context.Context, id string, apply func(repo.SkillRepository) error) error {
	if id != u.characterID || apply == nil || u.base.CharacterSettlement == nil {
		return ErrOwnerUnavailable
	}
	return u.base.CharacterSettlement.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		return apply(dojoSkillStore{tx, u.accountID, id, true, u.requestedTree})
	})
}
func (u dojoSkillTransactions) WithinCharacterSettlement(ctx context.Context, id string, apply func(repo.Group) error) error {
	if id != u.characterID || apply == nil || u.base.CharacterSettlement == nil {
		return ErrOwnerUnavailable
	}
	return u.base.CharacterSettlement.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		tx.Skill = dojoSkillStore{tx, u.accountID, id, true, u.requestedTree}
		return apply(tx)
	})
}

// DojoRepositories 仅用于已验证场景中的28/29/491请求；交易仍由原仓储事务提交。
func DojoRepositories(base repo.Group, accountID, characterID string, requested ...byte) repo.Group {
	var tree *byte
	if len(requested) > 0 {
		tree = &requested[0]
	}
	out := base
	out.Skill = dojoSkillStore{base, accountID, characterID, false, tree}
	tx := dojoSkillTransactions{base, accountID, characterID, tree}
	out.CharacterSkills = tx
	out.CharacterSettlement = tx
	return out
}

func PrepareDojoSkills(ctx context.Context, base repo.Group, accountID, characterID string) (repo.SkillRecord, error) {
	var out repo.SkillRecord
	err := (dojoSkillTransactions{base, accountID, characterID, nil}).WithinCharacterSkill(ctx, characterID, func(store repo.SkillRepository) error {
		record, found, err := store.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrSkillTreeUnavailable
		}
		// 每次进入只补缺失记录，保留已保存的独立加点。
		txStore := store.(dojoSkillStore)
		_, exists, err := txStore.base.Settings.Load(ctx, DojoSkillScope(characterID))
		if err != nil {
			return err
		}
		if !exists {
			record.UpdatedAt = time.Now().UTC()
			if err := store.Save(ctx, record); err != nil {
				return err
			}
		}
		out = repo.CloneSkill(record)
		return nil
	})
	return out, err
}
