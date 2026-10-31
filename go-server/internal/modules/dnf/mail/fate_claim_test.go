package mail

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func fateClaimTestNow() time.Time {
	return time.Date(2026, time.October, 27, 2, 0, 0, 0, time.FixedZone("CST", 8*60*60))
}

func fateClaimTestIDs(t *testing.T, ctx context.Context, owner Owner, mailID string) []uint32 {
	t.Helper()
	if _, err := owner.Open(ctx, "77"); err != nil {
		t.Fatal(err)
	}
	box, found, err := owner.repos.Mailbox.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load mailbox found=%t err=%v", found, err)
	}
	mail, ok := box.Mails[mailID]
	if !ok {
		t.Fatalf("mail %s missing", mailID)
	}
	ids, stored, err := mailboxStoredClaimIDs(mail)
	if err != nil {
		t.Fatalf("claim ids=%v stored=%t err=%v", ids, stored, err)
	}
	if !stored {
		parent, wireErr := mailboxWireID(mail.MailID)
		if wireErr != nil || parent == 0 {
			t.Fatalf("fallback claim id for mail %s: parent=%d err=%v", mailID, parent, wireErr)
		}
		return []uint32{parent}
	}
	if len(ids) == 0 {
		t.Fatalf("stored claim ids are empty for mail %s", mailID)
	}
	return ids
}

func seedFateClaimOwner(t *testing.T, ctx context.Context, group dnfrepo.Group, now time.Time) Owner {
	t.Helper()
	if err := group.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-fate"}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-fate"}); err != nil {
		t.Fatal(err)
	}
	owner := NewOwner(group)
	owner.now = func() time.Time { return now }
	return owner
}

func TestMailboxFateMultipleAttachmentsCreditAccountAndHandlerFact(t *testing.T) {
	// This case calls NewHandler, which constructs its own wall-clock Owner.
	// Keep setup, handler and wallet verification in one deterministic clock;
	// a fixed setup date alone becomes an invalid past week after rollover.
	synctest.Test(t, testMailboxFateMultipleAttachmentsCreditAccountAndHandlerFact)
}

func testMailboxFateMultipleAttachmentsCreditAccountAndHandlerFact(t *testing.T) {
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := time.Now()
	owner := seedFateClaimOwner(t, ctx, group, now)
	if _, err := dnfrepo.AppendSystemMail(ctx, group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "77",
		Source:               "raid_reward_overflow",
		CreatedAt:            now,
		Attachments: []dnfrepo.MailAttachment{
			{ItemID: int64(raidcurrency.FateItemID), Count: 30},
			{ItemID: int64(raidcurrency.FateItemID), Count: 45},
			{ItemID: int64(raidcurrency.FateItemID), Count: 60},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ids := fateClaimTestIDs(t, ctx, owner, "1")
	if len(ids) != 3 || ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatalf("claim ids=%v, want three stable attachment identities", ids)
	}

	result, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        group,
		Body:                currentMailIDList(ids...),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.FateCurrencyChanged {
		t.Fatal("handler did not publish the committed Fate balance change")
	}
	if len(result.UpperResponses) != 1 ||
		result.UpperResponses[0].MsgID != uint16(dnfenum.CmdPacketMailboxExtractItem) {
		t.Fatalf("mail claim must update existing native objects without rebuilding managers: %+v", result.UpperResponses)
	}
	ack := result.UpperResponses[0].Body
	if len(ack) != 5+3*12 || ack[0] != 1 || binary.LittleEndian.Uint32(ack[1:5]) != 3 {
		t.Fatalf("multi-attachment ACK=%x, want three accepted claim objects", ack)
	}
	account, found, err := group.Account.Load(ctx, "account-fate")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 135 || state.Earned != 135 {
		t.Fatalf("Fate state=%+v err=%v, want balance/earned 135", state, err)
	}
	if inventory, found, err := group.Inventory.Load(ctx, "77"); err != nil {
		t.Fatal(err)
	} else if found && len(inventory.Slots) != 0 {
		t.Fatalf("Fate attachments occupied inventory rows: %+v", inventory.Slots)
	}
}

func TestMailboxFateFullBagClaimsWithoutSlots(t *testing.T) {
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := fateClaimTestNow()
	owner := seedFateClaimOwner(t, ctx, group, now)
	slots := make(map[string]dnfrepo.ItemStack)
	for slot := int16(3); slot <= 359; slot++ {
		slots[mailboxSlotKey(dnfrepo.MainInventoryListType, slot)] = dnfrepo.ItemStack{
			ItemID: 900000 + int64(slot),
			Count:  1,
		}
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	before, _, err := group.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dnfrepo.AppendSystemMail(ctx, group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "77",
		Source:               "magic_box_reward_inventory_full",
		CreatedAt:            now,
		Attachments:          []dnfrepo.MailAttachment{{ItemID: int64(raidcurrency.FateItemID), Count: 6}},
	}); err != nil {
		t.Fatal(err)
	}
	ids := fateClaimTestIDs(t, ctx, owner, "1")
	result, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if result.InventoryFull || len(result.ItemSlotRefreshes) != 0 || !result.FateCurrencyChanged {
		t.Fatalf("claim result=%+v, want a slotless successful Fate claim", result)
	}
	after, _, err := group.Inventory.Load(ctx, "77")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("full inventory changed: before=%+v after=%+v err=%v", before, after, err)
	}
	account, _, _ := group.Account.Load(ctx, "account-fate")
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 6 || state.Earned != 6 {
		t.Fatalf("Fate state=%+v err=%v", state, err)
	}
}

func TestMailboxFateAndOrdinaryStackableShareAtomicClaim(t *testing.T) {
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := fateClaimTestNow()
	owner := seedFateClaimOwner(t, ctx, group, now)
	if _, err := dnfrepo.AppendSystemMail(ctx, group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "77",
		Source:               "magic_box_reward_inventory_full",
		CreatedAt:            now,
		Attachments: []dnfrepo.MailAttachment{
			{ItemID: int64(raidcurrency.FateItemID), Count: 40},
			{ItemID: 1001, Count: 2},
			{ItemID: int64(raidcurrency.FateItemID), Count: 5},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ids := fateClaimTestIDs(t, ctx, owner, "1")
	resolverOwner := owner
	resolverOwner.itemResolver = mailboxClaimTestResolver(65, 120, 99)
	result, err := resolverOwner.Claim(ctx, "77", ExtractRequest{MailIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if !result.FateCurrencyChanged || result.InventoryFull || len(result.ItemSlotRefreshes) != 1 {
		t.Fatalf("mixed claim result=%+v", result)
	}
	account, _, _ := group.Account.Load(ctx, "account-fate")
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 45 || state.Earned != 45 {
		t.Fatalf("Fate state=%+v err=%v", state, err)
	}
	inventory, _, err := group.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, stack := range inventory.Slots {
		if stack.ItemID == 1001 && stack.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("ordinary stackable missing from atomic claim: %+v", inventory.Slots)
	}
}

func TestMailboxFateWeeklyCapAndDuplicateClaimDoNotDoubleCredit(t *testing.T) {
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := fateClaimTestNow()
	account := dnfrepo.AccountRecord{AccountID: "account-fate"}
	if granted, _, err := raidcurrency.Credit(&account, 2490, now); err != nil || granted != 2490 {
		t.Fatalf("seed Fate quota granted=%d err=%v", granted, err)
	}
	if err := group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-fate"}); err != nil {
		t.Fatal(err)
	}
	owner := NewOwner(group)
	owner.now = func() time.Time { return now }
	_, err := dnfrepo.AppendSystemMail(ctx, group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "77",
		Source:               "raid_reward_overflow",
		CreatedAt:            now,
		Attachments: []dnfrepo.MailAttachment{
			{ItemID: int64(raidcurrency.FateItemID), Count: 5},
			{ItemID: int64(raidcurrency.FateItemID), Count: 10},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := fateClaimTestIDs(t, ctx, owner, "1")
	first, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: firstIDs})
	if err != nil {
		t.Fatal(err)
	}
	if !first.FateCurrencyChanged {
		t.Fatal("first claim should consume the remaining weekly quota")
	}
	account, _, _ = group.Account.Load(ctx, "account-fate")
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 2500 || state.Earned != 2500 {
		t.Fatalf("capped state=%+v err=%v, want 2500/2500", state, err)
	}

	secondID, err := dnfrepo.AppendSystemMail(ctx, group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "77",
		Source:               "raid_reward_overflow",
		CreatedAt:            now,
		Attachments:          []dnfrepo.MailAttachment{{ItemID: int64(raidcurrency.FateItemID), Count: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondIDs := fateClaimTestIDs(t, ctx, owner, secondID)
	second, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: secondIDs})
	if err != nil {
		t.Fatal(err)
	}
	if second.FateCurrencyChanged {
		t.Fatal("capped claim must not request a balance projection")
	}
	box, _, _ := group.Mailbox.Load(ctx, "77")
	if settled := box.Mails[secondID]; !settled.Claimed || len(settled.Attachments) != 0 {
		t.Fatalf("capped Fate receipt was not settled: %+v", settled)
	}
	if _, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: firstIDs}); err == nil {
		t.Fatal("duplicate claim succeeded")
	}
	account, _, _ = group.Account.Load(ctx, "account-fate")
	state, err = raidcurrency.Read(account, now)
	if err != nil || state.Balance != 2500 || state.Earned != 2500 {
		t.Fatalf("duplicate changed Fate state=%+v err=%v", state, err)
	}
}

type fateClaimRejectAssets struct{ dnfrepo.MailboxAssetUnitOfWork }

func (u fateClaimRejectAssets) WithinAccountTradeMailbox(
	ctx context.Context,
	accountID string,
	characterIDs []string,
	mailboxCharacterID string,
	apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error,
) error {
	base, ok := u.MailboxAssetUnitOfWork.(dnfrepo.AccountTradeMailboxUnitOfWork)
	if !ok {
		return dnfrepo.ErrMailboxAssetTransactionUnavailable
	}
	return base.WithinAccountTradeMailbox(ctx, accountID, characterIDs, mailboxCharacterID, func(
		accounts dnfrepo.AccountRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		mailboxes dnfrepo.MailboxRepository,
	) error {
		if err := apply(accounts, characters, inventories, mailboxes); err != nil {
			return err
		}
		return errors.New("fate mail commit failure fixture")
	})
}

func TestMailboxFateCommitFailureRollsBackAccountAndMail(t *testing.T) {
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := fateClaimTestNow()
	owner := seedFateClaimOwner(t, ctx, group, now)
	if _, err := dnfrepo.AppendSystemMail(ctx, group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "77",
		Source:               "raid_reward_overflow",
		CreatedAt:            now,
		Attachments:          []dnfrepo.MailAttachment{{ItemID: int64(raidcurrency.FateItemID), Count: 20}},
	}); err != nil {
		t.Fatal(err)
	}
	ids := fateClaimTestIDs(t, ctx, owner, "1")
	beforeAccount, _, _ := group.Account.Load(ctx, "account-fate")
	beforeMailbox, _, _ := group.Mailbox.Load(ctx, "77")
	broken := group
	broken.MailboxAssets = fateClaimRejectAssets{MailboxAssetUnitOfWork: group.MailboxAssets}
	if _, err := NewOwner(broken).Claim(ctx, "77", ExtractRequest{MailIDs: ids}); err == nil {
		t.Fatal("commit failure returned success")
	}
	afterAccount, _, _ := group.Account.Load(ctx, "account-fate")
	afterMailbox, _, _ := group.Mailbox.Load(ctx, "77")
	if !reflect.DeepEqual(beforeAccount, afterAccount) || !reflect.DeepEqual(beforeMailbox, afterMailbox) {
		t.Fatal("Fate account credit and mail settlement did not roll back together")
	}
	result, err := NewOwner(group).Claim(ctx, "77", ExtractRequest{MailIDs: ids})
	if err != nil || !result.FateCurrencyChanged {
		t.Fatalf("retry after rollback result=%+v err=%v", result, err)
	}
}

func TestMailboxFateClaimDoesNotConsolidateHistoricalInventoryRows(t *testing.T) {
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := fateClaimTestNow()
	owner := seedFateClaimOwner(t, ctx, group, now)
	legacy := dnfrepo.ItemStack{ItemID: int64(raidcurrency.FateItemID), Count: 100}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{"0:50": legacy},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dnfrepo.AppendSystemMail(ctx, group.Mailbox, dnfrepo.SystemMailDelivery{
		RecipientCharacterID: "77",
		Source:               "dnf90-gm-tool",
		CreatedAt:            now,
		Attachments:          []dnfrepo.MailAttachment{{ItemID: int64(raidcurrency.FateItemID), Count: 20}},
	}); err != nil {
		t.Fatal(err)
	}
	ids := fateClaimTestIDs(t, ctx, owner, "1")
	result, err := owner.Claim(ctx, "77", ExtractRequest{MailIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if !result.FateCurrencyChanged || len(result.ItemSlotRefreshes) != 0 {
		t.Fatalf("claim result=%+v", result)
	}
	account, _, _ := group.Account.Load(ctx, "account-fate")
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 20 || state.Earned != 20 {
		t.Fatalf("Fate state=%+v err=%v, mail must use Credit rather than Consolidate", state, err)
	}
	inventory, _, err := group.Inventory.Load(ctx, "77")
	if err != nil || inventory.Slots["0:50"].Count != 100 {
		t.Fatalf("historical inventory row changed: inventory=%+v err=%v", inventory.Slots, err)
	}
}
