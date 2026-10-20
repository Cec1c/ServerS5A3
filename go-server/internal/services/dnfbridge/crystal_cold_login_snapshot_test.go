package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type crystalColdLoginAccountSnapshotRepository struct {
	dnfrepo.AccountInventoryRepository
	loads    int
	failNext bool
}

func (r *crystalColdLoginAccountSnapshotRepository) Load(ctx context.Context, key string) (dnfrepo.AccountInventoryRecord, bool, error) {
	r.loads++
	if r.failNext {
		r.failNext = false
		return dnfrepo.AccountInventoryRecord{}, false, errors.New("injected account crystal snapshot failure")
	}
	return r.AccountInventoryRepository.Load(ctx, key)
}

func TestCrystalColdLoginBootstrapRejectsFailedAccountSnapshotAndRetries(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	character := dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 1001, Count: 2}}}
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], 3033)
	raw[0x59] = 0x5a
	account := dnfrepo.AccountInventoryRecord{AccountID: "dnf:crystal-cold", Slots: map[string]dnfrepo.ItemStack{"0:354": {ItemID: 3033, Count: 13, RawEntry: raw}}}
	if err := repos.Inventory.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	characterBefore, _, _ := repos.Inventory.Load(ctx, "77")
	accountBefore, _, _ := repos.AccountInventory.Load(ctx, account.AccountID)
	failing := &crystalColdLoginAccountSnapshotRepository{AccountInventoryRepository: repos.AccountInventory, failNext: true}
	repos.AccountInventory = failing
	service := &Service{options: options{accountID: account.AccountID}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	// Migrations have already completed. No PVF archive is installed, so cube,
	// epic-piece, premium and expiration reconciliation cannot consume the
	// injected account read. Assert its load count at the failing boundary.
	session := &gameSession{conn: conn, connID: "crystal-cold-snapshot", selectedCharacterID: 77, itemListMigrationsDone: true}
	if err := service.sendCurrentSelectInventoryBootstrap(session, "cold_snapshot_failure"); err == nil {
		t.Fatal("account read failure accepted an incomplete bootstrap")
	}
	if failing.loads != 1 || failing.failNext {
		t.Fatalf("fault was not the list0 account snapshot: loads=%d pending=%t", failing.loads, failing.failNext)
	}
	if conn.write.Len() != 0 || session.selectedItemListRefreshSent || session.selectedItemListBootstrapCharacterID != 0 {
		t.Fatalf("failed snapshot sent bytes=%d or committed gates sent=%t char=%d", conn.write.Len(), session.selectedItemListRefreshSent, session.selectedItemListBootstrapCharacterID)
	}
	if err := service.sendCurrentSelectInventoryBootstrap(session, "healthy_snapshot_retry"); err != nil {
		t.Fatal(err)
	}
	rest, lists, _ := requireCurrentSelectInventoryBootstrapPackets(t, conn.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing packets: %x", rest)
	}
	body := lists[0].Body
	if binary.LittleEndian.Uint16(body[3:5]) != 2 {
		t.Fatalf("list0 row count=%d want=2", binary.LittleEndian.Uint16(body[3:5]))
	}
	rows := decodeCurrentItemListTestRows(t, body[5:])
	assertCurrentItemListTestRow(t, rows[0], 3, 1001, 2)
	assertCurrentItemListTestRow(t, rows[1], 354, 3033, 13)
	if binary.LittleEndian.Uint32(rows[1][0x0E:0x12]) != 3033 || rows[1][0x59] != 0x5a {
		t.Fatalf("crystal identity tail changed: %x", rows[1])
	}
	if !session.selectedItemListRefreshSent || session.selectedItemListBootstrapCharacterID != 77 {
		t.Fatal("healthy bootstrap did not commit gates")
	}
	bytesBefore, loadsBefore := conn.write.Len(), failing.loads
	if err := service.sendCurrentSelectInventoryBootstrap(session, "duplicate_healthy_snapshot"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != bytesBefore || failing.loads != loadsBefore {
		t.Fatal("duplicate successful bootstrap sent packets or reloaded the account")
	}
	characterAfter, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	accountAfter, _, err := failing.AccountInventoryRepository.Load(ctx, account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(characterBefore, characterAfter) || !reflect.DeepEqual(accountBefore, accountAfter) {
		t.Fatal("snapshot error/retry mutated durable inventory")
	}
}

func TestCrystalColdLoginMainSnapshotKeepsOptionalRepositoryAndTrueEmptyAccount(t *testing.T) {
	for _, withAccount := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional_repository_absent", true: "healthy_account_record_absent"}[withAccount], func(t *testing.T) {
			ctx := context.Background()
			repos := testRepositoryGroup()
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 1001, Count: 2}}}); err != nil {
				t.Fatal(err)
			}
			if !withAccount {
				repos.AccountInventory = nil
			}
			service := &Service{options: options{accountID: "dnf:absent-crystal-account"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{connID: "crystal-empty-snapshot", selectedCharacterID: 77, itemListMigrationsDone: true}
			body, _, count, ok := service.buildCurrentItemListBodyForSession(ctx, session, 0)
			if !ok || count != 1 {
				t.Fatalf("optional/empty account rejected: ok=%t count=%d", ok, count)
			}
			rows := decodeCurrentItemListTestRows(t, body[5:])
			assertCurrentItemListTestRow(t, rows[0], 3, 1001, 2)
		})
	}
}
