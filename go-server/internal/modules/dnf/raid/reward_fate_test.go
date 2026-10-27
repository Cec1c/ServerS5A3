package raid

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func fateRewardTestTime() time.Time {
	return time.Date(2026, 10, 27, 2, 0, 0, 0, time.FixedZone("CST", 8*60*60))
}

func seedFateRewardCharacter(t *testing.T, group repo.Group, accountID, characterID string) {
	t.Helper()
	ctx := context.Background()
	if err := group.Character.Save(ctx, repo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Stats:       map[string]int64{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, repo.InventoryRecord{
		CharacterID: characterID,
		Slots:       map[string]repo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
}

func fateRewardCommand(accountID, characterID, instanceID string, project func(*RewardAssets) (RewardChanges, error)) RewardCommand {
	return RewardCommand{
		AccountID:     accountID,
		CharacterID:   characterID,
		InstanceID:    instanceID,
		Mode:          1,
		Phase:         0,
		Kind:          1,
		MinimumClears: 1,
		OwnedClears:   1,
		Fingerprint:   [32]byte{1},
		CompletedAt:   fateRewardTestTime(),
		RequireLedger: true,
		Project:       project,
	}
}

func TestRewardOwnerFateLedgerReceiptIsIdempotent(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	if err := group.Account.Save(ctx, repo.AccountRecord{AccountID: "account-fate", Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	seedFateRewardCharacter(t, group, "account-fate", "31")
	owner, err := NewRewardOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	now := fateRewardTestTime()
	projectCalls := 0
	var granted uint32
	cmd := fateRewardCommand("account-fate", "31", "00112233445566778899aabbccddeeff", func(assets *RewardAssets) (RewardChanges, error) {
		projectCalls++
		if assets.Ledger == nil {
			return RewardChanges{}, ErrRewardOwnerUnavailable
		}
		var creditErr error
		granted, _, creditErr = raidcurrency.Credit(assets.Ledger, 100, now)
		if creditErr != nil {
			return RewardChanges{}, creditErr
		}
		return RewardChanges{Ledger: true}, nil
	})
	first, err := owner.Grant(ctx, cmd)
	if err != nil || first.Replayed || projectCalls != 1 || granted != 100 {
		t.Fatalf("first grant result=%+v err=%v calls=%d granted=%d", first, err, projectCalls, granted)
	}
	second, err := owner.Grant(ctx, cmd)
	if err != nil || !second.Replayed || projectCalls != 1 {
		t.Fatalf("replay result=%+v err=%v calls=%d", second, err, projectCalls)
	}
	account, found, err := group.Account.Load(ctx, "account-fate")
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 100 || state.Earned != 100 || account.Metadata["keep"] != "yes" {
		t.Fatalf("state=%+v err=%v metadata=%#v", state, err, account.Metadata)
	}
	inventory, _, _ := group.Inventory.Load(ctx, "31")
	if len(inventory.Slots) != 0 {
		t.Fatalf("fate reward created a list-0 bag mirror: %#v", inventory.Slots)
	}
}

func TestRewardOwnerFateWeeklyCapIsSharedAcrossCharacters(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	if err := group.Account.Save(ctx, repo.AccountRecord{AccountID: "account-fate"}); err != nil {
		t.Fatal(err)
	}
	seedFateRewardCharacter(t, group, "account-fate", "31")
	seedFateRewardCharacter(t, group, "account-fate", "32")
	owner, err := NewRewardOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	now := fateRewardTestTime()
	grant := func(characterID, instanceID string) uint32 {
		t.Helper()
		var granted uint32
		cmd := fateRewardCommand("account-fate", characterID, instanceID, func(assets *RewardAssets) (RewardChanges, error) {
			var creditErr error
			granted, _, creditErr = raidcurrency.Credit(assets.Ledger, 1600, now)
			if creditErr != nil {
				return RewardChanges{}, creditErr
			}
			return RewardChanges{Ledger: true}, nil
		})
		if result, grantErr := owner.Grant(ctx, cmd); grantErr != nil || result.Replayed {
			t.Fatalf("grant character=%s result=%+v err=%v", characterID, result, grantErr)
		}
		return granted
	}
	if got := grant("31", "00112233445566778899aabbccdd0001"); got != 1600 {
		t.Fatalf("first character granted=%d", got)
	}
	if got := grant("32", "00112233445566778899aabbccdd0002"); got != 900 {
		t.Fatalf("second character weekly remainder=%d", got)
	}
	account, _, _ := group.Account.Load(ctx, "account-fate")
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != raidcurrency.WeeklyLimit || state.Earned != raidcurrency.WeeklyLimit {
		t.Fatalf("account state=%+v err=%v", state, err)
	}
}

func TestRewardOwnerNonFateRewardStillNeedsNoAccount(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	seedFateRewardCharacter(t, group, "account-without-row", "31")
	owner, err := NewRewardOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	cmd := RewardCommand{
		AccountID:     "account-without-row",
		CharacterID:   "31",
		InstanceID:    "00112233445566778899aabbccddeeff",
		Mode:          1,
		Phase:         0,
		Kind:          1,
		MinimumClears: 1,
		OwnedClears:   1,
		Fingerprint:   [32]byte{2},
		CompletedAt:   fateRewardTestTime(),
		Project: func(assets *RewardAssets) (RewardChanges, error) {
			if assets.Ledger != nil {
				return RewardChanges{}, ErrRewardReceipt
			}
			assets.Inventory.Slots["0:9"] = repo.ItemStack{ItemID: 1, Count: 1}
			return RewardChanges{Inventory: true}, nil
		},
	}
	result, err := owner.Grant(ctx, cmd)
	if err != nil || result.Replayed {
		t.Fatalf("legacy reward result=%+v err=%v", result, err)
	}
	inventory, _, _ := group.Inventory.Load(ctx, "31")
	if inventory.Slots["0:9"].ItemID != 1 {
		t.Fatalf("legacy inventory reward missing: %#v", inventory.Slots)
	}
	if _, found, loadErr := group.Account.Load(ctx, "account-without-row"); loadErr != nil || found {
		t.Fatalf("legacy reward unexpectedly required/created Account: found=%t err=%v", found, loadErr)
	}
}

func TestRewardOwnerFateRequiresAccountAndLedgerUOW(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	seedFateRewardCharacter(t, group, "account-fate", "31")
	owner, err := NewRewardOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	cmd := fateRewardCommand("account-fate", "31", "00112233445566778899aabbccddeeff", func(*RewardAssets) (RewardChanges, error) {
		called = true
		return RewardChanges{Ledger: true}, nil
	})
	if _, err = owner.Grant(ctx, cmd); err == nil || called {
		t.Fatalf("missing account accepted: called=%t err=%v", called, err)
	}
}
