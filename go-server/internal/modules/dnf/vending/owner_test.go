package vending

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"strconv"
	"testing"
	"time"
)

func fullConsumableVendingFixture(t *testing.T) (repo.Group, Command, time.Time) {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 27, 6, 32, 0, 0, time.FixedZone("CST", 8*60*60))
	account := repo.AccountRecord{AccountID: "vending-overflow-account", Metadata: map[string]string{
		premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
	}}
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, repo.CharacterRecord{AccountID: account.AccountID, CharacterID: "99"}); err != nil {
		t.Fatal(err)
	}
	slots := map[string]repo.ItemStack{"0:141": {ItemID: 7454, Count: 4}}
	for slot := int16(3); slot <= 8; slot++ {
		slots["0:"+strconv.Itoa(int(slot))] = repo.ItemStack{ItemID: 800000 + int64(slot), Count: 1}
	}
	for slot := int16(65); slot <= 120; slot++ {
		slots["0:"+strconv.Itoa(int(slot))] = repo.ItemStack{ItemID: 900000 + int64(slot), Count: 1}
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	cmd := Command{
		AccountID: account.AccountID, CharacterID: "99", CoinID: 7454, CoinCount: 1, CoinSlot: 141, Now: now,
		Reward: dungeon.CardItemReward{
			Stack:     repo.ItemStack{ItemID: 7433, Count: 1, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[disguise]", "source": "black_diamond_vending"}},
			Stackable: true, SlotStart: 65, SlotEnd: 120, StackLimit: 100,
		},
		OverflowToMail: true,
	}
	return repos, cmd, now
}

func TestVendingOverflowToMailCommitsCoinAndRewardTogether(t *testing.T) {
	ctx := context.Background()
	repos, cmd, _ := fullConsumableVendingFixture(t)
	result, err := Use(ctx, repos, cmd)
	if err != nil {
		t.Fatalf("Use overflow: %v", err)
	}
	if result.CoinRemaining != 3 || result.RewardSlot != -1 || result.RewardStack.ItemID != 7433 || result.RewardStack.Count != 1 || result.OverflowMailID == "" {
		t.Fatalf("overflow result=%+v", result)
	}
	inventory, found, err := repos.Inventory.Load(ctx, cmd.CharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots["0:141"].Count != 3 {
		t.Fatalf("coin stack=%+v, want 3", inventory.Slots["0:141"])
	}
	if incidentInventoryCount := func() int64 {
		var count int64
		for _, stack := range inventory.Slots {
			if stack.ItemID == 7433 {
				count += stack.Count
			}
		}
		return count
	}(); incidentInventoryCount != 0 {
		t.Fatalf("overflow reward unexpectedly occupied inventory: %+v", inventory.Slots)
	}
	mailbox, found, err := repos.Mailbox.Load(ctx, cmd.CharacterID)
	if err != nil || !found || len(mailbox.Mails) != 1 {
		t.Fatalf("overflow mailbox found=%t err=%v mails=%+v", found, err, mailbox.Mails)
	}
	mail := mailbox.Mails[result.OverflowMailID]
	if mail.Metadata["source"] != "black_diamond_vending_inventory_full" || len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 7433 || mail.Attachments[0].Count != 1 {
		t.Fatalf("overflow mail=%+v", mail)
	}
}

// The current CMD215 client treats an embedded settlement slot as a real
// inventory key. The bridge therefore leaves OverflowToMail disabled: a full
// page must transactionally reject without debiting the coin or creating mail.
func TestVendingFullPageWithoutOverflowRejectsWithoutDebitOrMail(t *testing.T) {
	ctx := context.Background()
	repos, cmd, _ := fullConsumableVendingFixture(t)
	cmd.OverflowToMail = false
	result, err := Use(ctx, repos, cmd)
	if !errors.Is(err, dungeon.ErrInventoryFull) {
		t.Fatalf("Use full page error=%v, want ErrInventoryFull", err)
	}
	if result.CoinRemaining != 0 || result.RewardSlot != 0 || result.RewardStack.ItemID != 0 || result.RewardStack.Count != 0 {
		t.Fatalf("rejected full page exposed a settlement result: %+v", result)
	}
	inventory, found, err := repos.Inventory.Load(ctx, cmd.CharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots["0:141"].Count != 4 {
		t.Fatalf("rejected full page consumed coin: %+v", inventory.Slots["0:141"])
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID == 7433 {
			t.Fatalf("rejected full page placed reward: %+v", stack)
		}
	}
	mailbox, found, err := repos.Mailbox.Load(ctx, cmd.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if found && len(mailbox.Mails) != 0 {
		t.Fatalf("rejected full page created mail: %+v", mailbox.Mails)
	}
}

type rejectingVendingMailboxRepository struct {
	repo.MailboxRepository
}

func (rejectingVendingMailboxRepository) Save(context.Context, repo.MailboxRecord) error {
	return errors.New("injected vending mailbox save failure")
}

func (r rejectingVendingMailboxRepository) SaveFields(ctx context.Context, record repo.MailboxRecord, fields ...repo.MailboxField) error {
	return errors.New("injected vending mailbox save failure")
}

type rejectingVendingMailboxAssets struct {
	repo.MailboxAssetUnitOfWork
}

func (u rejectingVendingMailboxAssets) WithinMailboxAssets(
	ctx context.Context,
	assetCharacterID string,
	mailboxCharacterID string,
	apply func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error,
) error {
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, assetCharacterID, mailboxCharacterID, func(
		characters repo.CharacterRepository,
		inventory repo.InventoryRepository,
		mailbox repo.MailboxRepository,
	) error {
		return apply(characters, inventory, rejectingVendingMailboxRepository{MailboxRepository: mailbox})
	})
}

func TestVendingOverflowMailFailureRollsBackCoinAndAttachment(t *testing.T) {
	ctx := context.Background()
	repos, cmd, _ := fullConsumableVendingFixture(t)
	repos.MailboxAssets = rejectingVendingMailboxAssets{MailboxAssetUnitOfWork: repos.MailboxAssets}
	if _, err := Use(ctx, repos, cmd); err == nil {
		t.Fatal("overflow mail failure was reported as success")
	}
	inventory, found, err := repos.Inventory.Load(ctx, cmd.CharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots["0:141"].Count != 4 {
		t.Fatalf("failed overflow consumed coin: %+v", inventory.Slots["0:141"])
	}
	mailbox, found, err := repos.Mailbox.Load(ctx, cmd.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if found && len(mailbox.Mails) != 0 {
		t.Fatalf("failed overflow left a mail: %+v", mailbox.Mails)
	}
}

func TestVendingRequiresBlackDiamondAndRollsBackCoinWhenRewardCannotFit(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Now()
	account := repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{}}
	repos.Account.Save(ctx, account)
	repos.Character.Save(ctx, repo.CharacterRecord{AccountID: account.AccountID, CharacterID: "99"})
	repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:10": {ItemID: 7454, Count: 2}, "0:11": {ItemID: 7000, Count: 1}}})
	cmd := Command{AccountID: account.AccountID, CharacterID: "99", CoinID: 7454, CoinCount: 1, CoinSlot: 10, Now: now, Reward: dungeon.CardItemReward{Stack: repo.ItemStack{ItemID: 7463, Count: 4}, Stackable: true, SlotStart: 11, SlotEnd: 11}}
	if _, e := Use(ctx, repos, cmd); !errors.Is(e, ErrBlackDiamondRequired) {
		t.Fatal("ordinary account used black diamond machine")
	}
	account.Metadata[premium.MetadataKey(premium.TypeBlackDiamond)] = strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	repos.Account.Save(ctx, account)
	if _, e := Use(ctx, repos, cmd); e == nil {
		t.Fatal("full destination did not reject")
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "99")
	if inventory.Slots["0:10"].Count != 2 {
		t.Fatal("failed reward consumed coin")
	}
	delete(inventory.Slots, "0:11")
	repos.Inventory.Save(ctx, inventory)
	result, e := Use(ctx, repos, cmd)
	if e != nil {
		t.Fatal(e)
	}
	inventory, _, _ = repos.Inventory.Load(ctx, "99")
	if result.CoinRemaining != 1 || result.RewardSlot != 11 || inventory.Slots["0:11"].ItemID != 7463 || inventory.Slots["0:11"].Count != 4 {
		t.Fatalf("incorrect reward commit: %+v %+v", result, inventory)
	}
	if result.RewardStack.ItemID != 7463 || result.RewardStack.Count != 4 {
		t.Fatalf("missing committed settlement row: %+v", result)
	}
	second, e := Use(ctx, repos, cmd)
	if e != nil {
		t.Fatal(e)
	}
	inventory, _, _ = repos.Inventory.Load(ctx, "99")
	if second.CoinRemaining != 0 || second.RewardSlot != 11 || second.RewardStack.Count != 8 || len(inventory.Slots) != 1 || inventory.Slots["0:11"].Count != 8 || result.RewardStack.Count != 4 {
		t.Fatalf("second settlement must carry total, preserve first snapshot and remove last coin: first=%+v second=%+v inventory=%+v", result, second, inventory)
	}
}
