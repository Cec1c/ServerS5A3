package dungeon

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestGriefSettlementCommitsFloorWithExperienceAndReceipt(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.GriefClearedFloor = 1
	cmd.GriefRewards = &GriefRewards{}
	owner, _ := NewOwner(repos)
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || first.Character.Stats[GriefHighestClearedStatKey] != 1 || first.Replayed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Character, first.Character) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	cmd.CompletionKey = "grief-next"
	cmd.GriefClearedFloor = 3
	if _, err := owner.CommitSettlement(ctx, cmd); !errors.Is(err, ErrSettlementStateInvalid) {
		t.Fatalf("skip err=%v", err)
	}
	afterSkip, _, _ := repos.Character.Load(ctx, "c")
	if !reflect.DeepEqual(afterSkip, first.Character) {
		t.Fatal("rejected skipped floor changed experience, progress or receipt")
	}
	cmd.GriefClearedFloor = 2
	second, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || second.Character.Stats[GriefHighestClearedStatKey] != 2 || second.Replayed {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, present := second.Character.Stats[DespairHighestClearedStatKey]; present || len(second.DespairItems) != 0 {
		t.Fatal("grief clear acquired despair progress or rewards")
	}
}

func TestGriefSettlementRejectsInvalidAndConflictingTowerCommands(t *testing.T) {
	for _, tc := range []struct {
		name           string
		grief, despair int
		despairRewards bool
	}{
		{"negative", -1, 0, false},
		{"past last", 101, 0, false},
		{"two towers", 1, 1, false},
		{"despair rewards", 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos, cmd := luckyStarSettlementFixture(t, "0")
			before, _, _ := repos.Character.Load(context.Background(), "c")
			cmd.GriefClearedFloor, cmd.DespairClearedFloor = tc.grief, tc.despair
			if tc.despairRewards {
				cmd.DespairRewards = &DespairRewards{}
			}
			owner, _ := NewOwner(repos)
			if _, err := owner.CommitSettlement(context.Background(), cmd); !errors.Is(err, ErrSettlementStateInvalid) {
				t.Fatalf("err=%v", err)
			}
			after, _, _ := repos.Character.Load(context.Background(), "c")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid command modified character")
			}
		})
	}
}

func TestGriefSettlementSaveFailureRollsBackProgressExperienceAndReceipt(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.GriefClearedFloor = 1
	cmd.GriefRewards = &GriefRewards{}
	before, _, _ := repos.Character.Load(ctx, "c")
	// The skill save fails after the character write, so rollback must undo
	// both the floor advance and the completion receipt written on that row.
	repos.CharacterSettlement = griefFailSkillUOW{repos.CharacterSettlement}
	owner, _ := NewOwner(repos)
	if _, err := owner.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("failed save accepted")
	}
	after, _, _ := repos.Character.Load(ctx, "c")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed save retained progress, exp or completion receipt")
	}
}

type griefFailSkillStore struct{ dnfrepo.SkillRepository }

func (griefFailSkillStore) Save(context.Context, dnfrepo.SkillRecord) error {
	return errors.New("grief settlement skill save failed")
}

type griefFailSkillUOW struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (u griefFailSkillUOW) WithinCharacterSettlement(ctx context.Context, id string, f func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		tx.Skill = griefFailSkillStore{tx.Skill}
		return f(tx)
	})
}
