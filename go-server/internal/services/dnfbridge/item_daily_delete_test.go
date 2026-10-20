package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func dailyDeleteTestCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst":      "10096296 `test/daily.stk`\n3037 `test/permanent.stk`\n8001 `test/zero.stk`\n",
		"stackable/test/daily.stk":     "[stackable type] `[material]`\n[stack limit] 1000\n[daily delete item] 1\n",
		"stackable/test/permanent.stk": "[stackable type] `[material]`\n[stack limit] 1000\n",
		"stackable/test/zero.stk":      "[stackable type] `[material]`\n[daily delete item] 0\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func newDailyDeleteTestService(t *testing.T, now time.Time) (*Service, dnfrepo.Group, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}, Warehouse: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = now
	service := &Service{
		options:        options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog: dailyDeleteTestCatalog(t), gameplayTimers: queue,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	return service, repositories, queue
}

func TestDailyDeleteGrantPersistsBoundaryAndSeparatesNextDay(t *testing.T) {
	now := time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC)
	catalog := dailyDeleteTestCatalog(t)
	definition, err := catalog.ResolveItem(10096296)
	if err != nil || !definition.DailyDeleteItem {
		t.Fatalf("definition=%+v err=%v", definition, err)
	}
	for _, id := range []uint32{3037, 8001} {
		ordinary, err := catalog.ResolveItem(id)
		if err != nil || ordinary.DailyDeleteItem {
			t.Fatalf("ordinary=%+v err=%v", ordinary, err)
		}
	}
	inventory := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
	slots, err := grantCurrentCeraShopProductAt(&inventory, definition, 2, now)
	if err != nil || len(slots) != 1 {
		t.Fatalf("grant=%v err=%v", slots, err)
	}
	stack := inventory.Slots[currentDungeonPickupMainSlotKey(int16(slots[0]))]
	deadline := itemexpiration.NextDailyDeleteAt(now)
	if stack.Extra[itemexpiration.DailyDeleteDeadlineKey] != strconv.FormatInt(deadline.Unix(), 10) || !stack.ExpireAt.Equal(deadline) || stack.Count != 2 {
		t.Fatalf("grant=%+v", stack)
	}
	if got := binary.LittleEndian.Uint32(stack.RawEntry[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != uint32(deadline.Unix()) {
		t.Fatalf("native expiry=%d", got)
	}
	parent := dnfrepo.ItemStack{ExpireAt: now.AddDate(2, 0, 0)}
	nested, err := currentPVFItemDefinitionForNestedRewardGrantAt(definition, parent, now)
	if err != nil || !nested.ExpirationDate.Equal(deadline) {
		t.Fatalf("nested=%+v err=%v", nested, err)
	}
	nextSlots, err := grantCurrentCeraShopProductAt(&inventory, definition, 3, deadline)
	if err != nil || len(nextSlots) != 1 || nextSlots[0] == slots[0] {
		t.Fatalf("next day merged expired stack: %v err=%v", nextSlots, err)
	}
	if inventory.Slots[currentDungeonPickupMainSlotKey(int16(slots[0]))].Count != 2 {
		t.Fatal("old stack count changed")
	}
}

func TestDailyDeleteOfflineCleanupAllContainersAndNoRenewal(t *testing.T) {
	acquired := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	service, repositories, queue := newDailyDeleteTestService(t, acquired)
	ctx := context.Background()
	definition, _ := service.pvfItemCatalog.ResolveItem(10096296)
	grant, _ := currentPVFItemDefinitionForGrantAt(definition, acquired)
	stack := currentPVFGrantIdentity(grant)
	stack.Count = 7
	ordinary := dnfrepo.ItemStack{ItemID: 3037, Count: 9, Extra: map[string]string{"source": "test"}}
	inv, _, _ := repositories.Inventory.Load(ctx, "19")
	if inv.Slots == nil {
		inv.Slots = map[string]dnfrepo.ItemStack{}
	}
	if inv.Warehouse == nil {
		inv.Warehouse = map[string]dnfrepo.ItemStack{}
	}
	inv.Slots["0:125"] = stack
	inv.Slots["0:126"] = ordinary
	inv.Warehouse["2:4"] = stack
	if err := repositories.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if account.Slots == nil {
		account.Slots = map[string]dnfrepo.ItemStack{}
	}
	account.Slots["12:8"] = stack
	if err := repositories.AccountInventory.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	// Login shortly before expiration must preserve the original deadline.
	queue.now = itemexpiration.NextDailyDeleteAt(acquired).Add(-time.Second)
	if _, err := service.reconcileCurrentPVFItemExpirations(ctx, repositories, "19", "account-1"); err != nil {
		t.Fatal(err)
	}
	kept, _, _ := repositories.Inventory.Load(ctx, "19")
	if !kept.Slots["0:125"].ExpireAt.Equal(grant.ExpirationDate) {
		t.Fatal("login renewed deadline")
	}
	queue.now = grant.ExpirationDate.Add(72 * time.Hour)
	summary, removed, err := service.reconcileCurrentDailyDeleteWithRemoved(ctx, repositories, "19", "account-1", service.pvfItemCatalog)
	if err != nil || summary.DeletedInventory != 1 || summary.DeletedWarehouse != 1 || summary.DeletedAccount != 1 || !reflect.DeepEqual(removed, []currentDailyDeleteSlot{{0, 125}, {2, 4}, {12, 8}}) {
		t.Fatalf("summary=%+v removed=%v err=%v", summary, removed, err)
	}
	inv, _, _ = repositories.Inventory.Load(ctx, "19")
	account, _, _ = repositories.AccountInventory.Load(ctx, "account-1")
	if len(inv.Slots) != 1 || len(inv.Warehouse) != 0 || len(account.Slots) != 0 || !reflect.DeepEqual(inv.Slots["0:126"], ordinary) {
		t.Fatalf("ordinary asset changed inventory=%+v account=%+v", inv, account)
	}
	summary, removed, err = service.reconcileCurrentDailyDeleteWithRemoved(ctx, repositories, "19", "account-1", service.pvfItemCatalog)
	if err != nil || summary.Total() != 0 || len(removed) != 0 {
		t.Fatalf("not idempotent: %+v %v %v", summary, removed, err)
	}
}

func TestDailyDeleteInvalidDeadlineRollsBackAllContainerDeletion(t *testing.T) {
	now := time.Date(2026, 10, 5, 22, 0, 0, 0, time.UTC)
	service, repositories, _ := newDailyDeleteTestService(t, now)
	ctx := context.Background()
	inv, _, _ := repositories.Inventory.Load(ctx, "19")
	if inv.Slots == nil {
		inv.Slots = map[string]dnfrepo.ItemStack{}
	}
	if inv.Warehouse == nil {
		inv.Warehouse = map[string]dnfrepo.ItemStack{}
	}
	inv.Slots["0:125"] = dnfrepo.ItemStack{ItemID: 10096296, Count: 1, Extra: map[string]string{itemexpiration.DailyDeleteDeadlineKey: strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)}}
	inv.Warehouse["2:1"] = dnfrepo.ItemStack{ItemID: 10096296, Count: 1, Extra: map[string]string{itemexpiration.DailyDeleteDeadlineKey: "corrupt"}}
	if err := repositories.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	_, removed, err := service.reconcileCurrentDailyDeleteWithRemoved(ctx, repositories, "19", "account-1", service.pvfItemCatalog)
	after, _, _ := repositories.Inventory.Load(ctx, "19")
	if err == nil || len(removed) != 0 || !reflect.DeepEqual(inv, after) {
		t.Fatalf("rollback failed err=%v removed=%v", err, removed)
	}
}

func TestDailyDeleteOnlineAtSixUsesCommittedOp14AndRejectsStaleCharacter(t *testing.T) {
	before := time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC)
	service, repositories, queue := newDailyDeleteTestService(t, before)
	ctx := context.Background()
	inv, _, _ := repositories.Inventory.Load(ctx, "19")
	if inv.Slots == nil {
		inv.Slots = map[string]dnfrepo.ItemStack{}
	}
	if inv.Warehouse == nil {
		inv.Warehouse = map[string]dnfrepo.ItemStack{}
	}
	inv.Slots["0:125"] = dnfrepo.ItemStack{ItemID: 10096296, Count: 1, Extra: map[string]string{itemexpiration.DailyDeleteDeadlineKey: strconv.FormatInt(before.Add(time.Second).Unix(), 10)}}
	if err := repositories.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "daily-test", accountID: "account-1", selectedCharacterID: 19, characterGeneration: 3}
	service.armCurrentDailyDeleteClock(session)
	first := queue.task(t, 0)
	if first.delay != time.Second {
		t.Fatalf("delay=%s", first.delay)
	}
	queue.now = before.Add(time.Second)
	queue.fire(first, false)
	after, _, _ := repositories.Inventory.Load(ctx, "19")
	if len(after.Slots) != 0 {
		t.Fatal("online expiration not committed")
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 14 || packet.Header.Classification != 0 || len(rest) != 0 || len(packet.Body) != 3+currentItemListEntryWireSize || packet.Body[0] != 0 || binary.LittleEndian.Uint16(packet.Body[1:3]) != 1 || binary.LittleEndian.Uint16(packet.Body[3:5]) != 125 || binary.LittleEndian.Uint32(packet.Body[5:9]) != math.MaxUint32 || binary.LittleEndian.Uint32(packet.Body[9:13]) != 0 {
		t.Fatalf("clear packet=%+v", packet)
	}
	second := queue.task(t, 1)
	if second.delay != 24*time.Hour {
		t.Fatalf("next boundary=%s", second.delay)
	}
	service.stopCurrentDailyDeleteClock(session)
	if !second.cancelled {
		t.Fatal("disconnect failed to cancel")
	}
	session.selectedCharacterID = 20
	session.characterGeneration++
	conn.write.Reset()
	queue.fire(second, true)
	if conn.write.Len() != 0 {
		t.Fatal("stale timer sent to a new character")
	}
}

func TestRealPVFDailyDeleteAntonMaterial(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(10096296)
	if err != nil || !definition.DailyDeleteItem || definition.PVFPath != "stackable/10096001/10096296.stk" {
		t.Fatalf("real Anton=%+v err=%v", definition, err)
	}
	ordinary, err := catalog.ResolveItem(3037)
	if err != nil || ordinary.DailyDeleteItem {
		t.Fatalf("ordinary real item=%+v err=%v", ordinary, err)
	}
	now := time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC)
	inventory := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
	slots, err := grantCurrentCeraShopProductAt(&inventory, definition, 1, now)
	if err != nil || len(slots) != 1 {
		t.Fatalf("real grant=%v %v", slots, err)
	}
	stack := inventory.Slots[currentDungeonPickupMainSlotKey(int16(slots[0]))]
	if stack.Extra[itemexpiration.DailyDeleteDeadlineKey] != strconv.FormatInt(now.Add(time.Second).Unix(), 10) {
		t.Fatalf("real grant deadline=%+v", stack)
	}
}

func TestDailyDeleteNPCBuyDeadlineIsCommittedWithCurrencyDebit(t *testing.T) {
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	definition, err := items.ResolveItem(600)
	if err != nil {
		t.Fatal(err)
	}
	definition.DailyDeleteItem = true
	// Simulate a legacy absolute event date combined with the daily flag.
	definition.ExpirationDate = time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)
	items.itemCache[600] = definition
	repositories := mustCurrentNPCShopRepositories(t, 1000, nil)
	service := currentNPCShopTestService(repositories, shop, items)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC)
	service.gameplayTimers = queue
	session := &gameSession{selectedCharacterID: 19}
	result, err := service.commitCurrentNPCShopBuy(context.Background(), session, shop, items, currentNPCShopBuyRequest{ItemID: 600, Count: 2, ActorContext: 326})
	if err != nil {
		t.Fatal(err)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	character, _, _ := repositories.Character.Load(context.Background(), "19")
	stack := inventory.Slots[currentDungeonPickupMainSlotKey(result.Slot)]
	want := itemexpiration.NextDailyDeleteAt(queue.now)
	if character.Stats["gold"] != 800 || stack.Count != 2 || !stack.ExpireAt.Equal(want) || stack.Extra[itemexpiration.DailyDeleteDeadlineKey] != strconv.FormatInt(want.Unix(), 10) {
		t.Fatalf("gold=%d stack=%+v", character.Stats["gold"], stack)
	}
}

func TestDailyDeleteOnlineFailureRetainsItemsAndRetriesWithoutSending(t *testing.T) {
	before := time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC)
	service, repositories, queue := newDailyDeleteTestService(t, before)
	ctx := context.Background()
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	inventory.Slots = map[string]dnfrepo.ItemStack{"0:125": {ItemID: 10096296, Count: 1, Extra: map[string]string{itemexpiration.DailyDeleteDeadlineKey: strconv.FormatInt(before.Add(time.Second).Unix(), 10)}}}
	inventory.Warehouse = map[string]dnfrepo.ItemStack{"2:1": {ItemID: 10096296, Count: 1, Extra: map[string]string{itemexpiration.DailyDeleteDeadlineKey: "bad"}}}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "daily-error", accountID: "account-1", selectedCharacterID: 19}
	service.armCurrentDailyDeleteClock(session)
	queue.now = before.Add(time.Second)
	queue.fire(queue.task(t, 0), false)
	after, _, _ := repositories.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(inventory, after) || conn.write.Len() != 0 || queue.task(t, 1).delay != time.Minute {
		t.Fatal("failed projection removed items, sent clears or did not retry")
	}
}

func TestDailyDeleteLegacyMarkerRepairDoesNotRenewHistoricalStaticDate(t *testing.T) {
	now := time.Date(2026, 10, 5, 20, 0, 0, 0, time.UTC)
	definition, _ := dailyDeleteTestCatalog(t).ResolveItem(10096296)
	definition.ExpirationDate = time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)
	stack := dnfrepo.ItemStack{ItemID: 10096296, Count: 1, ExpireAt: definition.ExpirationDate, Extra: map[string]string{"expiration_source": currentPVFWrongExpirationSource, "expire_unix": strconv.FormatInt(definition.ExpirationDate.Unix(), 10)}}
	patched, changed := applyCurrentPVFItemExpirationAt(stack, definition, now)
	want := itemexpiration.NextDailyDeleteAt(now)
	if !changed || !patched.ExpireAt.Equal(want) || patched.Extra[itemexpiration.DailyDeleteDeadlineKey] != strconv.FormatInt(want.Unix(), 10) || patched.Extra["expiration_source"] != "" {
		t.Fatalf("legacy=%+v", patched)
	}
	service := &Service{}
	catalog := dailyDeleteTestCatalog(t)
	catalog.itemCache[10096296] = definition
	entry := currentItemListEntryFromStack(0, 125, patched)
	entries := []currentItemListEntry{entry}
	if _, err := service.applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), entries, catalog, now); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(entries[0].data[currentItemListExpireTimeOffset : currentItemListExpireTimeOffset+4]); got != uint32(want.Unix()) {
		t.Fatalf("wire renewed to historical policy: %d", got)
	}
}

func TestRealPVFDailyDeleteFlaggedFamily(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC)
	want := itemexpiration.NextDailyDeleteAt(now)
	flagged, declared := 0, 0
	for _, id := range catalog.stackableIDs {
		ref := catalog.itemRefs[id]
		_, text, err := readDungeonDropText(catalog.source, "stackable", ref.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "[daily delete item]") {
			continue
		}
		declared++
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatalf("item=%d %v", id, err)
		}
		if !definition.DailyDeleteItem {
			continue
		}
		flagged++
		grant, err := currentPVFItemDefinitionForGrantAt(definition, now)
		if err != nil || !grant.ExpirationDate.Equal(want) {
			t.Fatalf("item=%d grant=%+v err=%v", id, grant, err)
		}
		stack := currentPVFGrantIdentity(grant)
		_, _, expired, err := itemexpiration.ReconcileDailyDelete(stack, want.Add(72*time.Hour))
		if err != nil || !expired {
			t.Fatalf("item=%d silently renewed err=%v", id, err)
		}
	}
	if flagged == 0 {
		t.Fatal("no real daily-delete items resolved")
	}
	t.Logf("real PVF: %d stackable IDs inspected; %d declarations; %d flag=1; every marked grant persisted and expired without renewal", len(catalog.stackableIDs), declared, flagged)
}
