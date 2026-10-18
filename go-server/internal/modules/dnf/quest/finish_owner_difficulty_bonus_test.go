package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The full difficulty key must feed both current bonus sources before the
// settlement receipt freezes the result. A late save failure must roll back
// progression, reward items and quest state together.
func TestOwnerTwoDigitDifficultyBonusesRollbackAndExpiredReplay(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		name := "commit"
		if failSave {
			name = "late_skill_failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := finishOwnerSeed(t, ctx)
			tables, err := progression.Load(ctx, finishProgressionSource{
				progression.ExperienceTablePath: "100 250 500\n",
				progression.SkillPointTablePath: "[sp table]\n1 0\n2 30\n3 30\n4 40\n[/sp table]\n[tp table]\n50 1\n[/tp table]\n",
				progression.QuestParameterPath:  "[difficulty]\n`N` 100\n`G` 30\n`10` 708\n[/difficulty]\n[exp reward table]\n200 -1\n[gold reward table]\n10 -1\n[green level penalty]\n80\n[grey level penalty]\n30\n",
			})
			if err != nil {
				t.Fatal(err)
			}
			catalog := finishOwnerCatalog()
			definition := catalog.byID[3145]
			definition.Difficulty = "10"
			catalog.byID[3145] = definition
			catalog.ordered[0] = definition
			input := finishOwnerInput(tables, finishOwnerAllocator(nil))
			input.ExperienceBonusPercent = 20
			before, _, err := repos.Character.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			before.Stats["experience_potion_quest_basis_points"] = 1000
			before.Stats["experience_potion_quest_until_ms"] = input.CommittedAt.Add(time.Minute).UnixMilli()
			if err := repos.Character.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			beforeQuest, _, _ := repos.Quest.Load(ctx, "19")
			beforeSkill, _, _ := repos.Skill.Load(ctx, "19")
			beforeInventory, _, _ := repos.Inventory.Load(ctx, "19")
			group := repos
			injected := errors.New("two-digit late skill save failure")
			if failSave {
				group.CharacterSettlement = difficultyBonusFailUOW{repos.CharacterSettlement, injected}
			}
			owner, err := NewOwner(group)
			if err != nil {
				t.Fatal(err)
			}
			first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if failSave {
				if !errors.Is(err, injected) || first.AtomicCommitted {
					t.Fatalf("failed settlement=%+v err=%v", first, err)
				}
				character, _, _ := repos.Character.Load(ctx, "19")
				quests, _, _ := repos.Quest.Load(ctx, "19")
				skill, _, _ := repos.Skill.Load(ctx, "19")
				inventory, _, _ := repos.Inventory.Load(ctx, "19")
				if !reflect.DeepEqual(character, before) || !reflect.DeepEqual(quests, beforeQuest) ||
					!reflect.DeepEqual(skill, beforeSkill) || !reflect.DeepEqual(inventory, beforeInventory) {
					t.Fatalf("late save failure changed persisted settlement aggregates")
				}
				owner, err = NewOwner(repos)
				if err != nil {
					t.Fatal(err)
				}
				first, err = owner.ApplyFinishSettlement(ctx, catalog, input)
			}
			if err != nil {
				t.Fatal(err)
			}
			// 341 base + floor(341*20/100) + floor(341*1000/10000).
			if !first.AtomicCommitted || first.Replayed || first.BaseExperienceDelta != 341 ||
				first.ExperienceBonusDelta != 102 || first.ExperienceDelta != 443 ||
				first.NewLevel != 3 || first.NewExperience != 443 || first.SPDelta != 60 {
				t.Fatalf("two-digit bonus settlement=%+v, want base=341 bonus=102 total=443", first)
			}
			input.CommittedAt = input.CommittedAt.Add(time.Hour)
			input.ExperienceBonusPercent = 0
			replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if err != nil || !replay.Replayed || replay.BaseExperienceDelta != 341 ||
				replay.ExperienceBonusDelta != 102 || replay.ExperienceDelta != 443 || replay.NewExperience != 443 {
				t.Fatalf("expired bonus replay=%+v err=%v", replay, err)
			}
			character, _, _ := repos.Character.Load(ctx, "19")
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			if character.Level != 3 || character.Stats["exp"] != 443 || character.Stats["gold"] != 10 ||
				inventory.Slots["0:65"].Count != 2 {
				t.Fatalf("replay changed rewards: character=%+v inventory=%+v", character, inventory)
			}
		})
	}
}

type difficultyBonusFailUOW struct {
	repo.CharacterSettlementUnitOfWork
	err error
}

func (u difficultyBonusFailUOW) WithinCharacterSettlement(ctx context.Context, id string, f func(repo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		tx.Skill = difficultyBonusFailSkill{tx.Skill, u.err}
		return f(tx)
	})
}

type difficultyBonusFailSkill struct {
	repo.SkillRepository
	err error
}

func (s difficultyBonusFailSkill) Save(context.Context, repo.SkillRecord) error { return s.err }
