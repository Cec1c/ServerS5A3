package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	dnfnpcshop "longheng.io/server/internal/modules/dnf/npcshop"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const expirationPVFQuote = "`"

// Regression on the real PVF: the Epic Road talisman 490010214 has
// no [stack limit] and carries an absolute [expiration date] that the server
// renews on write. Two consecutive single purchases must therefore land in ONE
// inventory row with Count 2 (not two rows), and the account weekly counter
// must read 2.
func TestCurrentNPCShopExpirationRealPVFTalismanSecondBuyMergesIntoOneRow(t *testing.T) {
	shop, items := realPVF535(t)
	repos := mustCurrentNPCShopRepositories(t, 100000000, nil)
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-expiration-merge", selectedCharacterID: 19}
	ctx := context.Background()

	first, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
		currentNPCShopBuyRequest{ItemID: 490010214, Count: 1, ActorContext: 1})
	if err != nil {
		t.Fatalf("first buy: %v", err)
	}
	second, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
		currentNPCShopBuyRequest{ItemID: 490010214, Count: 1, ActorContext: 1})
	if err != nil {
		t.Fatalf("second buy: %v", err)
	}
	if second.Slot != first.Slot {
		t.Fatalf("second buy opened a new row: first slot=%d second slot=%d (want one merged row)", first.Slot, second.Slot)
	}

	record, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	rows, total := 0, int64(0)
	for _, stack := range record.Slots {
		if stack.ItemID != 490010214 {
			continue
		}
		rows++
		total += stack.Count
	}
	if rows != 1 || total != 2 {
		t.Fatalf("talisman rows=%d total_count=%d want rows=1 total_count=2", rows, total)
	}

	// The account weekly counter (NOTI 715) must read 2 after two purchases.
	conn.write.Reset()
	request := make([]byte, 4)
	binary.LittleEndian.PutUint32(request, 3136)
	if err := service.handleCurrentNPCShopPurchaseCount(session, request); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(trailing) != 0 {
		t.Fatalf("NOTI trailing bytes=%d", len(trailing))
	}
	want := buildCurrentNPCShopPurchaseCountBody(map[uint32]uint32{490010214: 2})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("NOTI count body=%x want=%x", ack.Body, want)
	}
}

// Weekly quota on the real PVF: 69 then 1 reaches 70 (one merged row thanks to
// expiration normalization), the NOTI body reads 70 and the 71st purchase fails closed.
func TestCurrentNPCShopExpirationRealPVFWeeklyCounterFullAndOver(t *testing.T) {
	shop, items := realPVF535(t)
	repos := mustCurrentNPCShopRepositories(t, 100000000, nil)
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-expiration-full", selectedCharacterID: 19}
	ctx := context.Background()
	buy := func(count uint32) error {
		_, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
			currentNPCShopBuyRequest{ItemID: 490010214, Count: count, ActorContext: 1})
		return err
	}
	if err := buy(69); err != nil {
		t.Fatalf("69 must be allowed: %v", err)
	}
	if err := buy(1); err != nil {
		t.Fatalf("69+1 must be allowed: %v", err)
	}
	if err := buy(1); !errors.Is(err, dnfnpcshop.ErrPurchaseLimit) {
		t.Fatalf("71st purchase must be rejected with ErrPurchaseLimit: %v", err)
	}
	record, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	rows, total := 0, int64(0)
	for _, stack := range record.Slots {
		if stack.ItemID == 490010214 {
			rows++
			total += stack.Count
		}
	}
	if rows != 1 || total != 70 {
		t.Fatalf("after 70 purchases rows=%d total=%d want rows=1 total=70", rows, total)
	}
	conn.write.Reset()
	request := make([]byte, 4)
	binary.LittleEndian.PutUint32(request, 3136)
	if err := service.handleCurrentNPCShopPurchaseCount(session, request); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	want := buildCurrentNPCShopPurchaseCountBody(map[uint32]uint32{490010214: 70})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("NOTI body=%x want=%x", ack.Body, want)
	}
}

// A failed purchase must not create a row and must not advance the weekly
// counter, so the transaction is still all-or-nothing after expiration normalization.
func TestCurrentNPCShopExpirationInsufficientGoldLeavesNoRowAndNoCount(t *testing.T) {
	shop, items := realPVF535(t)
	repos := mustCurrentNPCShopRepositories(t, 100, nil)
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-expiration-poor", selectedCharacterID: 19}
	ctx := context.Background()
	if _, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
		currentNPCShopBuyRequest{ItemID: 490010214, Count: 1, ActorContext: 1}); err == nil {
		t.Fatal("purchase without enough gold must fail")
	}
	record, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	for _, stack := range record.Slots {
		if stack.ItemID == 490010214 {
			t.Fatalf("failed purchase left a row: %+v", stack)
		}
	}
	conn.write.Reset()
	request := make([]byte, 4)
	binary.LittleEndian.PutUint32(request, 3136)
	if err := service.handleCurrentNPCShopPurchaseCount(session, request); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	want := buildCurrentNPCShopPurchaseCountBody(map[uint32]uint32{})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("NOTI body after failure=%x want=%x", ack.Body, want)
	}
}

// A future absolute [expiration date] must keep its own value (the renewal
// helper is only for retired dates) while two purchases still merge into one
// row, so expiration normalization does not rewrite future instances.
func TestCurrentNPCShopExpirationFutureAbsoluteDateIsKeptAndMerges(t *testing.T) {
	source := bridgePVFSource{
		"monster/monster.lst":      "",
		"equipment/equipment.lst":  "",
		"stackable/stackable.lst":  "601 " + expirationPVFQuote + "future_material.stk" + expirationPVFQuote + "\n",
		"itemshop/itemshop.lst":    "400000044 " + expirationPVFQuote + "future_shop.shp" + expirationPVFQuote + "\n",
		"itemshop/future_shop.shp": "[NPC]\n3136\n[item list]\n601\n[/item list]\n",
		"equipment/pricetable.tbl": "[rate]\n200 150 30\n",
		"stackable/future_material.stk": "[price]\n200000\n[value]\n1\n[stackable type]\n" +
			expirationPVFQuote + "[material]" + expirationPVFQuote + "\n[expiration date]\n" +
			expirationPVFQuote + time.Now().UTC().AddDate(4, 0, 0).Format("2006-01-02 15:04:05") + expirationPVFQuote + "\n",
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos := mustCurrentNPCShopRepositories(t, 100000000, nil)
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-expiration-future", selectedCharacterID: 19}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
			currentNPCShopBuyRequest{ItemID: 601, Count: 1, ActorContext: 1}); err != nil {
			t.Fatalf("future-date purchase %d: %v", i+1, err)
		}
	}
	record, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	want := time.Time{}
	if definition, err := items.ResolveItem(601); err != nil {
		t.Fatalf("resolve 601: %v", err)
	} else {
		want = definition.ExpirationDate
	}
	if want.IsZero() {
		t.Fatal("catalog did not parse the future [expiration date]")
	}
	rows, total := 0, int64(0)
	var expire time.Time
	for _, stack := range record.Slots {
		if stack.ItemID != 601 {
			continue
		}
		rows++
		total += stack.Count
		expire = stack.ExpireAt
	}
	if rows != 1 || total != 2 {
		t.Fatalf("future-date rows=%d total=%d want rows=1 total=2", rows, total)
	}
	// A future absolute date must survive untouched: the persisted row must carry
	// exactly the parsed definition date (strictly non-zero), and the renewal
	// helper must return it unchanged.
	if expire.IsZero() {
		t.Fatal("persisted expiration is zero for a future-dated definition")
	}
	if !expire.Equal(want) {
		t.Fatalf("future absolute date changed: persisted=%s want=%s", expire, want)
	}
	if got := currentPVFRenewedRetiredExpirationDate(want, time.Now().UTC()); !got.Equal(want) {
		t.Fatalf("renewal helper changed a future date: got=%s want=%s", got, want)
	}
	if expire.Equal(currentPVFRetiredExpirationRenewalDate) {
		t.Fatalf("future absolute date collapsed onto the renewal date: %s", expire)
	}
}

// expirationFailSaveInventory delegates reads to the real in-memory store but fails
// every write, which injects a repository failure AFTER the project mutation has
// already happened inside the owner transaction.
type expirationFailSaveInventory struct {
	dnfrepo.InventoryRepository
	saveCalls *int64
}

func (f expirationFailSaveInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	atomic.AddInt64(f.saveCalls, 1)
	return errors.New("injected repository save failure")
}

// expirationFailSaveCharacter injects the same failure on the character store, which is
// where the gold mutation lands.
type expirationFailSaveCharacter struct {
	dnfrepo.CharacterRepository
	saveCalls *int64
}

func (f expirationFailSaveCharacter) Save(context.Context, dnfrepo.CharacterRecord) error {
	atomic.AddInt64(f.saveCalls, 1)
	return errors.New("injected repository save failure")
}

// expirationFailSaveSettlement hands the projector a transaction group whose inventory
// and character stores fail on Save, so the failure lands after the in-memory
// mutation and before the settlement can commit.
type expirationFailSaveSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
	saveCalls *int64
}

func (f expirationFailSaveSettlement) WithinCharacterSettlement(ctx context.Context, characterID string, apply func(dnfrepo.Group) error) error {
	return f.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		tx.Inventory = expirationFailSaveInventory{InventoryRepository: tx.Inventory, saveCalls: f.saveCalls}
		tx.Character = expirationFailSaveCharacter{CharacterRepository: tx.Character, saveCalls: f.saveCalls}
		return apply(tx)
	})
}

func expirationTalismanRows(t *testing.T, repos dnfrepo.Group) (rows int, total int64, gold int64) {
	t.Helper()
	ctx := context.Background()
	record, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	for _, stack := range record.Slots {
		if stack.ItemID == 490010214 {
			rows++
			total += stack.Count
		}
	}
	character, found, err := repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("character load found=%v err=%v", found, err)
	}
	return rows, total, character.Stats["gold"]
}

// A repository save failure after the in-transaction mutation must roll the
// whole purchase back: no row, unchanged gold, unchanged weekly counter.
func TestCurrentNPCShopExpirationMidTransactionSaveFailureRollsBackEverything(t *testing.T) {
	shop, items := realPVF535(t)
	repos := mustCurrentNPCShopRepositories(t, 100000000, nil)
	settlementInner := repos.CharacterSettlement
	var saveCalls int64
	repos.CharacterSettlement = expirationFailSaveSettlement{CharacterSettlementUnitOfWork: settlementInner, saveCalls: &saveCalls}
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-expiration-fault", selectedCharacterID: 19}
	ctx := context.Background()
	if _, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
		currentNPCShopBuyRequest{ItemID: 490010214, Count: 1, ActorContext: 1}); err == nil {
		t.Fatal("injected repository save failure must fail the purchase")
	}
	if atomic.LoadInt64(&saveCalls) == 0 {
		t.Fatal("the failing Save was never called, so no post-mutation fault was injected")
	}
	repos.CharacterSettlement = settlementInner
	rows, total, gold := expirationTalismanRows(t, repos)
	if rows != 0 || total != 0 {
		t.Fatalf("rollback left rows=%d total=%d want none", rows, total)
	}
	if gold != 100000000 {
		t.Fatalf("rollback left gold=%d want 100000000", gold)
	}
	conn.write.Reset()
	request := make([]byte, 4)
	binary.LittleEndian.PutUint32(request, 3136)
	if err := service.handleCurrentNPCShopPurchaseCount(session, request); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	want := buildCurrentNPCShopPurchaseCountBody(map[uint32]uint32{})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("weekly counter advanced on a failed transaction: body=%x want=%x", ack.Body, want)
	}
}

// DailyDeleteItem keeps its own path: the retired-absolute-date normalization
// must not apply; same-day purchases retain their daily expiry and merge.
func TestCurrentNPCShopExpirationDailyDeleteItemKeepsItsOwnPath(t *testing.T) {
	source := bridgePVFSource{
		"monster/monster.lst":      "",
		"equipment/equipment.lst":  "",
		"stackable/stackable.lst":  "601 " + expirationPVFQuote + "daily_delete.stk" + expirationPVFQuote + "\n",
		"itemshop/itemshop.lst":    "400000044 " + expirationPVFQuote + "daily_shop.shp" + expirationPVFQuote + "\n",
		"itemshop/daily_shop.shp":  "[NPC]\n3136\n[item list]\n601\n[/item list]\n",
		"equipment/pricetable.tbl": "[rate]\n200 150 30\n",
		"stackable/daily_delete.stk": "[price]\n50000\n[value]\n1\n[stackable type]\n" +
			expirationPVFQuote + "[material]" + expirationPVFQuote + "\n[daily delete item]\n1\n",
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	if definition, err := items.ResolveItem(601); err != nil {
		t.Fatalf("resolve 601: %v", err)
	} else if !definition.DailyDeleteItem {
		t.Fatal("synthetic item is not marked DailyDeleteItem")
	}
	repos := mustCurrentNPCShopRepositories(t, 100000000, nil)
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-expiration-daily", selectedCharacterID: 19}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
			currentNPCShopBuyRequest{ItemID: 601, Count: 1, ActorContext: 1}); err != nil {
			t.Fatalf("daily purchase %d: %v", i+1, err)
		}
	}
	record, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	rows := 0
	for _, stack := range record.Slots {
		if stack.ItemID != 601 {
			continue
		}
		rows++
		if stack.ExpireAt.Equal(currentPVFRetiredExpirationRenewalDate) {
			t.Fatalf("daily item took the retired-date renewal value: %s", stack.ExpireAt)
		}
	}
	// Same-day daily-delete purchases legitimately stack in one row; what matters
	// here is that the daily path keeps its own expiry and is not rewritten by the
	// retired-date normalization.
	if rows != 1 {
		t.Fatalf("daily item rows=%d want 1 (daily path unchanged)", rows)
	}
	total := int64(0)
	for _, stack := range record.Slots {
		if stack.ItemID == 601 {
			total += stack.Count
		}
	}
	if total != 2 {
		t.Fatalf("daily item total=%d want 2", total)
	}
}

// A pre-existing row with a legitimately different instance expiry must never be
// absorbed by a new purchase.
func TestCurrentNPCShopExpirationDifferentInstanceExpiryDoesNotMerge(t *testing.T) {
	shop, items := realPVF535(t)
	seedExpire := time.Date(2035, 1, 1, 6, 0, 0, 0, time.UTC)
	seeded := dnfrepo.ItemStack{ItemID: 490010214, Count: 5, ExpireAt: seedExpire}
	seeded, _ = applyCurrentItemListExpireTimeRaw(seeded, seedExpire)
	key := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, 121)
	repos := mustCurrentNPCShopRepositories(t, 100000000, map[string]dnfrepo.ItemStack{key: seeded})
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-expiration-otherexpiry", selectedCharacterID: 19}
	ctx := context.Background()
	if _, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
		currentNPCShopBuyRequest{ItemID: 490010214, Count: 1, ActorContext: 1}); err != nil {
		t.Fatalf("purchase next to a different-expiry row: %v", err)
	}
	record, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	rows, total, keptFive := 0, int64(0), false
	for _, stack := range record.Slots {
		if stack.ItemID != 490010214 {
			continue
		}
		rows++
		total += stack.Count
		if stack.Count == 5 && stack.ExpireAt.Equal(seedExpire) {
			keptFive = true
		}
	}
	if rows != 2 || total != 6 {
		t.Fatalf("different-expiry rows=%d total=%d want rows=2 total=6", rows, total)
	}
	if !keptFive {
		t.Fatal("the pre-existing different-expiry row was modified")
	}
}
