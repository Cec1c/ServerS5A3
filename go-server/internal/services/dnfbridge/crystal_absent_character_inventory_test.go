package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Real empty character inventory must not disguise a failed account warehouse
// snapshot. The account can already own crystals through another character.
func TestCrystalAccountSnapshotFailureCannotBecomeEmptyAbsentCharacterBootstrap(t *testing.T) {
	for _, fullBootstrap := range []bool{false, true} {
		t.Run(map[bool]string{false: "body_boundary", true: "complete_bootstrap"}[fullBootstrap], func(t *testing.T) {
			ctx := context.Background()
			repos := testRepositoryGroup()
			accountID := "dnf:crystal-account-existing-new-character"
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
			accountBefore, _, _ := repos.AccountInventory.Load(ctx, accountID)
			// Deliberately no character inventory record: healthy empty is a
			// supported bootstrap case, while account inventory is nonempty.
			failing := &crystalColdLoginAccountSnapshotRepository{AccountInventoryRepository: repos.AccountInventory, failNext: true}
			repos.AccountInventory = failing
			service := &Service{options: options{accountID: accountID}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			connection := &bufferConn{}
			session := &gameSession{conn: connection, connID: "crystal-absent-character-boundary", selectedCharacterID: 77, accountID: accountID, itemListMigrationsDone: true}
			if fullBootstrap {
				err := service.sendCurrentSelectInventoryBootstrap(session, "absent_character_account_snapshot_failure")
				t.Logf("error=%v account_loads=%d sent_bytes=%d gate=%t character_gate=%d", err, failing.loads, connection.write.Len(), session.selectedItemListRefreshSent, session.selectedItemListBootstrapCharacterID)
				if err == nil || connection.write.Len() != 0 || session.selectedItemListRefreshSent || session.selectedItemListBootstrapCharacterID != 0 {
					t.Fatal("account snapshot failure accepted an empty crystal warehouse or committed bootstrap")
				}
			} else {
				body, source, count, ok := service.buildCurrentSelectInventoryBootstrapBody(ctx, session, 0)
				t.Logf("ok=%t source=%s rows=%d bytes=%d account_loads=%d", ok, source, count, len(body), failing.loads)
				if ok || len(body) != 0 {
					t.Fatal("failed account warehouse became a healthy empty character container")
				}
			}
			if failing.loads != 1 || failing.failNext {
				t.Fatalf("fault did not hit main account snapshot: loads=%d pending=%t", failing.loads, failing.failNext)
			}
			var healthyBody []byte
			if fullBootstrap {
				if err := service.sendCurrentSelectInventoryBootstrap(session, "healthy_absent_character_retry"); err != nil {
					t.Fatal(err)
				}
				_, lists, _ := requireCurrentSelectInventoryBootstrapPackets(t, connection.write.Bytes())
				healthyBody = lists[0].Body
				if !session.selectedItemListRefreshSent || session.selectedItemListBootstrapCharacterID != 77 {
					t.Fatal("healthy retry did not commit")
				}
				bytesBefore := connection.write.Len()
				if err := service.sendCurrentSelectInventoryBootstrap(session, "duplicate_healthy_absent_character_retry"); err != nil {
					t.Fatal(err)
				}
				if connection.write.Len() != bytesBefore {
					t.Fatal("duplicate healthy retry sent again")
				}
			} else {
				body, _, count, ok := service.buildCurrentSelectInventoryBootstrapBody(ctx, session, 0)
				if !ok || count != 1 {
					t.Fatalf("healthy account retry rejected: ok=%t count=%d", ok, count)
				}
				healthyBody = body
			}
			rows := decodeCurrentItemListTestRows(t, healthyBody[5:])
			if len(rows) != 1 {
				t.Fatalf("healthy retry lost crystal warehouse: rows=%d", len(rows))
			}
			assertCurrentItemListTestRow(t, rows[0], 354, 3033, 13)
			if binary.LittleEndian.Uint32(rows[0][0x0E:0x12]) != 3033 || rows[0][0x59] != 0x5a {
				t.Fatal("crystal identity changed")
			}
			accountAfter, _, err := failing.AccountInventoryRepository.Load(ctx, accountID)
			if err != nil || !reflect.DeepEqual(accountBefore, accountAfter) {
				t.Fatal("warehouse snapshot/retry mutated durable account")
			}
			_, characterInventoryFound, err := repos.Inventory.Load(ctx, "77")
			if err != nil || characterInventoryFound {
				t.Fatal("snapshot invented a character inventory record")
			}
		})
	}
}

func TestCrystalAbsentCharacterBootstrapRetainsHealthyEmptyAccountAndOptionalRepository(t *testing.T) {
	for _, withAccountRepository := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional_absent", true: "healthy_empty"}[withAccountRepository], func(t *testing.T) {
			repos := testRepositoryGroup()
			if !withAccountRepository {
				repos.AccountInventory = nil
			}
			service := &Service{options: options{accountID: "healthy-empty-account"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{connID: "healthy-empty-character", selectedCharacterID: 77, itemListMigrationsDone: true}
			body, _, count, ok := service.buildCurrentSelectInventoryBootstrapBody(context.Background(), session, 0)
			if !ok || count != 0 || len(body) != 5 {
				t.Fatalf("true empty rejected: ok=%t count=%d bytes=%d", ok, count, len(body))
			}
		})
	}
}
