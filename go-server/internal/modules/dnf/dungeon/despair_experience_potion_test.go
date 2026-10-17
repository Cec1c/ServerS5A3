package dungeon

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func despairPotionFixture(t *testing.T) (dnfrepo.Group, SettlementCommand) {
	t.Helper()
	g, cmd := despairRewardFixture(t)
	cmd.UpdatedAt = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	cmd.ClearPotionBase = 10 // The other 10 EXP must not receive the potion bonus.
	c, _, err := g.Character.Load(context.Background(), cmd.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if err := experiencebuff.Record(&c, experiencebuff.Effect{
		Kind: experiencebuff.Clear, BasisPoints: 20000, DurationMillis: 60000,
	}, 7440, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return g, cmd
}

func TestDespairExperiencePotionCommitsAndReplaysAfterExpiry(t *testing.T) {
	ctx := context.Background()
	g, cmd := despairPotionFixture(t)
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.ExperienceGain != 40 || first.PotionBonus != 20 ||
		first.Character.Level != 2 || first.Character.Stats["exp"] != 130 ||
		first.Character.Stats[DespairHighestClearedStatKey] != 1 ||
		len(first.DespairItems) != 3 || first.Inventory.Slots["0:121"].ItemID != 3314 {
		t.Fatalf("incomplete tower/potion settlement: %+v", first)
	}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(2 * time.Minute)
	cmd.DespairRewards.Items[0].Stack.ItemID = 999
	cmd.ClearPotionBase = 99
	replay, err := owner.CommitSettlement(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.ExperienceGain != 40 || replay.PotionBonus != 20 ||
		!reflect.DeepEqual(replay.DespairItems, first.DespairItems) ||
		!reflect.DeepEqual(replay.Character, first.Character) ||
		!reflect.DeepEqual(replay.Skill, first.Skill) ||
		!reflect.DeepEqual(replay.Inventory, first.Inventory) {
		t.Fatalf("replay changed committed tower/potion rewards: %+v", replay)
	}
	// The next floor receives ordinary EXP after expiry and replaces the receipt.
	cmd.CompletionKey = "tower-floor-2"
	cmd.DespairClearedFloor = 2
	next, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || next.Replayed || next.ExperienceGain != 20 || next.PotionBonus != 0 ||
		next.Character.Stats["exp"] != 150 || next.Character.Stats[DespairHighestClearedStatKey] != 2 {
		t.Fatalf("next floor after expiry: %+v err=%v", next, err)
	}
}

var errDespairPotionCommit = errors.New("test failure after tower and potion writes")

type failDespairPotionCommit struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (u failDespairPotionCommit) WithinCharacterSettlement(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errDespairPotionCommit
	})
}

func TestDespairExperiencePotionFailureRollsBackAllRewards(t *testing.T) {
	ctx := context.Background()
	g, cmd := despairPotionFixture(t)
	beforeCharacter, _, _ := g.Character.Load(ctx, cmd.CharacterID)
	beforeSkill, _, _ := g.Skill.Load(ctx, cmd.CharacterID)
	beforeInventory, _, _ := g.Inventory.Load(ctx, cmd.CharacterID)
	g.CharacterSettlement = failDespairPotionCommit{g.CharacterSettlement}
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.CommitSettlement(ctx, cmd); !errors.Is(err, errDespairPotionCommit) {
		t.Fatalf("expected late transaction failure, got %v", err)
	}
	afterCharacter, _, _ := g.Character.Load(ctx, cmd.CharacterID)
	afterSkill, _, _ := g.Skill.Load(ctx, cmd.CharacterID)
	afterInventory, _, _ := g.Inventory.Load(ctx, cmd.CharacterID)
	if !reflect.DeepEqual(afterCharacter, beforeCharacter) ||
		!reflect.DeepEqual(afterSkill, beforeSkill) ||
		!reflect.DeepEqual(afterInventory, beforeInventory) {
		t.Fatal("failed settlement retained experience, tower progress, items or receipts")
	}
}
