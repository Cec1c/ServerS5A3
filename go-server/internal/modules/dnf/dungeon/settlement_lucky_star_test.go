package dungeon

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func luckyStarSettlementFixture(t *testing.T, points string) (repo.Group, SettlementCommand) {
	t.Helper()
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	tables, err := progression.Load(ctx, settlementTestSource())
	if err != nil {
		t.Fatal(err)
	}
	records := []error{
		repositories.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{LuckyStarMetadataKey: points, "keep": "value"}}),
		repositories.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "c", Level: 1, Stats: map[string]int64{"exp": 90}}),
		repositories.Skill.Save(ctx, repo.SkillRecord{CharacterID: "c", Points: repo.SkillPointState{SyncedLevel: 1, TotalSP: 10, RemainingSP: 10}}),
		repositories.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "c", Slots: map[string]repo.ItemStack{}}),
	}
	for _, err := range records {
		if err != nil {
			t.Fatal(err)
		}
	}
	return repositories, SettlementCommand{AccountID: "a", CharacterID: "c", CompletionKey: "clear-1", Tables: tables, Experience: 20, RecommendedDungeonClear: true, LuckyStar: &LuckyStarPolicy{Limit: 400, Amount: 1}}
}

func TestSettlementLuckyStarAwardIsAtomicReceiptedAndPVFCapped(t *testing.T) {
	for _, row := range []struct {
		name, points, want string
		before, after      uint32
	}{{"first", "0", "1", 0, 1}, {"last", "399", "400", 399, 400}, {"full", "400", "400", 400, 400}} {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			repos, cmd := luckyStarSettlementFixture(t, row.points)
			owner, _ := NewOwner(repos)
			result, err := owner.CommitSettlement(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			want := LuckyStarResult{Resolved: true, Before: row.before, After: row.after, Limit: 400}
			if result.LuckyStar != want || result.Character.Level != 2 {
				t.Fatalf("result=%+v", result)
			}
			replay, err := owner.CommitSettlement(ctx, cmd)
			if err != nil || !replay.Replayed || replay.LuckyStar != want {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			account, _, _ := repos.Account.Load(ctx, "a")
			if account.Metadata[LuckyStarMetadataKey] != row.want || account.Metadata["keep"] != "value" {
				t.Fatalf("account=%+v", account.Metadata)
			}
		})
	}
}

type failingLuckyAccount struct{ repo.AccountRepository }

func (r failingLuckyAccount) Save(context.Context, repo.AccountRecord) error {
	return errors.New("test account failure")
}

type failingLuckyUOW struct {
	repo.CharacterSettlementUnitOfWork
}

func (u failingLuckyUOW) WithinCharacterSettlement(ctx context.Context, id string, fn func(repo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error { tx.Account = failingLuckyAccount{tx.Account}; return fn(tx) })
}

func TestSettlementLuckyStarFailureRollsBackProgressionAndReceipt(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	repos.CharacterSettlement = failingLuckyUOW{repos.CharacterSettlement}
	owner, _ := NewOwner(repos)
	if _, err := owner.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("write failure accepted")
	}
	character, _, _ := repos.Character.Load(ctx, "c")
	account, _, _ := repos.Account.Load(ctx, "a")
	if character.Level != 1 || character.Stats["exp"] != 90 || account.Metadata[LuckyStarMetadataKey] != "0" {
		t.Fatal("partial settlement committed")
	}
	if _, present := character.Stats[luckyStarReceiptPrefix+"resolved"]; present {
		t.Fatal("failed receipt committed")
	}
}

func TestSettlementLuckyStarAbsentPolicyAndInvalidBalancesDoNotAward(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	owner, _ := NewOwner(repos)
	result, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || result.LuckyStar.Resolved {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, balance := range []string{"401", "bad", "-1"} {
		repos, cmd := luckyStarSettlementFixture(t, balance)
		owner, _ := NewOwner(repos)
		if _, err := owner.CommitSettlement(ctx, cmd); err == nil {
			t.Fatalf("invalid balance %s accepted", balance)
		}
	}
}
