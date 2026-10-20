package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestHealthyAbsentCharacterBeforeInitialMigrationsStillReadsTrueAccountWarehouse(t *testing.T) {
	for _, accountReadFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy_account", true: "explicit_account_error"}[accountReadFails], func(t *testing.T) {
			ctx := context.Background()
			repos := testRepositoryGroup()
			accountID := "dnf:migrations-absent-character"
			if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: accountID}); err != nil {
				t.Fatal(err)
			}
			raw := make([]byte, currentItemListEntryWireSize)
			binary.LittleEndian.PutUint32(raw[0x0E:0x12], 3033)
			raw[0x59] = 0x5a
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: accountID, Slots: map[string]dnfrepo.ItemStack{"0:354": {ItemID: 3033, Count: 13, RawEntry: raw}}}); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.AccountInventory.Load(ctx, accountID)
			snapshot := &crystalColdLoginAccountSnapshotRepository{AccountInventoryRepository: repos.AccountInventory, failNext: accountReadFails}
			repos.AccountInventory = snapshot
			service := &Service{options: options{accountID: accountID}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			connection := &bufferConn{}
			session := &gameSession{conn: connection, connID: "migration-absent-character", selectedCharacterID: 77, accountID: accountID}
			err := service.sendCurrentSelectInventoryBootstrap(session, "first_migration_absent_character")
			t.Logf("account_failure=%t error=%v account_loads=%d bytes=%d gate=%t", accountReadFails, err, snapshot.loads, connection.write.Len(), session.selectedItemListRefreshSent)
			if accountReadFails {
				if err == nil || connection.write.Len() != 0 || session.selectedItemListRefreshSent || snapshot.loads != 1 {
					t.Fatal("explicit account snapshot failure was accepted")
				}
				if err := service.sendCurrentSelectInventoryBootstrap(session, "healthy_retry_after_initial_migration_absence"); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal("healthy absent character failed initial account snapshot", err)
			}
			_, lists, _ := requireCurrentSelectInventoryBootstrapPackets(t, connection.write.Bytes())
			rows := decodeCurrentItemListTestRows(t, lists[0].Body[5:])
			if len(rows) != 1 {
				t.Fatalf("true warehouse missing after migration absence: rows=%d", len(rows))
			}
			assertCurrentItemListTestRow(t, rows[0], 354, 3033, 13)
			if rows[0][0x59] != 0x5a {
				t.Fatal("native account identity tail changed")
			}
			after, _, err := snapshot.AccountInventoryRepository.Load(ctx, accountID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("healthy absence/retry mutated warehouse")
			}
			_, found, err := repos.Inventory.Load(ctx, "77")
			if err != nil || found {
				t.Fatal("healthy absence/retry invented character inventory")
			}
		})
	}
}
