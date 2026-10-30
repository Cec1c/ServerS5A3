package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	ceraPackageFateTestSourceID uint32 = 900
	ceraPackageFateTestReward   uint32 = 30
	ceraPackageFateTestOtherID  uint32 = 600
)

func ceraPackageFateTestNow() time.Time {
	// The production cera-package owner currently uses time.Now directly. Keep
	// the fixture's cap and projection reads in the same week as that commit.
	return time.Now().UTC()
}

func mustCurrentCeraPackageFateCatalog(t *testing.T, includeOther bool) *pvfDungeonDropCatalog {
	t.Helper()
	stackables := "900 `cash/fate_package.stk`\n10304529 `material/fate.stk`\n"
	packageData := "10304529 30\n"
	if includeOther {
		stackables += "600 `material/other_reward.stk`\n"
		packageData += "600 1\n"
	}
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": stackables,
		"stackable/cash/fate_package.stk": "[stackable type]\n`[usable cera package]`\n" +
			"[stack limit]\n100\n" +
			"[package data]\n" + packageData + "[/package data]\n",
		"stackable/material/fate.stk": "[stackable type]\n`[material]`\n" +
			"[attach type]\n`[account]`\n" +
			"[stack limit]\n1000\n",
		"stackable/material/other_reward.stk": "[stackable type]\n`[material]`\n" +
			"[stack limit]\n999\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

type ceraPackageFateFixture struct {
	service *Service
	session *gameSession
	conn    *bufferConn
	group   dnfrepo.Group
	now     time.Time
	catalog *pvfDungeonDropCatalog
	request currentCeraPackageOpenRequest
}

func newCeraPackageFateFixture(
	t *testing.T,
	account dnfrepo.AccountRecord,
	includeOther bool,
	slots map[string]dnfrepo.ItemStack,
) *ceraPackageFateFixture {
	t.Helper()
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := ceraPackageFateTestNow()
	if account.AccountID == "" {
		account.AccountID = "account-1"
	}
	account = dnfrepo.CloneAccount(account)
	if err := group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	for _, characterID := range []string{"19", "20"} {
		if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   account.AccountID,
			Stats:       map[string]int64{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: slots}).Slots,
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "20",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := mustCurrentCeraPackageFateCatalog(t, includeOther)
	service := &Service{
		options: options{
			accountID:          account.AccountID,
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return group, true },
		gameplayTimers:     &processGameplayTimeQueue{now: func() time.Time { return now }},
	}
	service.options.currentClientVersion = "official-24.9"
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "cera-package-fate-account-wallet",
		accountID:           account.AccountID,
		selectedCharacterID: 19,
	}
	return &ceraPackageFateFixture{
		service: service,
		session: session,
		conn:    conn,
		group:   group,
		now:     now,
		catalog: catalog,
		request: currentCeraPackageOpenRequest{SourceSlot: 75},
	}
}

func ceraPackageFateSlots(count int64) map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"0:75": {ItemID: int64(ceraPackageFateTestSourceID), Count: count},
	}
}

func fullCeraPackageFateBag() map[string]dnfrepo.ItemStack {
	slots := make(map[string]dnfrepo.ItemStack, 357)
	for slot := int16(3); slot <= 359; slot++ {
		slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)] = dnfrepo.ItemStack{
			ItemID: 970000 + int64(slot),
			Count:  1,
		}
	}
	slots["0:75"] = dnfrepo.ItemStack{ItemID: int64(ceraPackageFateTestSourceID), Count: 2}
	return slots
}

func ceraPackageFateRequestBody(slot uint16, choices ...currentCeraPackageChoice) []byte {
	body := make([]byte, currentCeraPackageRequestHeaderSize+len(choices)*currentCeraPackageChoiceStride)
	binary.LittleEndian.PutUint16(body[0:2], slot)
	body[2] = byte(len(choices))
	for index, choice := range choices {
		offset := currentCeraPackageRequestHeaderSize + index*currentCeraPackageChoiceStride
		binary.LittleEndian.PutUint32(body[offset:offset+4], choice.ItemID)
		body[offset+4] = choice.Option
	}
	return body
}

func assertNoCeraPackageFateInventoryRow(t *testing.T, group dnfrepo.Group, characterID string) {
	t.Helper()
	inventory, found, err := group.Inventory.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load inventory char=%s found=%t err=%v", characterID, found, err)
	}
	for key, stack := range inventory.Slots {
		if stack.ItemID == int64(raidcurrency.FateItemID) {
			t.Fatalf("Fate created an inventory row char=%s key=%s stack=%+v", characterID, key, stack)
		}
	}
}

func TestCeraPackageFateCreditsAccountAndRefreshesWallet(t *testing.T) {
	fixture := newCeraPackageFateFixture(t, dnfrepo.AccountRecord{AccountID: "account-1"}, false, ceraPackageFateSlots(2))
	if err := fixture.service.handleGameCommand(
		fixture.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketOpenCerapackage),
		ceraPackageFateRequestBody(75),
	); err != nil {
		t.Fatal(err)
	}
	frames := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(frames) == 0 || frames[0].Header.MsgID != uint16(dnfenum.CmdPacketOpenCerapackage) ||
		!reflect.DeepEqual(frames[0].Body, []byte{1, 75, 0}) {
		t.Fatalf("op518 ACK frames=%+v", frames)
	}
	sawFateList, sawWeekly := false, false
	for _, frame := range frames {
		if frame.Header.MsgID == 14 && len(frame.Body) > 3 && frame.Body[0] == currentRaidCurrencyListType {
			sawFateList = true
			total := binary.LittleEndian.Uint32(frame.Body[3+0x06 : 3+0x0A])
			if total != ceraPackageFateTestReward {
				t.Fatalf("list44 Fate count=%d want=%d body=%x", total, ceraPackageFateTestReward, frame.Body)
			}
		}
		if frame.Header.MsgID == 1456 {
			sawWeekly = true
			if got := binary.LittleEndian.Uint32(frame.Body[6:10]); got != ceraPackageFateTestReward {
				t.Fatalf("weekly Fate earned=%d want=%d", got, ceraPackageFateTestReward)
			}
		}
	}
	if !sawFateList || !sawWeekly {
		t.Fatalf("Fate wallet was not refreshed: list44=%t weekly=%t", sawFateList, sawWeekly)
	}
	inventory, _, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if source := inventory.Slots["0:75"]; source.ItemID != int64(ceraPackageFateTestSourceID) || source.Count != 1 {
		t.Fatalf("source package=%+v, want one remaining", source)
	}
	assertNoCeraPackageFateInventoryRow(t, fixture.group, "19")
	account, _, _ := fixture.group.Account.Load(context.Background(), "account-1")
	state, err := raidcurrency.Read(account, fixture.now)
	if err != nil || state.Balance != ceraPackageFateTestReward || state.Earned != ceraPackageFateTestReward {
		t.Fatalf("Fate state=%+v err=%v", state, err)
	}
}

func TestCeraPackageFateWeeklyCapUsesActualGrantedAmount(t *testing.T) {
	account := dnfrepo.AccountRecord{AccountID: "account-1"}
	if granted, _, err := raidcurrency.Credit(&account, 2490, ceraPackageFateTestNow()); err != nil || granted != 2490 {
		t.Fatalf("seed weekly Fate granted=%d err=%v", granted, err)
	}
	fixture := newCeraPackageFateFixture(t, account, false, ceraPackageFateSlots(2))
	definition, err := fixture.service.prepareCurrentCeraPackage(context.Background(), fixture.session, fixture.catalog, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	first, err := fixture.service.commitCurrentCeraPackage(context.Background(), fixture.session, fixture.catalog, definition, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if first.FateGranted != 10 || !first.FateCurrencyChanged {
		t.Fatalf("first capped result=%+v, want granted=10 changed=true", first)
	}
	second, err := fixture.service.commitCurrentCeraPackage(context.Background(), fixture.session, fixture.catalog, definition, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if second.FateGranted != 0 || second.FateCurrencyChanged {
		t.Fatalf("fully capped result=%+v, want granted=0 changed=false", second)
	}
	account, _, _ = fixture.group.Account.Load(context.Background(), "account-1")
	state, err := raidcurrency.Read(account, ceraPackageFateTestNow())
	if err != nil || state.Balance != raidcurrency.WeeklyLimit || state.Earned != raidcurrency.WeeklyLimit {
		t.Fatalf("fully capped state=%+v err=%v", state, err)
	}
	inventory, _, _ := fixture.group.Inventory.Load(context.Background(), "19")
	if _, found := inventory.Slots["0:75"]; found {
		t.Fatal("fully capped package did not consume the second source")
	}
	assertNoCeraPackageFateInventoryRow(t, fixture.group, "19")
}

func TestCeraPackageFateFullBagMailsOtherRewardsInSameTransaction(t *testing.T) {
	fixture := newCeraPackageFateFixture(t, dnfrepo.AccountRecord{AccountID: "account-1"}, true, fullCeraPackageFateBag())
	if err := fixture.service.handleGameCommand(
		fixture.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketOpenCerapackage),
		ceraPackageFateRequestBody(75),
	); err != nil {
		t.Fatal(err)
	}
	frames := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(frames) == 0 || frames[0].Header.MsgID != uint16(dnfenum.CmdPacketOpenCerapackage) ||
		!reflect.DeepEqual(frames[0].Body, []byte{1, 75, 0}) {
		t.Fatalf("mixed full-bag ACK frames=%+v", frames)
	}
	inventory, _, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if source := inventory.Slots["0:75"]; source.ItemID != int64(ceraPackageFateTestSourceID) || source.Count != 1 {
		t.Fatalf("source package=%+v, want one remaining", source)
	}
	assertNoCeraPackageFateInventoryRow(t, fixture.group, "19")
	box, found, err := fixture.group.Mailbox.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load overflow mailbox found=%t err=%v", found, err)
	}
	var overflowMail *dnfrepo.MailRecord
	for _, mail := range box.Mails {
		copy := mail
		overflowMail = &copy
	}
	if overflowMail == nil || overflowMail.Metadata["source"] != "cera_package_main_inventory_full" ||
		len(overflowMail.Attachments) != 1 || overflowMail.Attachments[0].ItemID != int64(ceraPackageFateTestOtherID) ||
		overflowMail.Attachments[0].Count != 1 {
		t.Fatalf("overflow mail=%+v", overflowMail)
	}
	account, _, _ := fixture.group.Account.Load(context.Background(), "account-1")
	state, err := raidcurrency.Read(account, fixture.now)
	if err != nil || state.Balance != ceraPackageFateTestReward || state.Earned != ceraPackageFateTestReward {
		t.Fatalf("Fate state=%+v err=%v", state, err)
	}
}

type ceraPackageFateRejectAssets struct{ dnfrepo.MailboxAssetUnitOfWork }

func (u ceraPackageFateRejectAssets) WithinAccountTradeMailbox(
	ctx context.Context,
	accountID string,
	characterIDs []string,
	mailboxCharacterID string,
	apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error,
) error {
	base, ok := u.MailboxAssetUnitOfWork.(dnfrepo.AccountTradeMailboxUnitOfWork)
	if !ok {
		return errors.New("cera package Fate test requires account-trade mailbox UOW")
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
		return errors.New("injected cera package Fate post-save failure")
	})
}

func TestCeraPackageFateCommitFailureRollsBackSourceAccountAndMail(t *testing.T) {
	fixture := newCeraPackageFateFixture(t, dnfrepo.AccountRecord{AccountID: "account-1"}, true, fullCeraPackageFateBag())
	beforeAccount, _, _ := fixture.group.Account.Load(context.Background(), "account-1")
	beforeInventory, _, _ := fixture.group.Inventory.Load(context.Background(), "19")
	beforeMailbox, mailboxFound, _ := fixture.group.Mailbox.Load(context.Background(), "19")
	fixture.group.MailboxAssets = ceraPackageFateRejectAssets{MailboxAssetUnitOfWork: fixture.group.MailboxAssets}
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return fixture.group, true }

	if err := fixture.service.handleCurrentCeraPackageOpen(fixture.session, ceraPackageFateRequestBody(75)); err != nil {
		t.Fatal(err)
	}
	frames := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(frames) == 0 || frames[0].Header.MsgID != uint16(dnfenum.CmdPacketOpenCerapackage) ||
		len(frames[0].Body) == 0 || frames[0].Body[0] != 0 {
		t.Fatalf("failure ACK frames=%+v", frames)
	}
	afterAccount, _, _ := fixture.group.Account.Load(context.Background(), "account-1")
	afterInventory, _, _ := fixture.group.Inventory.Load(context.Background(), "19")
	afterMailbox, afterMailboxFound, _ := fixture.group.Mailbox.Load(context.Background(), "19")
	if !reflect.DeepEqual(beforeAccount, afterAccount) ||
		!reflect.DeepEqual(beforeInventory, afterInventory) ||
		mailboxFound != afterMailboxFound || !reflect.DeepEqual(beforeMailbox, afterMailbox) {
		t.Fatal("cera package Fate failure leaked source, account credit, or overflow mail")
	}
	assertNoCeraPackageFateInventoryRow(t, fixture.group, "19")
}
