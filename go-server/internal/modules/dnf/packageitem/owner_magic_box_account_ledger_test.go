package packageitem

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type staleMagicBoxAccountStore struct {
	base          dnfrepo.AccountRepository
	stale         dnfrepo.AccountRecord
	fullSaveCalls int
}

func (s *staleMagicBoxAccountStore) Load(context.Context, string) (dnfrepo.AccountRecord, bool, error) {
	return dnfrepo.CloneAccount(s.stale), true, nil
}

func (s *staleMagicBoxAccountStore) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	s.fullSaveCalls++
	return s.base.Save(ctx, record)
}

func TestOwnerSeriaMagicBoxUsesAccountLedgerInsteadOfStaleOutsideSnapshot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "acc",
		State:     "active",
		Metadata:  map[string]string{currentSeriaLuckMetadataKey: "7"},
	}); err != nil {
		t.Fatal(err)
	}
	saveMagicBoxTestCharacter(t, ctx, repos, "acc")
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:67": {ItemID: currentSeriaLuckItemID, Count: 2, Extra: map[string]string{"item_kind": "stackable"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	stale, found, err := repos.Account.Load(ctx, "acc")
	if err != nil || !found {
		t.Fatalf("load stale account found=%t err=%v", found, err)
	}
	// Model a Fate ledger commit after the stale outside-transaction snapshot.
	live := dnfrepo.CloneAccount(stale)
	live.Metadata["raid_fate_balance_v1"] = "777"
	live.Metadata["raid_fate_week_v1"] = "2943"
	live.Metadata["raid_fate_earned_v1"] = "345"
	if err := repos.Account.Save(ctx, live); err != nil {
		t.Fatal(err)
	}
	account := &staleMagicBoxAccountStore{base: repos.Account, stale: stale}
	repos.Account = account
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	drawCalls := 0
	owner.drawRoll = func(total int64) int64 {
		drawCalls++
		if drawCalls == 1 {
			return 0
		}
		return total - 1
	}
	result, err := owner.ApplyMagicBoxExpand(ctx, MagicBoxExpandCommand{
		SelectedCharacterID: 77,
		AccountID:           "acc",
		ListType:            listTypeMain,
		SlotIndex:           67,
		BoxItemID:           uint32(currentSeriaLuckItemID),
		MaterialSlotIndex:   -1,
		OpenCount:           2,
	}, staticMagicBoxResolver(alignedcmd.MagicBoxResolution{
		Kind: "random",
		Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{
			{ItemID: 30, Count: 1, Weight: 1},
			{ItemID: 2600014, Count: 2, Weight: 99},
		}}},
	}), staticMagicBoxRewardResolver(map[int64]alignedcmd.MagicBoxRewardItem{
		30:      magicBoxContractRewardItem(),
		2600014: {ItemID: 2600014, Kind: "stackable", SlotStart: 65, SlotEnd: 120},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if drawCalls != 2 || !result.Success || len(result.ContractActivations) != 1 || !result.SeriaLuckDoubleTriggered || result.SeriaLuckBefore != 7 || result.SeriaLuckAfter != 1 {
		t.Fatalf("result=%+v", result)
	}
	if account.fullSaveCalls != 0 {
		t.Fatalf("stale outside-transaction account was saved %d times", account.fullSaveCalls)
	}
	after, found, err := account.base.Load(ctx, "acc")
	if err != nil || !found {
		t.Fatalf("load committed account found=%t err=%v", found, err)
	}
	if after.Metadata[currentSeriaLuckMetadataKey] != "1" ||
		after.Metadata["raid_fate_balance_v1"] != "777" ||
		after.Metadata["raid_fate_week_v1"] != "2943" ||
		after.Metadata["raid_fate_earned_v1"] != "345" {
		t.Fatalf("account metadata after ledger commit=%#v", after.Metadata)
	}
}

var errMagicBoxCommitBoundary = errors.New("injected magic-box commit boundary failure")

func TestOwnerMagicBoxSelectsAccountLedgerBeforeDrawAndNeverRerolls(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveMagicBoxContractAccount(t, ctx, repos, 0)
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: magicBoxBaseSlots()}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	drawCalls := 0
	owner.drawRoll = func(total int64) int64 {
		drawCalls++
		if drawCalls == 1 {
			return 0 // first entry is the contract
		}
		return total - 1 // a retry would select the non-contract tail
	}
	result, err := owner.ApplyMagicBox(
		ctx,
		magicBoxContractTestCommand(),
		staticMagicBoxResolver(alignedcmd.MagicBoxResolution{
			Kind: "random",
			Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{
				{ItemID: 30, Count: 1, Weight: 1},
				{ItemID: 2600014, Count: 1, Weight: 99},
			}}},
			MaterialItemID: 10007367, MaterialCountPerUse: 1,
		}),
		staticMagicBoxRewardResolver(map[int64]alignedcmd.MagicBoxRewardItem{
			30:      magicBoxContractRewardItem(),
			2600014: {ItemID: 2600014, Kind: "stackable", SlotStart: 65, SlotEnd: 120},
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if drawCalls != 1 {
		t.Fatalf("magic-box draw count=%d want exactly 1", drawCalls)
	}
	if !result.Success || len(result.ContractActivations) != 1 || result.ContractActivations[0].ItemID != 30 {
		t.Fatalf("first contract draw was not preserved: %+v", result)
	}
	account, found, err := repos.Account.Load(ctx, magicBoxContractTestAccountID)
	if err != nil || !found || premium.ExpireAt(account, premium.TypeOverEquip) == 0 {
		t.Fatalf("contract activation not committed: found=%t account=%+v err=%v", found, account, err)
	}
	inventory, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID == 2600014 {
			t.Fatalf("reroll tail was granted: %+v", inventory.Slots)
		}
	}
}

type rejectingMagicBoxLedger struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	dnfrepo.AccountCharacterLedgerMailboxAssetUnitOfWork
}

func (u rejectingMagicBoxLedger) WithinAccountCharacterLedgerMailboxAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountRepository, dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.MailboxRepository) error,
) error {
	return u.AccountCharacterLedgerMailboxAssetUnitOfWork.WithinAccountCharacterLedgerMailboxAssets(
		ctx,
		accountID,
		characterID,
		func(accounts dnfrepo.AccountRepository, accountInventory dnfrepo.AccountInventoryRepository, characters dnfrepo.CharacterRepository, inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository, mailboxes dnfrepo.MailboxRepository) error {
			if err := apply(accounts, accountInventory, characters, inventory, equipment, mailboxes); err != nil {
				return err
			}
			return errMagicBoxCommitBoundary
		},
	)
}

func TestOwnerMagicBoxAccountAndInventoryRollbackTogether(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveMagicBoxContractAccount(t, ctx, repos, 0)
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       magicBoxBaseSlots(),
	}); err != nil {
		t.Fatal(err)
	}
	ledger, ok := repos.AccountAssets.(dnfrepo.AccountCharacterLedgerMailboxAssetUnitOfWork)
	if !ok {
		t.Fatal("memory account assets do not implement the ledger mailbox UOW")
	}
	repos.AccountAssets = rejectingMagicBoxLedger{
		AccountCharacterAssetUnitOfWork:              repos.AccountAssets,
		AccountCharacterLedgerMailboxAssetUnitOfWork: ledger,
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.ApplyMagicBox(
		ctx,
		magicBoxContractTestCommand(),
		staticMagicBoxResolver(magicBoxContractTestResolution(30, true)),
		staticMagicBoxRewardResolver(map[int64]alignedcmd.MagicBoxRewardItem{30: magicBoxContractRewardItem()}),
	)
	if !errors.Is(err, errMagicBoxCommitBoundary) {
		t.Fatalf("ApplyMagicBox error=%v want injected rollback", err)
	}
	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if box := inventory.Slots["0:10"]; box.ItemID != 10007368 || box.Count != 1 {
		t.Fatalf("box consumption was not rolled back: %+v", inventory.Slots)
	}
	if material := inventory.Slots["0:121"]; material.ItemID != 10007367 || material.Count != 3 {
		t.Fatalf("material consumption was not rolled back: %+v", inventory.Slots)
	}
	account, found, err := repos.Account.Load(ctx, magicBoxContractTestAccountID)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if premium.ExpireAt(account, premium.TypeOverEquip) != 0 {
		t.Fatalf("account contract committed after injected failure: %#v", account.Metadata)
	}
}
