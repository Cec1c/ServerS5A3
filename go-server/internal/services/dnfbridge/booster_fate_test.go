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
	boosterFateTestBoxItemID uint32 = 900
	boosterFateTestReward    uint32 = 30
)

func boosterFateTestNow() time.Time {
	return time.Date(2026, time.October, 27, 2, 0, 0, 0, time.FixedZone("CST", 8*60*60))
}

func mustCurrentBoosterFateCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "900 `cash/fate_gift.stk`\n10304529 `material/fate.stk`\n",
		"stackable/cash/fate_gift.stk": "[stackable type]\n`[booster]` 0\n" +
			"[stack limit]\n100\n" +
			"[booster info]\n[etc]\n1 10304529 10000 30\n[/etc]\n[/booster info]\n",
		"stackable/material/fate.stk": "[stackable type]\n`[material]` 0\n" +
			"[attach type]\n`[account]`\n" +
			"[stack limit]\n1000\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

type boosterFateFixture struct {
	service *Service
	session *gameSession
	conn    *bufferConn
	group   dnfrepo.Group
	now     time.Time
}

func newBoosterFateFixture(
	t *testing.T,
	account dnfrepo.AccountRecord,
	slots map[string]dnfrepo.ItemStack,
) *boosterFateFixture {
	t.Helper()
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	now := boosterFateTestNow()
	if account.AccountID == "" {
		account.AccountID = "account-1"
	}
	account = dnfrepo.CloneAccount(account)
	if err := group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: account.AccountID,
		Slots:     map[string]dnfrepo.ItemStack{},
	}); err != nil {
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
	service := currentBoosterTestService(mustCurrentBoosterFateCatalog(t), group)
	service.options.currentClientVersion = "official-24.9"
	service.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "booster-fate-account-wallet",
		accountID:           account.AccountID,
		selectedCharacterID: 19,
	}
	return &boosterFateFixture{service: service, session: session, conn: conn, group: group, now: now}
}

func boosterFateBoxSlots(count int64) map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"0:75": {ItemID: int64(boosterFateTestBoxItemID), Count: count},
	}
}

func fullBoosterFateBagExceptSource() map[string]dnfrepo.ItemStack {
	slots := make(map[string]dnfrepo.ItemStack, 357)
	for slot := int16(3); slot <= 359; slot++ {
		if slot == 75 {
			continue
		}
		slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)] = dnfrepo.ItemStack{
			ItemID: 950000 + int64(slot),
			Count:  1,
		}
	}
	slots["0:75"] = dnfrepo.ItemStack{ItemID: int64(boosterFateTestBoxItemID), Count: 1}
	return slots
}

func assertNoBoosterFateInventoryRow(t *testing.T, group dnfrepo.Group, characterID string) {
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

func TestBoosterFateGiftConsumesSourceAndCreditsAccountAtomically(t *testing.T) {
	fixture := newBoosterFateFixture(t, dnfrepo.AccountRecord{AccountID: "account-1"}, boosterFateBoxSlots(2))
	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{75, 0}); err != nil {
		t.Fatal(err)
	}
	frames := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(frames) == 0 || frames[0].Header.MsgID != uint16(dnfenum.CmdPacketUseBoosterItem) ||
		len(frames[0].Body) < 25 || frames[0].Body[0] != 1 {
		t.Fatalf("booster Fate ACK frames=%+v", frames)
	}
	if got := binary.LittleEndian.Uint32(frames[0].Body[17:21]); got != raidcurrency.FateItemID {
		t.Fatalf("ACK reward item=%d want=%d", got, raidcurrency.FateItemID)
	}
	if got := binary.LittleEndian.Uint32(frames[0].Body[21:25]); got != boosterFateTestReward {
		t.Fatalf("ACK reward count=%d want=%d", got, boosterFateTestReward)
	}
	sawFateList, sawWeekly := false, false
	for _, frame := range frames {
		if frame.Header.MsgID == 14 && len(frame.Body) > 3 && frame.Body[0] == currentRaidCurrencyListType {
			sawFateList = true
		}
		if frame.Header.MsgID == 1456 {
			sawWeekly = true
		}
	}
	if !sawFateList || !sawWeekly {
		t.Fatalf("committed booster Fate wallet was not projected: list44=%t weekly=%t", sawFateList, sawWeekly)
	}
	inventory, found, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if source := inventory.Slots["0:75"]; source.ItemID != int64(boosterFateTestBoxItemID) || source.Count != 1 {
		t.Fatalf("source stack=%+v, want one remaining gift box", source)
	}
	assertNoBoosterFateInventoryRow(t, fixture.group, "19")
	account, found, err := fixture.group.Account.Load(context.Background(), "account-1")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	state, err := raidcurrency.Read(account, fixture.now)
	if err != nil || state.Balance != boosterFateTestReward || state.Earned != boosterFateTestReward {
		t.Fatalf("Fate state=%+v err=%v", state, err)
	}
}

func TestBoosterFateWeeklyCapPublishesOnlyGrantedAmount(t *testing.T) {
	account := dnfrepo.AccountRecord{AccountID: "account-1"}
	if granted, _, err := raidcurrency.Credit(&account, 2490, boosterFateTestNow()); err != nil || granted != 2490 {
		t.Fatalf("seed weekly Fate granted=%d err=%v", granted, err)
	}
	fixture := newBoosterFateFixture(t, account, boosterFateBoxSlots(2))

	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{75, 0}); err != nil {
		t.Fatal(err)
	}
	first := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(first) == 0 || len(first[0].Body) < 25 || first[0].Body[0] != 1 {
		t.Fatalf("first capped ACK frames=%+v", first)
	}
	if got := binary.LittleEndian.Uint32(first[0].Body[21:25]); got != 10 {
		t.Fatalf("weekly-capped ACK reward count=%d want=10", got)
	}
	account, _, _ = fixture.group.Account.Load(context.Background(), "account-1")
	state, err := raidcurrency.Read(account, fixture.now)
	if err != nil || state.Balance != raidcurrency.WeeklyLimit || state.Earned != raidcurrency.WeeklyLimit {
		t.Fatalf("post-cap state=%+v err=%v", state, err)
	}

	fixture.conn.write.Reset()
	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{75, 0}); err != nil {
		t.Fatal(err)
	}
	second := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(second) == 0 || len(second[0].Body) != 17 || second[0].Body[0] != 1 {
		t.Fatalf("fully capped ACK frames=%+v", second)
	}
	if got := binary.LittleEndian.Uint16(second[0].Body[15:17]); got != 0 {
		t.Fatalf("fully capped ACK reward rows=%d want=0", got)
	}
	account, _, _ = fixture.group.Account.Load(context.Background(), "account-1")
	state, err = raidcurrency.Read(account, fixture.now)
	if err != nil || state.Balance != raidcurrency.WeeklyLimit || state.Earned != raidcurrency.WeeklyLimit {
		t.Fatalf("fully capped state=%+v err=%v", state, err)
	}
	inventory, _, _ := fixture.group.Inventory.Load(context.Background(), "19")
	if _, found := inventory.Slots["0:75"]; found {
		t.Fatal("fully capped claim did not consume the second gift box")
	}
	assertNoBoosterFateInventoryRow(t, fixture.group, "19")
}

func TestBoosterFateAccountWalletIsSharedAcrossOnlineCharacters(t *testing.T) {
	fixture := newBoosterFateFixture(t, dnfrepo.AccountRecord{AccountID: "account-1"}, boosterFateBoxSlots(1))
	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{75, 0}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	peer := &gameSession{
		conn:                conn,
		connID:              "booster-fate-peer-character",
		accountID:           "account-1",
		selectedCharacterID: 20,
	}
	if err := fixture.service.sendCurrentFateSnapshot(context.Background(), peer, false); err != nil {
		t.Fatal(err)
	}
	frames := splitAllGameServerUpperPackets(t, conn.write.Bytes())
	if len(frames) != 2 || frames[0].Header.MsgID != 14 || frames[1].Header.MsgID != 1456 {
		t.Fatalf("peer Fate frames=%+v", frames)
	}
	row := frames[0].Body
	if len(row) != 3+currentItemListEntryWireSize || row[0] != currentRaidCurrencyListType ||
		binary.LittleEndian.Uint32(row[5:9]) != raidcurrency.FateItemID ||
		binary.LittleEndian.Uint32(row[9:13]) != boosterFateTestReward {
		t.Fatalf("peer list44 row=%x", row)
	}
	weekly := frames[1].Body
	if len(weekly) != 14 || binary.LittleEndian.Uint32(weekly[6:10]) != boosterFateTestReward {
		t.Fatalf("peer Fate weekly body=%x", weekly)
	}
	assertNoBoosterFateInventoryRow(t, fixture.group, "20")
}

func TestBoosterFateFullBagDoesNotNeedAnInventorySlot(t *testing.T) {
	fixture := newBoosterFateFixture(t, dnfrepo.AccountRecord{AccountID: "account-1"}, fullBoosterFateBagExceptSource())
	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{75, 0}); err != nil {
		t.Fatal(err)
	}
	frames := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(frames) == 0 || len(frames[0].Body) < 25 || frames[0].Body[0] != 1 {
		t.Fatalf("full-bag booster Fate ACK frames=%+v", frames)
	}
	inventory, _, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := inventory.Slots["0:75"]; found {
		t.Fatal("source gift remained after a successful full-bag Fate claim")
	}
	if len(inventory.Slots) != 356 {
		t.Fatalf("inventory rows=%d, want the original 356 non-source rows unchanged", len(inventory.Slots))
	}
	assertNoBoosterFateInventoryRow(t, fixture.group, "19")
	account, _, _ := fixture.group.Account.Load(context.Background(), "account-1")
	state, err := raidcurrency.Read(account, fixture.now)
	if err != nil || state.Balance != boosterFateTestReward || state.Earned != boosterFateTestReward {
		t.Fatalf("Fate state=%+v err=%v", state, err)
	}
}

type boosterFateFailingLedger struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	ledger dnfrepo.AccountCharacterLedgerAssetUnitOfWork
}

func (u boosterFateFailingLedger) WithinAccountCharacterLedgerAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(
		dnfrepo.AccountRepository,
		dnfrepo.AccountInventoryRepository,
		dnfrepo.CharacterRepository,
		dnfrepo.InventoryRepository,
		dnfrepo.EquipmentRepository,
	) error,
) error {
	if u.ledger == nil {
		return errors.New("booster Fate test requires a ledger UOW")
	}
	return u.ledger.WithinAccountCharacterLedgerAssets(ctx, accountID, characterID, func(
		accounts dnfrepo.AccountRepository,
		accountInventory dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		if err := apply(accounts, accountInventory, characters, inventories, equipment); err != nil {
			return err
		}
		return errors.New("injected booster Fate post-save failure")
	})
}

func TestBoosterFateCommitFailureRollsBackSourceAndAccount(t *testing.T) {
	account := dnfrepo.AccountRecord{AccountID: "account-1"}
	if granted, _, err := raidcurrency.Credit(&account, 5, boosterFateTestNow()); err != nil || granted != 5 {
		t.Fatalf("seed state granted=%d err=%v", granted, err)
	}
	fixture := newBoosterFateFixture(t, account, fullBoosterFateBagExceptSource())
	beforeAccount, _, _ := fixture.group.Account.Load(context.Background(), "account-1")
	beforeInventory, _, _ := fixture.group.Inventory.Load(context.Background(), "19")
	ledger, ok := fixture.group.AccountAssets.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork)
	if !ok {
		t.Fatal("memory AccountAssets does not implement the Fate ledger UOW")
	}
	fixture.group.AccountAssets = boosterFateFailingLedger{
		AccountCharacterAssetUnitOfWork: fixture.group.AccountAssets,
		ledger:                          ledger,
	}
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return fixture.group, true }

	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{75, 0}); err != nil {
		t.Fatal(err)
	}
	frames := splitAllGameServerUpperPackets(t, fixture.conn.write.Bytes())
	if len(frames) == 0 || frames[0].Header.MsgID != uint16(dnfenum.CmdPacketUseBoosterItem) ||
		len(frames[0].Body) < 1 || frames[0].Body[0] != 0 {
		t.Fatalf("failure ACK frames=%+v", frames)
	}
	afterAccount, _, _ := fixture.group.Account.Load(context.Background(), "account-1")
	afterInventory, _, _ := fixture.group.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(beforeAccount, afterAccount) || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatal("booster Fate failure leaked a source consumption or account credit")
	}
	assertNoBoosterFateInventoryRow(t, fixture.group, "19")
}
