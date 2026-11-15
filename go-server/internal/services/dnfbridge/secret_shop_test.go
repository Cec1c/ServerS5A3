package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/secretshop"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func realSecretShopFixture(t *testing.T) (*blackDiamondShopFixture, *runtimeDungeonState, *secretshop.Catalog) {
	t.Helper()
	t.Setenv("DNFBRIDGE_BLACK_DIAMOND_SECRET_SHOP", "on")
	f := newBlackDiamondShopFixture(t, 3000, 0, 1000000)
	f.service.options.currentClientVersion = "official-24.9"
	_, r, _, _, _ := prepareCompletedSettlementRuntime(t)
	r.Character.CharacterID = "19"
	r.Character.AccountID = blackDiamondShopAccountID
	r.AccountID = blackDiamondShopAccountID
	r.Dungeon.ID = 7146
	r.Dungeon.Metadata.TutorialDungeon = worldmap.OptionalInt{}
	r.Dungeon.Metadata.BasisLevel = worldmap.OptionalInt{Value: 90, Set: true}
	r.entryCharacterLevel = 90
	f.session.dungeon.runtime = r
	catalog, err := secretshop.Load(f.shop.items.source)
	if err != nil {
		t.Fatal(err)
	}
	f.service.secretShops.once.Do(func() { f.service.secretShops.catalog = catalog })
	f.service.dungeonChoice = func(int) (int, error) { return 0, nil }
	return f, r, catalog
}

func TestRealSecretShopBlackDiamondFourRowsAndDoubleStock(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	scene, _ := r.Session.Scene()
	encounter := &currentSecretShopEncounter{NPCID: 1003, Nonce: [2]int64{123, 456}, Members: map[uint16]bool{19: false}}
	normal, err := f.service.buildCurrentSecretShop(catalog, encounter, r, scene, false)
	if err != nil {
		t.Fatal(err)
	}
	black, err := f.service.buildCurrentSecretShop(catalog, encounter, r, scene, true)
	if err != nil {
		t.Fatal(err)
	}
	if normal == nil || black == nil || len(normal.Rows) != 2 || len(black.Rows) != 4 {
		t.Fatalf("ordinary/black rows: %+v / %+v", normal, black)
	}
	for i, row := range normal.Rows {
		if black.Rows[i].Candidate.ItemID != row.Candidate.ItemID || black.Rows[i].Remaining != row.Remaining*2 {
			t.Fatalf("PVF stock multiplier: normal=%+v black=%+v", row, black.Rows[i])
		}
	}
	r.secretShop = black
	if err = f.service.sendCurrentSecretShopLocked(f.session, r); err != nil {
		t.Fatal(err)
	}
	body := shopPackets(t, f.service, f.connection, 280)
	if len(body) != 1 || len(body[0]) != 92 || binary.LittleEndian.Uint32(body[0]) != 4 {
		t.Fatalf("native 4x22-byte rows: %x", body)
	}
	if err = f.service.sendCurrentSecretShopLocked(f.session, r); err != nil {
		t.Fatal(err)
	}
	if len(shopPackets(t, f.service, f.connection, 279)) != 1 {
		t.Fatal("appearance replay rerolled/resent")
	}
}

func TestRealSecretShopAppearanceFrozenFromPVFAndRealEntitlement(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	f.seedPackage(t)
	f.openPackage(t)
	f.useVoucher(t)
	f.connection.write.Reset()
	weights, err := catalog.WeightPairs(7146, 90, 1)
	if err != nil {
		t.Fatal(err)
	}
	offset := 0
	for _, v := range weights {
		if v.NPCID == 1003 {
			break
		}
		offset += int(v.Weight)
	}
	calls := 0
	f.service.dungeonChoice = func(limit int) (int, error) {
		calls++
		if calls == 1 {
			return offset, nil
		}
		return 0, nil
	}
	scene, _ := r.Session.Scene()
	if err = f.service.offerCurrentSecretShopLocked(f.session, r, scene); err != nil {
		t.Fatal(err)
	}
	if r.secretShop == nil || r.secretShop.Encounter.NPCID != 1003 || !r.secretShop.Encounter.Members[19] || len(r.secretShop.Rows) != 4 {
		t.Fatalf("real entitlement offer: %+v", r.secretShop)
	}
	f.service.dungeonChoice = func(int) (int, error) { t.Fatal("appearance rerolled"); return 0, nil }
	if err = f.service.offerCurrentSecretShopLocked(f.session, r, scene); err != nil {
		t.Fatal(err)
	}
	if len(shopPackets(t, f.service, f.connection, 280)) != 1 {
		t.Fatal("duplicate clear emitted another shelf")
	}
}

func secretShopBuyBody(id, count uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, id)
	binary.LittleEndian.PutUint32(b[4:], count)
	return b
}
func installRealSecretShop(t *testing.T, f *blackDiamondShopFixture, r *runtimeDungeonState, catalog *secretshop.Catalog) *currentSecretShop {
	t.Helper()
	scene, _ := r.Session.Scene()
	shop, err := f.service.buildCurrentSecretShop(catalog, &currentSecretShopEncounter{NPCID: 1003, Nonce: [2]int64{321, 654}, Members: map[uint16]bool{19: false}}, r, scene, false)
	if err != nil || shop == nil {
		t.Fatal(err)
	}
	shop.Sent = true
	shop.Open = true
	r.secretShop = shop
	return shop
}

func TestRealSecretShopPurchaseAtomicStockAndWireReplay(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	shop := installRealSecretShop(t, f, r, catalog)
	row := shop.Rows[0]
	qty := uint32(2)
	seq := uint64(100)
	request := gameplayRequest{Opcode: 296, Classification: 1, Body: secretShopBuyBody(row.Candidate.ItemID, qty), Sequence: &seq}
	if err := f.service.handleCurrentSecretShop(f.session, request); err != nil {
		t.Fatal(err)
	}
	gold := f.gold(t)
	if gold != 1000000-row.Candidate.Price*2 || shop.Rows[0].Remaining != row.Remaining-2 {
		t.Fatalf("purchase: gold=%d row=%+v", gold, shop.Rows[0])
	}
	inventory := f.inventory(t)
	character, _, _ := f.repositories.Character.Load(context.Background(), "19")
	ack := shopPackets(t, f.service, f.connection, 296)
	if len(ack) != 1 || len(ack[0]) != 30 || ack[0][0] != 1 || binary.LittleEndian.Uint32(ack[0][26:]) != row.Remaining-2 {
		t.Fatalf("native ACK: %x", ack)
	}
	if err := f.service.handleCurrentSecretShop(f.session, request); err != nil {
		t.Fatal(err)
	}
	after, _, _ := f.repositories.Character.Load(context.Background(), "19")
	if f.gold(t) != gold || !reflect.DeepEqual(f.inventory(t), inventory) || !reflect.DeepEqual(after, character) {
		t.Fatal("wire replay charged or granted twice")
	}
	// Same sequence with a changed quantity is a conflict, never another buy.
	request.Body = secretShopBuyBody(row.Candidate.ItemID, 3)
	if err := f.service.handleCurrentSecretShop(f.session, request); err != nil {
		t.Fatal(err)
	}
	if f.gold(t) != gold || !reflect.DeepEqual(f.inventory(t), inventory) {
		t.Fatal("conflicting sequence mutated assets")
	}
	seq++
	request.Body = secretShopBuyBody(row.Candidate.ItemID, row.Remaining)
	if err := f.service.handleCurrentSecretShop(f.session, request); err != nil {
		t.Fatal(err)
	}
	if f.gold(t) != gold {
		t.Fatal("insufficient stock consumed gold")
	}
	seq++
	request.Body = secretShopBuyBody(row.Candidate.ItemID, 1)
	if err := f.service.handleCurrentSecretShop(f.session, request); err != nil {
		t.Fatal(err)
	}
	acks := shopPackets(t, f.service, f.connection, 296)
	last := acks[len(acks)-1]
	if len(last) != 30 || last[0] != 1 || binary.LittleEndian.Uint32(last[11:15]) != 3 {
		t.Fatalf("native ACK must set merged final count=3: %x", last)
	}
}

type secretShopUnknownCommit struct {
	repo.AccountCharacterAssetUnitOfWork
	ledger repo.AccountCharacterLedgerAssetUnitOfWork
	once   bool
}

func (u *secretShopUnknownCommit) WithinAccountCharacterLedgerAssets(ctx context.Context, a, c string, apply func(repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	err := u.ledger.WithinAccountCharacterLedgerAssets(ctx, a, c, apply)
	if err == nil && !u.once {
		u.once = true
		return errors.New("synthetic commit response lost after durable commit")
	}
	return err
}
func TestRealSecretShopUnknownCommitRetryDoesNotChargeAgain(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	shop := installRealSecretShop(t, f, r, catalog)
	row := shop.Rows[0]
	wrapper := &secretShopUnknownCommit{AccountCharacterAssetUnitOfWork: f.repositories.AccountAssets, ledger: f.repositories.AccountAssets.(repo.AccountCharacterLedgerAssetUnitOfWork)}
	f.repositories.AccountAssets = wrapper
	f.service.repositoryProvider = func() (repo.Group, bool) { return f.repositories, true }
	seq := uint64(888)
	if _, _, err := f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq); err == nil {
		t.Fatal("lost commit response not injected")
	}
	gold := f.gold(t)
	inventory := f.inventory(t)
	result, left, err := f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq)
	if err != nil || result.ItemID != row.Candidate.ItemID || left != row.Remaining-1 || gold != 1000000-row.Candidate.Price || f.gold(t) != gold || !reflect.DeepEqual(f.inventory(t), inventory) {
		t.Fatalf("unknown commit recovery: result=%+v left=%d err=%v", result, left, err)
	}
}

func TestRealSecretShopPhysicalMaterialRollbackAndLockedExpiry(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	shop := installRealSecretShop(t, f, r, catalog)
	// Synthetic material quote using the real physical 7454 item. It occupies
	// main-bag stacks; cube materials such as3037 belong to fixed shared cells.
	shop.Rows[0].Candidate.CostMode = 1
	shop.Rows[0].Candidate.CostItemID = 7454
	shop.Rows[0].Candidate.Price = 3
	inventory := f.inventory(t)
	if inventory.Slots == nil {
		inventory.Slots = map[string]repo.ItemStack{}
	}
	inventory.Slots["0:52"] = repo.ItemStack{ItemID: 7454, Count: 2}
	inventory.Slots["0:53"] = repo.ItemStack{ItemID: 7454, Count: 2}
	if err := f.repositories.Inventory.Save(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	seq := uint64(1)
	wrapper := &secretShopUnknownCommit{AccountCharacterAssetUnitOfWork: f.repositories.AccountAssets, ledger: f.repositories.AccountAssets.(repo.AccountCharacterLedgerAssetUnitOfWork)}
	f.repositories.AccountAssets = wrapper
	f.service.repositoryProvider = func() (repo.Group, bool) { return f.repositories, true }
	if _, _, err := f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq); err == nil {
		t.Fatal("lost commit response not injected")
	}
	result, left, err := f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq)
	if err != nil || left != shop.Rows[0].Remaining-1 {
		t.Fatal(err, left)
	}
	updated := map[uint16]bool{}
	for _, entry := range result.Updates {
		updated[binary.LittleEndian.Uint16(entry.data[:2])] = true
	}
	if !updated[52] || !updated[53] || !updated[354] {
		t.Fatalf("lost response retry omitted changed material/product slots: %v", updated)
	}
	if f.gold(t) != 1000000 {
		t.Fatal("material purchase charged gold")
	}
	inv := f.inventory(t)
	var material int64
	for _, v := range inv.Slots {
		if v.ItemID == 7454 {
			material += v.Count
		}
	}
	if material != 1 {
		t.Fatal(material, inv)
	}
	before := f.inventory(t)
	seq++
	if _, _, err = f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq); err == nil {
		t.Fatal("insufficient material accepted")
	}
	if !reflect.DeepEqual(f.inventory(t), before) {
		t.Fatal("failed transaction changed inventory")
	}
	inv.Slots["0:53"] = repo.ItemStack{ItemID: 7454, Count: 30, ExpireAt: time.Now().Add(-time.Hour)}
	if err = f.repositories.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq); err == nil {
		t.Fatal("expired material accepted")
	}
	inv.Slots["0:53"] = repo.ItemStack{ItemID: 7454, Count: 30, Extra: map[string]string{"expire_time": strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)}}
	if err = f.repositories.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq); err == nil {
		t.Fatal("extra-only expired material accepted")
	}
	inv.Slots["0:53"] = repo.ItemStack{ItemID: 7454, Count: 30, Extra: map[string]string{"equipment_lock_state": "locked"}}
	if err = f.repositories.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, &seq); err == nil {
		t.Fatal("locked material accepted")
	}
}

func TestRealSecretShopRebuiltQuoteAndDurableStockIdentity(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	scene, _ := r.Session.Scene()
	e := &currentSecretShopEncounter{NPCID: 1003, Nonce: [2]int64{12345, 67890}, Members: map[uint16]bool{19: false}}
	first, err := f.service.frozenCurrentSecretShop(catalog, e, r, scene, false, 19)
	if err != nil {
		t.Fatal(err)
	}
	f.service.dungeonChoice = func(int) (int, error) { t.Fatal("existing member quote rerolled"); return 0, nil }
	rebuilt, err := f.service.frozenCurrentSecretShop(catalog, e, r, scene, false, 19)
	if err != nil || !reflect.DeepEqual(first.Rows, rebuilt.Rows) {
		t.Fatal("row identity changed", err)
	}
	seq := uint64(10)
	_, left, err := f.service.commitCurrentSecretShopBuy(context.Background(), f.session, first, 0, 1, &seq)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.restoreCurrentSecretShopStock(f.session, rebuilt); err != nil || rebuilt.Rows[0].Remaining != left {
		t.Fatal("durable stock not restored", err, rebuilt.Rows[0].Remaining, left)
	}
	before := f.inventory(t)
	gold := f.gold(t)
	rebuilt.Rows[0], rebuilt.Rows[1] = rebuilt.Rows[1], rebuilt.Rows[0]
	seq++
	if _, _, err = f.service.commitCurrentSecretShopBuy(context.Background(), f.session, rebuilt, 0, 1, &seq); err == nil {
		t.Fatal("reordered quote used stock for another item")
	}
	if gold != f.gold(t) || !reflect.DeepEqual(before, f.inventory(t)) {
		t.Fatal("mismatched quote mutated assets")
	}
}

func TestRealSecretShopRejectsInactiveWrongOwnerClosedAndMalformed(t *testing.T) {
	f, r, catalog := realSecretShopFixture(t)
	shop := installRealSecretShop(t, f, r, catalog)
	id := shop.Rows[0].Candidate.ItemID
	before := f.inventory(t)
	for _, request := range []gameplayRequest{{Opcode: 296, Body: nil}, {Opcode: 296, Body: secretShopBuyBody(id, 0)}, {Opcode: 296, Body: secretShopBuyBody(99999999, 1)}} {
		if err := f.service.handleCurrentSecretShop(f.session, request); err != nil {
			t.Fatal(err)
		}
	}
	shop.Open = false
	if err := f.service.handleCurrentSecretShop(f.session, gameplayRequest{Opcode: 296, Body: secretShopBuyBody(id, 1)}); err != nil {
		t.Fatal(err)
	}
	shop.Open = true
	r.Character.CharacterID = "999"
	if err := f.service.handleCurrentSecretShop(f.session, gameplayRequest{Opcode: 296, Body: secretShopBuyBody(id, 1)}); err != nil {
		t.Fatal(err)
	}
	r.Character.CharacterID = "19"
	r.townReturnPending = true
	if err := f.service.handleCurrentSecretShop(f.session, gameplayRequest{Opcode: 296, Body: secretShopBuyBody(id, 1)}); err != nil {
		t.Fatal(err)
	}
	if f.gold(t) != 1000000 || !reflect.DeepEqual(before, f.inventory(t)) {
		t.Fatal("invalid context mutated assets")
	}
	r.townReturnPending = false
	f.service.mu.Lock()
	f.service.gameSessions[19] = &gameSession{selectedCharacterID: 19}
	f.service.mu.Unlock()
	if _, _, err := f.service.commitCurrentSecretShopBuy(context.Background(), f.session, shop, 0, 1, nil); err == nil {
		t.Fatal("replaced socket bought from stale runtime")
	}
	if f.gold(t) != 1000000 || !reflect.DeepEqual(before, f.inventory(t)) {
		t.Fatal("replaced connection mutated assets")
	}
}
